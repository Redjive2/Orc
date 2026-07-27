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
	"orc/common/watch"
	"orc/orc/internal/instruct"
	"orc/orc/internal/model"
	"orc/orc/internal/session"
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

	// WakeMessage is what a woken agent is told when nothing says otherwise. It is
	// `poke`'s default, and `instruct.DefaultWake` — the bottom of the override
	// chain a fleet's, a role's, and an agent's wake messages sit above.
	WakeMessage = instruct.DefaultWake
)

// wake is `orc wake [<identity>…]`.
//
// With no names it sweeps everything employed in the caller's subtree, which is the
// form the cycle runs in. With names it wakes those, which is the form somebody runs
// by hand after reading a board.
func (a App) wake(args []string) error {
	var quietFor, every, message string
	var dry, tend bool
	rest, err := flagged(args, options{
		values: map[string]*string{
			"--after": &quietFor, "--every": &every, "--message": &message,
		},
		switches: map[string]*bool{"--dry-run": &dry, "--tend": &tend},
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

	// An explicit --message is the operator saying what to send *this time*, so it
	// beats everything stored. Left empty, each agent is told whatever its own
	// standing wake message says — see `orc instruct wake`.
	text := strings.TrimSpace(message)
	// An explicit --message is typed into somebody's terminal, so it is held to what
	// a stored wake message is held to. The stored ones go through instruct.Check on
	// the way in; this one has never been near that door.
	if text != "" {
		if err := session.Typeable(text); err != nil {
			return err
		}
	}

	var names []user.Name
	for _, raw := range rest {
		who, err := user.Parse(raw)
		if err != nil {
			return err
		}
		names = append(names, who)
	}

	cycle := &waker{app: a, quiet: quiet, message: text, dry: dry, tend: tend,
		names: names, woken: map[string]string{}}

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
	tend    bool
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

	woke, stuck, quiet, dead := 0, 0, 0, 0
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
			// fleet with one broken agent should still be kept moving. It is counted
			// as not-running rather than dropped, so the summary's numbers add up to
			// the fleet and a pass that could reach nobody does not read as a fleet
			// where everybody is fine.
			w.app.note("%s could not be checked: %v", who, err)
			dead++
			continue
		}
		switch got {
		case wokeIt:
			woke++
		case alreadyWoken:
			stuck++
		case down:
			dead++
		default:
			quiet++
		}
	}

	if woke == 0 && stuck == 0 && dead == 0 && !verbose {
		// The loop is quiet when it has nothing to say. A cycle that printed
		// "nothing to do" every pass is one an operator stops reading — but an agent
		// that is not running at all is something to say.
		return nil
	}
	return w.report(woke, stuck, quiet, dead, len(targets))
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
	// down: the fleet says it should be running and it is not.
	//
	// Not this cycle's job to fix — `tend` reconciles, and two things reconciling
	// one fleet is how a fleet gets two answers — but very much its job to *say*.
	// A cycle whose whole purpose is "nothing has quietly stopped" that stayed
	// silent about an agent which is not running at all would be reporting on the
	// least important kind of silence and hiding the worst.
	down
)

