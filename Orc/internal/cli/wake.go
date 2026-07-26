package cli

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/view"
)

// `orc wake` — the cycle that keeps agents from going quiet.
//
// An agent finishes a turn and stops. Nothing is wrong with it: Claude has said its
// piece and is waiting for the next thing somebody says. In a fleet nobody is
// watching, that is where work stops — not because an agent failed, but because it
// did exactly what it was built to do and then nobody spoke.
//
// This is the thing that speaks. It reads the event feed each session writes
// (Plan.md §6.2), finds the ones that have been waiting longer than they should be,
// and pokes them — through `poke`'s own path, so there is still one way text reaches
// a session and one thing to test.
//
// Two rules keep it from becoming noise:
//
//   - **It only wakes what is actually waiting.** A session mid-turn is silent for
//     good reasons — a long build, a slow read — and poking it would queue a
//     "continue" into the middle of work it is already doing.
//   - **It wakes each silence once.** If a poke does not move the agent, poking it
//     again every cycle would fill its context with nudges and hide the fact that it
//     is stuck. The second pass says so instead, and `orc doctor` is where a stuck
//     session belongs.

// The cycle's defaults.
const (
	// DefaultQuiet is how long a waiting session may stay waiting before it is a
	// fleet that has quietly stopped. Long enough that an operator thinking between
	// turns is not interrupted, short enough that an overnight run does not lose
	// the night.
	DefaultQuiet = 10 * time.Minute

	// MinQuiet is the shortest silence worth calling silence. Below this the cycle
	// is racing the agent's own turn boundaries: Claude stops for a moment between
	// tool calls, and a threshold under a minute would poke it for breathing.
	MinQuiet = time.Minute

	// WakeMessage is what a woken agent is told. It is `poke`'s default, and for
	// the same reason: the whole verb is "nudge the identity to continue working".
	WakeMessage = "continue"
)

// wake is `orc wake [<identity>…]`.
//
// With no names it sweeps everything employed in the caller's subtree, which is the
// form the cycle runs in. With names it wakes those, which is the form somebody runs
// by hand after reading a board.
func (a App) wake(args []string) error {
	var quietFor, every, message string
	var dry bool
	rest, err := flagged(args, options{
		values: map[string]*string{
			"--after": &quietFor, "--every": &every, "--message": &message,
		},
		switches: map[string]*bool{"--dry-run": &dry},
	})
	if err != nil {
		return err
	}

	quiet := DefaultQuiet
	if strings.TrimSpace(quietFor) != "" {
		got, err := time.ParseDuration(quietFor)
		if err != nil {
			return fault.Usage{Reason: fmt.Sprintf("--after takes a duration like 10m: %v", err)}
		}
		if got < MinQuiet {
			return fault.Usage{Reason: fmt.Sprintf(
				"--after %s is under %s, which is short enough to poke an agent between its own "+
					"tool calls; %s is the shortest silence worth the name", quietFor, MinQuiet, MinQuiet)}
		}
		quiet = got
	}

	text := strings.TrimSpace(message)
	if text == "" {
		text = WakeMessage
	}

	var names []user.Name
	for _, raw := range rest {
		who, err := user.Parse(raw)
		if err != nil {
			return err
		}
		names = append(names, who)
	}

	cycle := &waker{app: a, quiet: quiet, message: text, dry: dry, names: names, woken: map[string]string{}}

	if strings.TrimSpace(every) == "" {
		return cycle.once(true)
	}

	interval, err := time.ParseDuration(every)
	if err != nil {
		return fault.Usage{Reason: fmt.Sprintf("--every takes a duration like 5m: %v", err)}
	}
	if interval < MinWatch {
		return fault.Usage{Reason: fmt.Sprintf(
			"--every %s is under %s; a cycle tighter than that is a busy-wait, and the silence it "+
				"is looking for is measured in minutes", every, MinWatch)}
	}
	return cycle.loop(interval)
}

