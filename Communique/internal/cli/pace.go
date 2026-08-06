package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"orc/common/keys"
	"orc/common/watch"
	"orc/cq/internal/fault"
	"orc/cq/internal/logbook"
	stored "orc/cq/internal/settings"
	"orc/cq/internal/style"
)

// `cq pace` — one thing to run on an agent machine.
//
// A working fleet needs three cycles up: `cq sync --watch` mirrors the machine,
// `orc wake --every` pokes agents that have gone quiet, and `orc tend --watch`
// puts back sessions that stopped. Each is a long-running process, each has to be
// started, and a machine where one of them quietly died is a machine that looks
// fine and is not — which is the failure the whole backstop design exists to
// prevent and the one thing none of the three can prevent for *itself*.
//
// So this is a supervisor, not a fourth cycle.
//
// That distinction is the whole design. Re-timing the work here would mean a
// second implementation of cadence — and the cadences are not simple: each cycle
// re-reads its own stored interval between passes, honours the fleet/role/identity
// layers, and refuses a value under its own floor. All of that is built and
// tested. Running `orc wake` on a ticker from here would throw it away and drift
// from it, so instead the cycles are started in the form they were designed for
// and kept up.
//
// The keys are the other half. A cycle's job is to be eventually right; a person
// at a terminal wants it now — after an employ, after a queue action, after
// changing a prompt — and the alternative is a second window and a remembered
// command line.
//
//	^S  sync now      ^W  wake now      ^T  tend now      ^C  stop
//
// They run the *one-shot* form of each command, beside the watcher rather than
// through it. A cycle mid-pass is not interrupted and its schedule is not
// disturbed: the two are independent, which is what makes pressing a key safe at
// any moment.

// paceCycle is one supervised cycle.
type paceCycle struct {
	name string     // "sync", "wake", "tend"
	kind watch.Kind // what it registers as, so nothing starts a second one
	key  byte       // the keystroke that runs it once, now
	// tool is the binary and watchArgs the long-running form. onceArgs is the same
	// work done once, which is what a key runs.
	tool      string
	watchArgs []string
	onceArgs  []string
	// ownLog says the tool writes its own logbook, so this must not tee it.
	//
	// `cq sync --watch` keeps its own — it has to, because it is the one cycle
	// somebody also runs on its own, without pace above it. Teeing it here as well
	// would put every line in the file twice, which reads as a loop running at
	// double the rate it is.
	ownLog bool
}