// consider decides about one identity, and wakes it if it should.
func (w *waker) consider(s caller, who user.Name) (outcome, error) {
	state, live, err := s.store.Session(who)
	if err != nil {
		return working, err
	}
	if !live {
		// `tend` is what starts it; this says so rather than passing over it. With
		// --tend the cycle brings it up itself, for the machine where the wake cron
		// entry is the only thing running.
		if !revivable(s, who) {
			return working, nil
		}
		if !w.tend {
			return down, w.app.say(fmt.Sprintf("%s %s   %s", w.app.out.Warn("not running"),
				w.app.out.Identity(who.String()),
				w.app.out.Muted("employed with no session — `orc tend` starts it, or wake --tend")))
		}
		if w.dry {
			return down, w.app.say(fmt.Sprintf("%s %s   %s", w.app.out.Muted("would start"),
				w.app.out.Identity(who.String()), w.app.out.Muted("employed with no session")))
		}
		if err := w.app.tendOne(s, who); err != nil {
			return down, err
		}
		// Started, and that is the state this pass was after: an agent that has just
		// been given a fresh session is not a quiet one, and poking it on top of
		// the nudge `tend` may already have sent would be two messages for one gap.
		return down, w.app.say(fmt.Sprintf("%s %s   %s", w.app.out.Good("started"),
			w.app.out.Identity(who.String()), w.app.out.Muted("it was employed with no session")))
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
	// A negative silence means the clock moved: an event stamped in the future, or a
	// machine whose time was corrected between the event and now. Waking on it would
	// be waking on arithmetic nobody can check, and skipping it silently would let a
	// genuinely stuck agent hide behind a bad timestamp — so it is said out loud and
	// left for the next pass, by which time the clock has usually settled.
	if quiet < 0 {
		w.app.note("%s: its last event is stamped %s in the future, so how long it has been "+
			"quiet cannot be said; check the clock", who, round(-quiet))
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
	// The mark is read from the store as well as from this pass's memory. `orc wake
	// --every` keeps its own; `orc wake` from a cron entry — which is how most
	// machines will run it — starts empty every time, so without the stored one a
	// wedged agent was poked afresh on every invocation and never once reported as
	// stuck. The two ways of running the same command behaved differently in the
	// exact case the command exists for.
	mark, marked := w.woken[who.String()]
	if !marked {
		mark, marked = s.store.Woken(who, state.ID)
	}
	if marked && mark == last {
		if err := w.app.say(fmt.Sprintf("%s %s   %s", w.app.out.Warn("still silent"),
			w.app.out.Identity(who.String()),
			w.app.out.Muted(fmt.Sprintf("%s since the last wake — `orc attach %s` or `orc doctor`",
				round(quiet), who)))); err != nil {
			return working, err
		}
		return alreadyWoken, nil
	}

	// What to say. The flag wins; otherwise the identity's own message, else its
	// role's, else the fleet's, else `continue`.
	message, from, err := w.messageFor(s, who)
	if err != nil {
		return working, err
	}

	if w.dry {
		said := "waiting " + round(quiet)
		if from != "" {
			said += " · " + string(from) + "'s message"
		}
		if err := w.app.say(fmt.Sprintf("%s %s   %s", w.app.out.Muted("would wake"),
			w.app.out.Identity(who.String()), w.app.out.Muted(said))); err != nil {
			return working, err
		}
		return wokeIt, nil
	}

	// Retried across the window where a restarting supervisor has no socket. A
	// cycle that runs every ten minutes and gave up on a millisecond of silence
	// would leave an agent stopped for ten minutes over a blip.
	revived, tried, err := w.app.tell(s, who, message)
	if err != nil {
		return working, unreached(who, tried, err)
	}
	if revived {
		w.app.note("%s was employed and not running, so it was started before being woken", who)
	}
	w.app.noteTries("waking", who, tried)
	w.woken[who.String()] = last
	if err := s.store.RecordWake(who, state.ID, last); err != nil {
		// The poke has already happened. The worst an unrecorded wake costs is one
		// extra nudge next pass, which is much cheaper than failing the cycle.
		w.app.note("%s was woken, but the wake could not be recorded, so it may be woken again: %v", who, err)
	}

	return wokeIt, w.app.say(fmt.Sprintf("%s %s   %s", w.app.out.Good("woke"),
		w.app.out.Identity(who.String()),
		w.app.out.Muted(fmt.Sprintf("waiting %s · %s", round(quiet), quoteShort(message)))))
}

// messageFor is what to say to one agent, and where it came from.
//
// The flag first, because an operator who typed a message meant that one. Then the
// override chain from Instruct.md §4, which the store walks: the agent's own, else
// its role's, else the fleet's, else `continue`. Only one is ever sent — three
// stapled together is not a message, it is a mess arriving mid-turn.
func (w *waker) messageFor(s caller, who user.Name) (string, instruct.Kind, error) {
	if w.message != "" {
		return w.message, "", nil
	}

	var role model.Name
	if target, err := s.fleet.Identity(who); err == nil {
		role = target.Role()
	}

	text, from, err := s.store.WakeMessage(who, role)
	if err != nil {
		// A wake message that will not read must not stop the cycle: an agent
		// left silent because somebody saved a bad file is the failure this whole
		// command exists to prevent.
		w.app.note("%s: its wake message could not be read, so it was sent the default: %v", who, err)
		return WakeMessage, "", nil
	}
	return text, from, nil
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
//
// A session whose feed ends at its own SessionStart is the same case wearing a hook
// event, and the feed reports it as waiting for that reason — see view.waits. It is
// the state a restart leaves, so it is the common one, not the corner.
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
func (w *waker) report(woke, stuck, quiet, dead, total int) error {
	if total == 0 {
		return w.app.say(w.app.out.Muted("nothing is employed, so nothing can be silent"))
	}
	if woke == 0 && stuck == 0 && dead == 0 {
		return w.app.say(fmt.Sprintf("%s   %s", w.app.out.Good("all working"),
			w.app.out.Muted(fmt.Sprintf("%d session%s, none silent for %s",
				quiet, plural(quiet), round(w.quiet)))))
	}

	line := fmt.Sprintf("%s of %d", w.app.out.Good(fmt.Sprintf("woke %d", woke)), total)
	if stuck > 0 {
		line += fmt.Sprintf("   %s", w.app.out.Warn(fmt.Sprintf("%d still silent after a wake", stuck)))
	}
	// Not running is worse than silent and is said last, where a reader's eye
	// finishes: a woken fleet with one agent down is not a healthy fleet.
	if dead > 0 {
		what := "not running"
		if w.tend {
			what = "started"
		}
		line += fmt.Sprintf("   %s", w.app.out.Warn(fmt.Sprintf("%d %s", dead, what)))
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

	// Recorded while it runs, so an upgrade can see that this fleet is being woken
	// — and so this process picks up the new build when one arrives.
	dog := w.app.watching(watch.Wake, interval, selfArgs())
	defer dog.done()

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

		// Between passes, never during one: a pass that is half-way through poking
		// a fleet when the process image changes underneath it is worse than a pass
		// run by an old build.
		dog.renew()

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