// waker is the cycle's state.
//
// The one piece of memory it has is `woken`: the last event a session had shown when
// it was last poked. It lives here, in the process, rather than in the store —
// partly because the store's files belong to another stream, and mostly because it
// is the right shape for it. A wake is not a fact about the fleet worth keeping; it
// is a fact about this cycle's last pass, and a cycle that restarts should look at a
// quiet fleet with fresh eyes.
type waker struct {
	app     App
	quiet   time.Duration
	message string
	dry     bool
	names   []user.Name

	// woken maps an identity to the last event it had when it was poked. A session
	// that has said nothing since is stuck, not silent, and is reported rather than
	// poked again.
	woken map[string]string
}

// once is a single pass.
func (w *waker) once(verbose bool) error {
	s, err := w.app.begin()
	if err != nil {
		return err
	}
	if err := s.mayRunVerb("wake"); err != nil {
		return err
	}

	targets := w.names
	if len(targets) == 0 {
		targets = s.fleet.Employed(s.who)
	}

	woke, stuck, quiet := 0, 0, 0
	for _, who := range targets {
		// A sweep skips what it may not direct rather than failing on it: the
		// caller asked about their fleet, and somebody else's agent being in it is
		// not an error. A named identity is a request, and is refused properly.
		if err := s.controls(who, "wake"); err != nil {
			if len(w.names) > 0 {
				return err
			}
			continue
		}

		got, err := w.consider(s, who)
		if err != nil {
			// One unreadable session is not a reason to stop waking the rest: a
			// fleet with one broken agent should still be kept moving.
			w.app.note("%s could not be checked: %v", who, err)
			continue
		}
		switch got {
		case wokeIt:
			woke++
		case alreadyWoken:
			stuck++
		default:
			quiet++
		}
	}

	if woke == 0 && stuck == 0 && !verbose {
		// The loop is quiet when it has nothing to say. A cycle that printed
		// "nothing to do" every pass is one an operator stops reading.
		return nil
	}
	return w.report(woke, stuck, quiet, len(targets))
}

// outcome is what a pass decided about one identity.
type outcome int

const (
	// working: it is mid-turn, or it has not been quiet long enough.
	working outcome = iota
	// wokeIt: it was waiting, and has been poked.
	wokeIt
	// alreadyWoken: it was poked and has not moved since.
	alreadyWoken
)

// consider decides about one identity, and wakes it if it should.
func (w *waker) consider(s caller, who user.Name) (outcome, error) {
	state, live, err := s.store.Session(who)
	if err != nil {
		return working, err
	}
	if !live {
		// Not this cycle's business. A session that should be running and is not is
		// `tend`'s job, and doing it here would be two things reconciling one fleet.
		return working, nil
	}

	feed, err := view.Load(s.store.EventsPath(who), who)
	if err != nil {
		return working, err
	}

	started, err := state.StartedAt()
	if err != nil {
		return working, err
	}
	last, quiet, ok := silence(feed, started, s.store.Now())
	if !ok {
		return working, nil
	}
	if quiet < w.quiet {
		return working, nil
	}

	// Poked already, and it has said nothing since. Waking it again would bury the
	// problem under nudges.
	//
	// The presence of the key is what says "woken", not the mark's value: a
	// session that has never said anything has no last event, and comparing the
	// empty mark against a missing entry made those two the same thing — so the
	// one agent that most needs waking, the one that has done nothing at all,
	// was reported stuck on its first pass and never poked.
	if mark, ok := w.woken[who.String()]; ok && mark == last {
		if err := w.app.say(fmt.Sprintf("%s %s   %s", w.app.out.Warn("still silent"),
			w.app.out.Identity(who.String()),
			w.app.out.Muted(fmt.Sprintf("%s since the last wake — `orc attach %s` or `orc doctor`",
				round(quiet), who)))); err != nil {
			return working, err
		}
		return alreadyWoken, nil
	}

	if w.dry {
		if err := w.app.say(fmt.Sprintf("%s %s   %s", w.app.out.Muted("would wake"),
			w.app.out.Identity(who.String()), w.app.out.Muted("waiting "+round(quiet)))); err != nil {
			return working, err
		}
		return wokeIt, nil
	}

	client, err := w.app.dial(s, who)
	if err != nil {
		return working, err
	}
	if err := client.Poke(w.message); err != nil {
		return working, err
	}
	w.woken[who.String()] = last

	return wokeIt, w.app.say(fmt.Sprintf("%s %s   %s", w.app.out.Good("woke"),
		w.app.out.Identity(who.String()),
		w.app.out.Muted(fmt.Sprintf("waiting %s · %s", round(quiet), quoteShort(w.message)))))
}

