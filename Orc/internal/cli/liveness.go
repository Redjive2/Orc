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
	"orc/orc/internal/authz"
	"orc/orc/internal/model"
	"orc/orc/internal/session"
)

// employ puts an identity on the worklist and populates it.
//
// The budget is the interesting part, and it is checked here rather than in a hook
// because a spawn is rare, deliberate, and already going through Orc — so it can be
// checked exactly and refused closed. Two things make the check unusual:
//
//   - it is a *hypothetical*: the count multiplier means the marginal cost of a
//     session is not its own load, so the answer comes from totalling the set with
//     the new session in it rather than from adding a number to a total;
//   - it is measured over the caller's whole subtree, so employing through a
//     subordinate is not a way around it.
func (a App) employ(args []string) error {
	var modelName, effortName string
	rest, err := flagged(args, options{values: map[string]*string{
		"--model": &modelName, "--effort": &effortName,
	}})
	if err != nil {
		return err
	}
	if err := exactly(rest, 1, "employ takes one identity"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	if err := s.mayRunVerb("employ"); err != nil {
		return err
	}

	who, err := user.Parse(rest[0])
	if err != nil {
		return err
	}
	if err := s.controls(who, "employ"); err != nil {
		return err
	}

	target, err := s.fleet.Identity(who)
	if err != nil {
		return err
	}
	if target.Role().Zero() {
		return fault.Conflict{Path: who.String(), Reason: fmt.Sprintf(
			"%s has no role, so a session would be able to do nothing; `orc assign role %s <role>` first",
			who, who)}
	}

	// What it was employed at last time, then the role's default, then the flags.
	// A `fire` and a re-`employ` should not quietly downgrade an agent somebody
	// deliberately put on opus.
	m, e := target.Model(), target.Effort()
	if !m.Valid() {
		m = model.DefaultModel
	}
	if !e.Valid() {
		e = model.DefaultEffort
	}
	if strings.TrimSpace(modelName) != "" {
		if m, err = model.ParseModel(modelName); err != nil {
			return err
		}
	}
	if strings.TrimSpace(effortName) != "" {
		if e, err = model.ParseEffort(effortName); err != nil {
			return err
		}
	}
	load := model.SessionLoad(m, e)

	if target.Employed() {
		// Already on the worklist. Re-employing at a different load is a change; at
		// the same load it is a no-op somebody may be running from a script.
		if target.Model() == m && target.Effort() == e {
			if err := a.say(fmt.Sprintf("%s is already employed at %s/%s",
				a.out.Identity(who.String()), a.out.Value(m.String()), a.out.Value(e.Short()))); err != nil {
				return err
			}
			return a.tendOne(s, who)
		}
	}

	if err := s.affordable(who, target, load); err != nil {
		return err
	}

	if _, err := s.store.ApplyIdentity(who, func(model.Identity) (model.IdentityEvent, error) {
		return model.Employ(s.who, s.store.Now(), m, e)
	}); err != nil {
		return err
	}

	after, err := s.store.Fleet()
	if err != nil {
		return err
	}
	total, loads := after.Load(s.who)
	budget, held := after.Budget(s.who)
	line := fmt.Sprintf("%s %s at %s/%s   load %s",
		a.out.Good("employed"), a.out.Identity(who.String()),
		a.out.Value(m.String()), a.out.Value(e.Short()), a.out.Authority(fmt.Sprintf("%d", load)))
	if held {
		line += fmt.Sprintf("   fleet %d of %d over %d session%s",
			total, budget, len(loads), plural(len(loads)))
	}
	if err := a.say(line); err != nil {
		return err
	}
	return a.tendOne(s, who)
}

// affordable refuses an employment the caller's budget does not cover, and explains
// which half of the arithmetic refused it.
//
// The two cases read very differently to somebody who has just been told no: either
// the new session is too big, or the *count* went up and made everything cost more.
// The second looks like a bug unless the message says so.
func (s caller) affordable(who user.Name, target model.Identity, load int) error {
	// Re-employing something already on the worklist at the same or lower load is
	// not new spending, so it is not budgeted again.
	if target.Employed() && load <= target.Load() {
		return nil
	}

	before, after, budget, ok := s.fleet.Afford(s.who, load)
	if ok {
		return nil
	}
	if budget == 0 {
		return fault.Denied{Actor: s.who.String(), Action: "employ", Target: who.String(),
			Reason: "it holds no spawn permission, so it may not add thinking to the worklist"}
	}

	_, loads := s.fleet.Load(s.who)
	detail := fmt.Sprintf("load %d → %d of %d", before, after, budget)
	if load < after-before {
		detail += fmt.Sprintf(": the count multiplier rose from %s to %s",
			authz.Multiplier(len(loads)), authz.Multiplier(len(loads)+1))
	}
	return fault.Denied{Actor: s.who.String(), Action: "employ", Target: who.String(), Reason: detail}
}

// fire takes an identity off the worklist and depopulates it.
func (a App) fire(args []string) error {
	var yes bool
	rest, err := flagged(args, options{switches: map[string]*bool{"--yes": &yes}})
	if err != nil {
		return err
	}
	if err := exactly(rest, 1, "fire takes one identity"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	if err := s.mayRunVerb("fire"); err != nil {
		return err
	}

	who, err := user.Parse(rest[0])
	if err != nil {
		return err
	}
	if err := s.controls(who, "fire"); err != nil {
		return err
	}

	// Ending a session mid-turn loses the turn, so the confirmation is required
	// exactly when there is something live to lose.
	_, live, err := s.store.Session(who)
	if err != nil {
		return err
	}
	if live && !yes {
		return fault.Usage{Reason: fmt.Sprintf(
			"%s has a live session, and firing ends it mid-turn.\n"+
				"  its memories, mail, and tasks are kept; pass --yes to go ahead", who)}
	}

	if _, err := s.store.ApplyIdentity(who, func(current model.Identity) (model.IdentityEvent, error) {
		if !current.Employed() {
			return model.IdentityEvent{}, nil
		}
		return model.Fire(s.who, s.store.Now())
	}); err != nil {
		return err
	}

	if live {
		if err := a.depopulate(s.store, who); err != nil {
			return err
		}
	}
	return a.say(fmt.Sprintf("%s %s   it keeps its memories, mail, and tasks",
		a.out.Warn("fired"), a.out.Identity(who.String())))
}

// tend reconciles the worklist with what is actually running.
//
// Every command calls it (§6.4), which is Macmuffin's `drain` idiom and works for
// the same reason: a fleet anybody is watching is a fleet somebody is running
// commands against. As a verb it exists so that reconciling is testable on its own,
// and so an operator can ask for it after fixing whatever broke.
func (a App) tend(args []string) error {
	var every string
	rest, err := flagged(args, options{values: map[string]*string{"--watch": &every}})
	if err != nil {
		return err
	}
	if err := exactly(rest, 0, "tend takes no arguments"); err != nil {
		return err
	}
	if every == "" {
		return a.tendOnce(true)
	}

	interval, err := time.ParseDuration(every)
	if err != nil {
		return fault.Usage{Reason: fmt.Sprintf("--watch takes a duration like 30s or 5m, not %q", every)}
	}
	if interval < MinWatch {
		// The rate is only meaningful for a positive interval, and "0s" and
		// "-5s" both reach here: dividing by either is the panic rule 4 forbids,
		// found by the test for this refusal rather than by a user.
		rate := ""
		if interval > 0 {
			rate = fmt.Sprintf(" and would reconcile %d times a minute", int(time.Minute/interval))
		}
		return fault.Usage{Reason: fmt.Sprintf(
			"--watch %s is below %s%s; %s is the shortest useful interval",
			every, MinWatch, rate, MinWatch)}
	}
	return a.tendLoop(interval)
}

// MinWatch is the shortest --watch interval.
//
// Reconciling is not free — it derives the fleet and stats every session — and a
// loop tighter than this is a busy-wait dressed as supervision. A crashed session
// is restarted on the next pass either way, so the only thing a shorter interval
// buys is a smaller window, at the cost of a machine that never idles.
const MinWatch = 5 * time.Second

// WatchGiveUp is how many consecutive failed passes end the loop.
//
// A pass that fails once is a session dying mid-read or a store being written to,
// and the next pass will see a settled world — that is the whole point of a
// backstop. A pass that fails this many times running is not a race, and a loop
// that cannot make progress should say so and stop rather than fill a terminal
// with the same line until somebody notices.
const WatchGiveUp = 5

// tendOnce reconciles the caller's subtree a single time.
func (a App) tendOnce(verbose bool) error {
	s, err := a.begin()
	if err != nil {
		return err
	}
	acted, err := a.reconcile(s, s.fleet.Subtree(s.who), verbose)
	if err != nil {
		return err
	}
	if acted == 0 && verbose {
		return a.say(a.out.Good("nothing to do") + "   the worklist and the sessions agree")
	}
	return nil
}

// watch reconciles on an interval until interrupted.
//
// It is the backstop for everything that reconciles a fleet by being asked to:
// every command calls tend (§6.4), so a fleet somebody is working in stays
// reconciled on its own — and a fleet nobody is touching does not. A session that
// dies at three in the morning is restarted by this loop or by nobody.
//
// The loop is deliberately quiet. It prints only when it acted or when a pass
// failed, because a supervisor that logs "nothing to do" every thirty seconds is
// one an operator stops reading, and a line nobody reads is the same as no line.
//
// A pass re-derives the fleet from the store rather than reusing the one it
// started with: the worklist is exactly the thing another operator changes while
// this is running, and reconciling against a stale snapshot would undo their
// employ on the next tick.
func (a App) tendLoop(interval time.Duration) error {
	if err := a.say(fmt.Sprintf("%s   %s",
		a.out.Good(fmt.Sprintf("tending every %s", interval)),
		a.out.Muted("interrupt to stop"))); err != nil {
		return err
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)

	// Recorded while it runs, so an upgrade can see this fleet has a backstop —
	// and so this process picks up the new build when one arrives.
	dog := a.watching(watch.Tend, interval, selfArgs())
	defer dog.done()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	failures := 0
	var last error
	for {
		if err := a.tendOnce(false); err != nil {
			failures++
			last = err
			// The failure is reported every time rather than once: two different
			// failures in a row are two different problems, and collapsing them
			// would hide the second.
			_, _ = fmt.Fprintf(a.Stderr, "%s %v\n", a.err.Alarm("orc:"), err)
			if failures >= WatchGiveUp {
				return fault.Conflict{Path: "tend --watch", Reason: fmt.Sprintf(
					"%d passes in a row failed; the last was: %v", failures, last)}
			}
		} else {
			failures = 0
		}

		// Between passes, never during one: reconciling a fleet is the last thing
		// that should be interrupted by the process image changing underneath it.
		dog.renew()

		select {
		case <-stop:
			// A stopped backstop is not a failure, and the fleet is exactly as
			// reconciled as the last pass left it.
			return a.say("\n" + a.out.Muted("stopped"))
		case <-ticker.C:
		}
	}
}

// tendOne reconciles a single identity, after employ or fire changed it.
func (a App) tendOne(s caller, who user.Name) error {
	_, err := a.reconcile(s, []user.Name{who}, false)
	return err
}

// reconcile is the loop: employed and unpopulated gets a session, populated and not
// employed loses one.
//
// It never kills a session for being over budget. Employing is where the budget is
// enforced, because that is the moment somebody asked for something; killing a
// running agent to balance a number would destroy work to satisfy a check that
// should have refused earlier.
func (a App) reconcile(s caller, names []user.Name, verbose bool) (acted int, err error) {
	for _, name := range names {
		// Read from the store rather than from the fleet the command started with.
		// The snapshot in `s.fleet` was derived before this command changed anything,
		// so `orc employ` reconciling against it would decide that the identity it
		// had just employed was not employed — which is exactly the bug the first
		// run of this found.
		who, err := s.store.Identity(name)
		if err != nil {
			return acted, err
		}
		_, live, err := s.store.Session(name)
		if err != nil {
			// A session file that will not parse is damage `orc verify` reports; it
			// must not stop the rest of the fleet being tended.
			a.note("%s: its session state could not be read: %v", name, err)
			continue
		}

		switch {
		case who.Employed() && !live:
			id, err := session.NewID()
			if err != nil {
				return acted, err
			}
			m, e := who.Model(), who.Effort()
			if !m.Valid() {
				m = model.DefaultModel
			}
			if !e.Valid() {
				e = model.DefaultEffort
			}
			if err := a.populate(s.store, name, id, m, e, false); err != nil {
				// One identity failing to populate is not a reason to stop tending the
				// others: a fleet with one broken agent should still come up.
				a.note("%s could not be populated: %v", name, err)
				acted++
				continue
			}
			acted++
			if err := a.say(fmt.Sprintf("%s %s at %s/%s   session %s",
				a.out.Good("populated"), a.out.Identity(name.String()),
				a.out.Value(m.String()), a.out.Value(e.Short()), a.out.Muted(short(id)))); err != nil {
				return acted, err
			}

		case !who.Employed() && live:
			if err := a.depopulate(s.store, name); err != nil {
				a.note("%s could not be depopulated: %v", name, err)
				acted++
				continue
			}
			acted++
			if err := a.say(fmt.Sprintf("%s %s   it is not on the worklist",
				a.out.Warn("depopulated"), a.out.Identity(name.String()))); err != nil {
				return acted, err
			}
		}
	}

	// Over budget is reported rather than corrected, every time, so a fleet that
	// grew past its budget by some other route does not stay quiet about it.
	if verbose {
		total, loads := s.fleet.Load(s.who)
		if budget, held := s.fleet.Budget(s.who); held && total > budget {
			a.note("the worklist is %d over budget (%d of %d across %d sessions); employing more will refuse",
				total-budget, total, budget, len(loads))
		}
	}
	return acted, nil
}

// refresh replaces a session: same identity, new conversation.
//
// The distinction from a crash restart is exact and is the reason both exist. A
// crash resumes the same session id, because nobody asked for a new agent — the
// process died. A refresh mints a new id, because somebody did.
func (a App) refresh(args []string) error {
	if err := exactly(args, 1, "refresh takes one identity"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	if err := s.mayRunVerb("refresh"); err != nil {
		return err
	}

	who, err := user.Parse(args[0])
	if err != nil {
		return err
	}
	if who.String() != s.who.String() {
		if err := s.controls(who, "refresh"); err != nil {
			return err
		}
	}

	target, err := s.fleet.Identity(who)
	if err != nil {
		return err
	}
	if !target.Employed() {
		return fault.Conflict{Path: who.String(), Reason: fmt.Sprintf(
			"%s is not employed, so there is no session to replace; `orc employ %s` starts one", who, who)}
	}

	if _, live, err := s.store.Session(who); err != nil {
		return err
	} else if live {
		if err := a.depopulate(s.store, who); err != nil {
			return err
		}
	}

	id, err := session.NewID()
	if err != nil {
		return err
	}
	m, e := target.Model(), target.Effort()
	if !m.Valid() {
		m = model.DefaultModel
	}
	if !e.Valid() {
		e = model.DefaultEffort
	}
	if err := a.populate(s.store, who, id, m, e, false); err != nil {
		return err
	}

	if err := a.say(fmt.Sprintf("%s %s   session %s, fresh context",
		a.out.Good("refreshed"), a.out.Identity(who.String()), a.out.Muted(short(id)))); err != nil {
		return err
	}
	// Session-scoped grants were tied to the session that just ended. Saying so is
	// what makes "temporarily" mean something an operator can see.
	lapsed := 0
	for _, g := range target.Grants() {
		if g.Session() != "" {
			lapsed++
		}
	}
	if lapsed > 0 {
		a.note("%d session-scoped grant%s lapsed with the old session", lapsed, plural(lapsed))
	}
	return nil
}

// poke types a message into a session without attaching.
func (a App) poke(args []string) error {
	if len(args) == 0 {
		return fault.Usage{Reason: "poke takes an identity, and optionally a message"}
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	if err := s.mayRunVerb("poke"); err != nil {
		return err
	}

	who, err := user.Parse(args[0])
	if err != nil {
		return err
	}
	if who.String() != s.who.String() {
		if err := s.controls(who, "poke"); err != nil {
			return err
		}
	}

	// The default is the whole point of the verb: "nudge the identity to continue
	// working".
	message := strings.TrimSpace(strings.Join(args[1:], " "))
	if message == "" {
		message = "continue"
	}
	// Checked here as well as at the pty. The supervisor refuses it either way, but
	// a refusal that arrives after the dial is one the operator reads as "the
	// session is broken" rather than as "that message cannot be typed".
	if err := session.Typeable(message); err != nil {
		return err
	}

	client, err := a.dial(s, who)
	if err != nil {
		return err
	}
	if err := client.Poke(message); err != nil {
		return err
	}
	return a.say(fmt.Sprintf("%s %s   %s", a.out.Good("poked"),
		a.out.Identity(who.String()), a.out.Muted(quoteShort(message))))
}

// dial finds a session's socket, and says what to do when there is none.
func (a App) dial(s caller, who user.Name) (*session.Client, error) {
	state, live, err := s.store.Session(who)
	if err != nil {
		return nil, err
	}
	if !live {
		target, err := s.fleet.Identity(who)
		if err != nil {
			return nil, err
		}
		if target.Employed() {
			return nil, fault.Unavailable{Peer: who.String(), Err: fmt.Errorf(
				"it is employed but not running; `orc tend` starts it, and %s says why it stopped",
				s.store.SessionLogPath(who))}
		}
		return nil, fault.Unavailable{Peer: who.String(), Err: fmt.Errorf(
			"it has no session; `orc employ %s` starts one", who)}
	}
	return session.Dial(state.Socket)
}

// short trims a session id for a line that is about something else.
func short(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8] + "…"
}

// quoteShort renders a poked message for a confirmation line.
func quoteShort(s string) string {
	const max = 48
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return `"` + s + `"`
}