// pace is `cq pace`.
func (a App) pace(args []string) error {
	fs := flag.NewFlagSet("pace", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	home := fs.String("home", a.look("CQ_HOME", defaultAgentDir()), "agent state directory")
	syncEvery := fs.String("sync", "", "how often to mirror; default is whatever the server last asked for")
	wakeEvery := fs.String("wake", DefaultPaceWake.String(), "how often to look for quiet agents")
	tendEvery := fs.String("tend", DefaultPaceTend.String(), "how often to reconcile the work list")
	orcBin := fs.String("orc", a.look("ORC", "orc"), "the orc executable")
	if err := parse(fs, args); err != nil {
		return err
	}

	// Every interval is checked here, before anything is started.
	//
	// They are passed straight to the cycles' own flags, which would refuse them —
	// but by then the refusal is a child exiting instantly, and `keepUp` would
	// restart it forever on a widening backoff. A typo would present as a cycle
	// that never comes up rather than as the typo it is, so it is caught at the
	// one point where it can still be answered plainly.
	for _, given := range []struct{ flag, value string }{
		{"--sync", *syncEvery}, {"--wake", *wakeEvery}, {"--tend", *tendEvery},
	} {
		if strings.TrimSpace(given.value) == "" {
			continue
		}
		got, err := time.ParseDuration(given.value)
		if err != nil || got <= 0 {
			return fault.Usage{Reason: fmt.Sprintf(
				"%s takes a duration with something in it, like 5m — not %q", given.flag, given.value)}
		}
	}

	// Where this binary is, so the sync cycle is *this* build rather than
	// whatever `cq` resolves to on the path — which on a machine mid-upgrade is
	// not the same thing.
	self, err := restartable()
	if err != nil {
		return err
	}

	// The sync interval the server last asked for, so a restart does not go back
	// to a default the operator never chose. A flag beats it, for the run it was
	// typed on.
	every := strings.TrimSpace(*syncEvery)
	if every == "" {
		if chosen, err := stored.Read(*home); err == nil && chosen.Pace != "" {
			every = chosen.Pace
		} else {
			every = DefaultWatch.String()
		}
	}

	cycles := []paceCycle{
		{
			name: "sync", kind: watch.Sync, key: keys.CtrlS, tool: self, ownLog: true,
			watchArgs: []string{"sync", "--watch", every, "--home", *home},
			onceArgs:  []string{"sync", "--home", *home},
		},
		{
			name: "wake", kind: watch.Wake, key: keys.CtrlW, tool: *orcBin,
			watchArgs: []string{"wake", "--every", *wakeEvery, "--tend"},
			onceArgs:  []string{"wake", "--tend"},
		},
		{
			name: "tend", kind: watch.Tend, key: keys.CtrlT, tool: *orcBin,
			watchArgs: []string{"tend", "--watch", *tendEvery},
			onceArgs:  []string{"tend"},
		},
	}

	// Nothing is started beside something already doing the same job.
	//
	// Two wakers on one fleet is not twice the backstop: it is two processes
	// reading the same feed, both deciding an agent is silent, and both poking it
	// — which is the duplicate-prompt problem the delivery path works hard to
	// avoid, arrived at from the other end. The registry is what makes this
	// answerable, and the refusal names what to stop.
	reg := watch.Registry{Dir: watchers(*home)}
	var already []string
	for _, c := range cycles {
		running, err := reg.Running(c.kind)
		if err != nil {
			// A registry that cannot be read is not a reason to refuse: the cost
			// of guessing wrong here is a duplicate cycle, and the cost of
			// refusing is a machine with no cycles at all.
			//
			// What it *is* a reason to do is drop what was found so far. Breaking
			// with a part-built list meant the answer depended on which cycle the
			// read failed at — a failure on the first started everything, and the
			// same failure on the second refused on whatever the first had
			// reported. One unreadable registry, two different behaviours.
			a.tell("cq: could not tell what is already running (%v); starting anyway", err)
			already = nil
			break
		}
		if running {
			already = append(already, c.name)
		}
	}
	if len(already) > 0 {
		return fault.Usage{Reason: fmt.Sprintf(
			"something is already %s on this machine; stop it first, or let it keep doing the job. "+
				"`cq status` lists what is running", strings.Join(already, " and "))}
	}

	return a.pacing(cycles, *home, every)
}

// The intervals `pace` starts the two Orc cycles at when nobody says otherwise.
//
// They are starting points and not policy: both cycles re-read their own stored
// pace between passes, so `orc pace` — or cq's own panel — is what actually
// decides, and these only govern the first round after a start.
const (
	DefaultPaceWake = 5 * time.Minute
	DefaultPaceTend = 30 * time.Second
)

// pacing runs the cycles and the keyboard until it is stopped.
func (a App) pacing(cycles []paceCycle, home, every string) error {
	if err := a.say("%s   %s", a.ink("pacing", style.Good),
		a.ink(fmt.Sprintf("sync every %s · ^S sync  ^W wake  ^T tend  ^C stop", every), style.Quiet)); err != nil {
		return err
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)

	done := make(chan struct{})
	var once sync.Once
	shutdown := func() { once.Do(func() { close(done) }) }

	// Each cycle gets a goroutine that keeps it up. A cycle that exits — crashed,
	// killed, upgraded out from under itself — is started again, because a
	// supervisor whose children can quietly stop is a supervisor that is not one.
	var wg sync.WaitGroup
	for _, c := range cycles {
		wg.Add(1)
		go func(c paceCycle) {
			defer wg.Done()
			a.keepUp(c, home, done)
		}(c)
	}

	// The keyboard, if there is one. A machine running this from a service has no
	// terminal and no keys, and that is a perfectly good way to run it — so this
	// is a missing convenience rather than a failure to start.
	presses := make(chan byte, 4)
	reader, err := keys.Open(os.Stdin)
	switch {
	case errors.Is(err, keys.ErrNotATerminal):
		a.tell("cq: input is not a terminal, so the keys are off; the cycles keep their own time")
	case err != nil:
		a.tell("cq: the keyboard could not be set up (%v); the cycles keep their own time", err)
	default:
		defer func() { _ = reader.Close() }()
		go readKeys(reader, presses, done)
	}

	// Ending happens from two places and must happen the same way from both. The
	// terminal goes back *first*, so anything printed on the way out lands on a
	// working shell rather than on one with no echo.
	finish := func() error {
		shutdown()
		if reader != nil {
			_ = reader.Close()
		}
		wg.Wait()
		return a.say("\n%s", a.ink("stopped", style.Quiet))
	}

	// Which one-shots are in flight.
	//
	// A key runs its command in its own goroutine, because the alternative is what
	// this replaces: `cmd.Run` called from inside the select, holding the loop for
	// however long a sync against a slow server takes — during which ^C and
	// SIGTERM did nothing at all. The most likely moment to want to stop is while
	// something is taking too long, and that was exactly the moment stopping
	// stopped working.
	//
	// A second press while one is still running is *ignored* rather than queued. A
	// key is "do it now", and three syncs stacked up behind an impatient hand are
	// three round trips nobody asked for.
	busy := map[byte]bool{}
	finished := make(chan byte, len(cycles))

	for {
		select {
		case <-stop:
			return finish()

		case k := <-finished:
			delete(busy, k)

		case k := <-presses:
			if k == keys.CtrlC || k == keys.CtrlD {
				return finish()
			}
			c, ok := cycleFor(cycles, k)
			if !ok {
				continue
			}
			if busy[k] {
				a.tell("%s %s is already running", a.ink(keys.Name(k), style.Value), c.name)
				continue
			}
			busy[k] = true
			go func(c paceCycle, k byte) {
				a.runNow(c, home)
				finished <- k
			}(c, k)
		}
	}
}

// cycleFor finds the cycle a keystroke runs, if it is one of them.
func cycleFor(cycles []paceCycle, k byte) (paceCycle, bool) {
	for _, c := range cycles {
		if c.key == k {
			return c, true
		}
	}
	return paceCycle{}, false
}

// logged is where a cycle's child writes: the terminal, and the machine's log.
//
// Both, not one. The terminal is what somebody sitting at this machine is reading
// right now; the log is what the browser will show hours later, from somewhere
// else entirely. Dropping either would take away the only view somebody has.
//
// A log that cannot be opened costs the file and not the cycle. The alternative —
// refusing to start a watcher because a directory would not create — would take a
// fleet down to keep a diagnostic, which is exactly backwards.
func (a App) logged(c paceCycle, home string) (io.Writer, io.Writer) {
	if c.ownLog {
		return a.Stdout, a.Stderr
	}
	w, err := logbook.Open(home, logbook.Kind(c.name))
	if err != nil {
		a.complain(err)
		return a.Stdout, a.Stderr
	}
	// Deliberately not closed. The writer outlives this call by the length of the
	// child's run, and the process holding it exits when pace does — at which point
	// the file is closed by the only thing that can safely close it.
	return io.MultiWriter(a.Stdout, w), io.MultiWriter(a.Stderr, w)
}

// keepUp runs one cycle, and starts it again whenever it stops.
//
// The backoff is why this is not a bare loop. A cycle that cannot start — a
// missing `orc`, a store that will not open — would otherwise be re-executed as
// fast as the machine can fork, and the failure would be buried under its own
// output. Widening the gap turns that into a line somebody can read while leaving
// a genuine crash restarted within a second.
func (a App) keepUp(c paceCycle, home string, done <-chan struct{}) {
	const (
		floor   = time.Second
		ceiling = time.Minute
	)
	wait := floor
	for {
		select {
		case <-done:
			return
		default:
		}

		started := time.Now()
		cmd := exec.Command(c.tool, c.watchArgs...)
		cmd.Stdout, cmd.Stderr = a.logged(c, home)
		if err := cmd.Start(); err != nil {
			a.tell("cq: %s could not start (%v); trying again in %s", c.name, err, round(wait))
			select {
			case <-done:
				return
			case <-time.After(wait):
			}
			if wait < ceiling {
				wait *= 2
			}
			continue
		}

		// The child dies with the parent.
		//
		// A ^C in a terminal reaches the whole process group, so it looks handled
		// — but a SIGTERM from a service manager reaches this process only, and
		// without this the three cycles would outlive it as orphans. The next `cq
		// pace` would then refuse to start, correctly, on registry entries held by
		// processes nobody can find.
		killed := make(chan struct{})
		go func() {
			select {
			case <-done:
				if cmd.Process != nil {
					_ = cmd.Process.Signal(syscall.SIGTERM)
				}
			case <-killed:
			}
		}()
		err := cmd.Wait()
		close(killed)

		select {
		case <-done:
			return
		default:
		}

		// A cycle that ran for a while and then stopped is a different event from
		// one that will not start, and only the second should slow down.
		if time.Since(started) > time.Minute {
			wait = floor
		}
		if err != nil {
			a.tell("cq: %s stopped (%v); starting it again in %s", c.name, err, round(wait))
		} else {
			a.tell("cq: %s exited; starting it again in %s", c.name, round(wait))
		}

		select {
		case <-done:
			return
		case <-time.After(wait):
		}
		if wait < ceiling {
			wait *= 2
		}
	}
}

// runNow does one cycle's work immediately, beside the watcher rather than
// through it.
//
// Its own process, so a key cannot disturb a pass already running and cannot be
// disturbed by one starting. The output goes to the same streams, so what a key
// did is in the same place as what the cycles do.
func (a App) runNow(c paceCycle, home string) {
	a.tell("%s %s now", a.ink(keys.Name(c.key), style.Value), c.name)
	cmd := exec.Command(c.tool, c.onceArgs...)
	cmd.Stdout, cmd.Stderr = a.logged(c, home)
	if err := cmd.Run(); err != nil {
		a.tell("cq: %s now: %v", c.name, err)
	}
}

// readKeys pumps keystrokes until the reader closes.
func readKeys(r *keys.Reader, out chan<- byte, done <-chan struct{}) {
	for {
		k, err := r.Read()
		if err != nil {
			return
		}
		select {
		case out <- k:
		case <-done:
			return
		}
	}
}