// silence reports how long a session has been waiting for somebody to speak, and
// whether it is waiting at all.
//
// "Waiting" is the feed's own reading of Claude's last event, not a guess from a
// timestamp: a session that has been quiet for an hour because it is running a long
// build is working, and poking it would queue a nudge into the middle of it. The
// distinction is the difference between a cycle that keeps a fleet moving and one
// that talks over it.
//
// A session with no events at all is judged from when it started. An agent that has
// been up for twenty minutes and has never called a tool is exactly as stopped as
// one that finished and waited, and it is the more worrying of the two.
func silence(feed view.Session, started, now time.Time) (last string, quiet time.Duration, waiting bool) {
	row, ok := feed.Last()
	if !ok {
		// Marked by the session's start rather than left blank, so that a refresh
		// — a new session, a new start — reads as a new silence rather than as
		// the same one already woken.
		return "started " + started.Format(time.RFC3339Nano), now.Sub(started), true
	}
	if !feed.Waiting {
		return "", 0, false
	}
	return row.At.Format(time.RFC3339Nano), now.Sub(row.At), true
}

// report says what the pass came to.
func (w *waker) report(woke, stuck, quiet, total int) error {
	if total == 0 {
		return w.app.say(w.app.out.Muted("nothing is employed, so nothing can be silent"))
	}
	if woke == 0 && stuck == 0 {
		return w.app.say(fmt.Sprintf("%s   %s", w.app.out.Good("all working"),
			w.app.out.Muted(fmt.Sprintf("%d session%s, none silent for %s",
				quiet, plural(quiet), round(w.quiet)))))
	}

	line := fmt.Sprintf("%s of %d", w.app.out.Good(fmt.Sprintf("woke %d", woke)), total)
	if stuck > 0 {
		line += fmt.Sprintf("   %s", w.app.out.Warn(fmt.Sprintf("%d still silent after a wake", stuck)))
	}
	return w.app.say(line)
}

// loop is the cycle proper.
//
// It is `tend --watch`'s shape, and deliberately so: a fleet has two backstops now,
// one that keeps sessions *running* and one that keeps them *moving*, and an
// operator should not have to learn two loops.
func (w *waker) loop(interval time.Duration) error {
	if err := w.app.say(fmt.Sprintf("%s   %s", w.app.out.Header("waking"),
		w.app.out.Muted(fmt.Sprintf("every %s, anything waiting over %s · ^C stops",
			round(interval), round(w.quiet))))); err != nil {
		return err
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	failures := 0
	for {
		if err := w.once(false); err != nil {
			failures++
			w.app.note("pass failed: %v", err)
			if failures >= WatchGiveUp {
				return fault.Conflict{Path: "wake --every", Reason: fmt.Sprintf(
					"%d passes in a row failed; the last was: %v", failures, err)}
			}
		} else {
			failures = 0
		}

		select {
		case <-stop:
			return w.app.say("\n" + w.app.out.Muted("stopped"))
		case <-ticker.C:
		}
	}
}

// round trims a duration to something worth reading on a status line.
func round(d time.Duration) string {
	switch {
	case d >= time.Hour:
		return d.Round(time.Minute).String()
	case d >= time.Minute:
		return d.Round(time.Second).String()
	default:
		return d.Round(time.Second).String()
	}
}
