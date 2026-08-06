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
	load := s.fleet.Price(m, e)

	if target.Employed() {
		// Already on the worklist. Re-employing at a different load is a change; at
		// the same load it is a no-op somebody may be running from a script.
		if target.Model() == m && target.Effort() == e {
			if err := a.say(fmt.Sprintf("%s is already employed at %s/%s",
				a.out.Identity(who.String()), a.out.Value(m.String()), a.out.Value(e.Short()))); err != nil {
				return err
			}
			return a.tryNow(s, who)
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
	return a.tryNow(s, who)
}

// tryNow reconciles one identity with the start backoff cleared.
//
// `tend` paces a session that keeps failing to start, which is right for a backstop
// nobody asked to run and wrong for `orc employ`: somebody has just said "start
// this", and answering with a silence that lasts until a timer they cannot see
// elapses is the tool ignoring an instruction. An explicit ask always gets an
// attempt, and if that attempt fails the pacing starts again from one.
func (a App) tryNow(s caller, who user.Name) error {
	if err := s.store.ClearFailedStarts(who); err != nil {
		a.note("%s: the start record could not be cleared: %v", who, err)
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
			s.fleet.Multiplier(len(loads)), s.fleet.Multiplier(len(loads)+1))
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

	// Taking somebody off the worklist ends the conversation deliberately. Forgetting
	// the ending stops `tend` resuming what was just fired, which would be a backstop
	// overruling the operator.
	if err := s.store.ForgetEnded(who); err != nil {
		return err
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
	// Nothing is going to start it now, so the record of what stopped it starting
	// describes a question nobody is asking. It would otherwise pace the first
	// attempt after a re-employ, against failures from before it was fired.
	if err := s.store.ClearFailedStarts(who); err != nil {
		a.note("%s: the start record could not be cleared: %v", who, err)
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

	// A cycle that stops is a fleet with no backstop, and nothing restarts one. See
	// backstop.go: a pass may fail as often as it likes, and only how often that is
	// said out loud changes.
	guard := &backstop{app: a, what: "tending"}
	quiet := false
	for {
		// Paused, and quiet about it after the first time. The loop keeps looping
		// rather than exiting: somebody who turns tending back on should not also
		// have to remember which terminal they turned it off in.
		if a.tendPaused() {
			if !quiet {
				a.note("tending is off (`orc pace tend --on` starts it again); still watching")
				quiet = true
			}
			select {
			case <-stop:
				return a.say("\n" + a.out.Muted("stopped"))
			case <-ticker.C:
			}
			continue
		}
		quiet = false

		guard.pass(func() error { return a.tendOnce(false) })

		// Between passes, never during one: reconciling a fleet is the last thing
		// that should be interrupted by the process image changing underneath it.
		dog.renew()

		// And the interval is re-read here, for the reason `orc wake --every`
		// re-reads its own: a backstop started days ago from a shell nobody has
		// open is exactly what a stored setting has to be able to reach.
		if next := a.tendInterval(interval); next != interval {
			a.note("tending every %s from now on", round(next))
			interval = next
			ticker.Reset(interval)
		}

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
		// Bring the measurement up to date while we are here. `tend` runs under
		// almost every command, which is what makes this continuous without a
		// daemon, and a transcript that has not grown costs a stat. It reports
		// nothing and fails nothing: a rollup is a measurement, and a fleet must
		// never go untended because a number could not be written.
		s.advanceActivity(name)
		_, live, err := s.store.Session(name)
		if err != nil {
			// A session file that will not parse is damage `orc verify` reports; it
			// must not stop the rest of the fleet being tended.
			a.note("%s: its session state could not be read: %v", name, err)
			continue
		}

		// Tending turned off for this agent, its role, or the fleet.
		//
		// Read through the layers, the way `orc wake` reads its own pause. It was
		// read at the fleet layer only, which made `orc pace tend <agent> --off` a
		// control that confirmed, drew itself as in force in `orc pace` and in cq's
		// browser, and did nothing: the backstop repopulated the agent on its very
		// next pass. A setting that reports success and has no effect is worse than
		// one that refuses, because the operator stops looking.
		//
		// Only the *starting* half is skipped. An agent nobody is tending should
		// stay down when it is down, and still come off the work list when it is
		// fired — depopulating is what `fire` asked for, and a pause on the backstop
		// is not a veto over an instruction somebody gave.
		if who.Employed() && !live && s.store.Pacing(name, roleOf(s, name)).TendOff.Off() {
			if verbose {
				a.note("%s is employed and not running, and its tending is off "+
					"(`orc pace tend %s --on` starts it again)", name, name)
			}
			continue
		}

		switch {
		case who.Employed() && !live:
			// A start that keeps failing is tried again on a widening interval
			// rather than on every command. See store/attempts.go: the point is to
			// keep trying forever without forking a doomed supervisor every time
			// somebody types `orc status`.
			if due, left, got := s.store.StartDue(name); !due {
				if verbose {
					a.note("%s has failed to start %d time%s; the next attempt is in %s (%s)",
						name, got.Failures, plural(got.Failures), round(left), got.Why)
				}
				// Counted as work, because it is: the worklist and the sessions do
				// *not* agree, and a pass that held off and then reported "nothing to
				// do" would be telling an operator the fleet was reconciled while an
				// agent sat there employed and stopped.
				acted++
				continue
			}
			m, e := who.Model(), who.Effort()
			if !m.Valid() {
				m = model.DefaultModel
			}
			if !e.Valid() {
				e = model.DefaultEffort
			}

			// Continue the conversation that was there, rather than replacing it.
			//
			// An agent does not usually stop because its conversation is broken. It
			// stops because something outside it went wrong for a while — a usage
			// limit reached mid-turn, a network that came and went, a machine that
			// slept — and by the time `tend` runs, that is over. Starting a fresh
			// session then throws away the work the agent was part-way through and
			// gives back something that has never heard of it, which is what "it did
			// not come back properly" means in practice.
			//
			// `orc refresh` and `orc fire` forget the ending, so a conversation
			// somebody deliberately ended is never resurrected by a backstop.
			id, resume := "", false
			if ended, ok := s.store.LastEnded(name); ok {
				id, resume = ended.Session, true
			} else {
				fresh, err := session.NewID()
				if err != nil {
					return acted, err
				}
				id = fresh
			}

			if err := a.populate(s.store, name, id, m, e, resume); err != nil {
				// One identity failing to populate is not a reason to stop tending the
				// others: a fleet with one broken agent should still come up.
				a.note("%s could not be populated: %v", name, err)
				if err := s.store.RecordFailedStart(name, err.Error()); err != nil {
					// The backoff is an optimisation over trying constantly, so a
					// record that will not write costs the pacing and not the retry.
					a.note("%s: the failed start could not be recorded: %v", name, err)
				}
				acted++
				continue
			}
			// It came up, so whatever was wrong is over and the next failure starts
			// counting from one again.
			if err := s.store.ClearFailedStarts(name); err != nil {
				a.note("%s: the start record could not be cleared: %v", name, err)
			}
			acted++
			what := "populated"
			if resume {
				what = "resumed"
			}
			if err := a.say(fmt.Sprintf("%s %s at %s/%s   session %s",
				a.out.Good(what), a.out.Identity(name.String()),
				a.out.Value(m.String()), a.out.Value(e.Short()), a.out.Muted(short(id)))); err != nil {
				return acted, err
			}
			// Speak to it.
			//
			// Two reasons, and they are different. A session that went mid-turn comes
			// back sitting on an unfinished thought: the model call it was inside
			// never returned, and nothing finishes it on its own. A session somebody
			// has just employed has the opposite problem — nothing is unfinished and
			// nothing has started, because a Claude session that has not been spoken
			// to has no turn in which to act on its instructions.
			// What a session needs said to it follows from the session, not from
			// who started it.
			//
			// This used to ask the caller: `employ` said "opening" and a backstop
			// said nothing, so a *fresh* session started by `tend` was never spoken
			// to. A Claude session with no user turn does nothing at all — it holds
			// its instructions and has no occasion to act on them — so an agent whose
			// session the backstop rebuilt sat at its prompt until the wake cycle
			// noticed it, a whole interval later, and an `orc refresh` left one that
			// looked started and never moved.
			//
			// The session answers it instead. A conversation that was resumed carries
			// on, and is nudged only where it stopped mid-turn. One that is new has
			// never heard anything and is told to begin, every time, whoever asked.
			if resume {
				if err := a.nudgeIfInterrupted(s, name); err != nil {
					a.note("%s was resumed but could not be nudged on: %v", name, err)
				}
			} else {
				if err := a.openWith(s, name); err != nil {
					a.note("%s was started but could not be told to begin: %v", name, err)
				}
				// The interruption record belongs to the session that ended, and this
				// is a new one that has now been spoken to. Left behind it would nudge
				// the next start for a turn nobody is in.
				if err := s.store.ForgetEnded(name); err != nil {
					a.note("%s: the ending could not be cleared: %v", name, err)
				}
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

// openWith says the first thing to a session that has just been started.
//
// A Claude session with no user turn does nothing. It has the fleet's, the role's,
// and the identity's standing instructions in its system prompt — `orc employ`
// passes them — and no occasion to act on any of them: it is at its prompt, waiting
// for somebody to speak, and the fleet's own backstop will not notice for however
// long the wake threshold is. An agent employed at midnight and found idle at nine
// is an agent nobody ever said anything to.
//
// What it says is the **wake message**: the identity's, else its role's, else the
// fleet's, else `continue`. The same override chain, deliberately — a fleet that has
// written what to say to an agent that is not doing anything has already written
// this, and a second setting to keep in step with the first would be a second thing
// to get wrong.
//
// A failure to deliver is reported and does not fail the command. The session is up,
// which is what was asked for; the wake cycle is the backstop behind this one, and a
// fleet that refused to employ an agent because it could not immediately say hello
// would be worse in every case than one that says so.
func (a App) openWith(s caller, name user.Name) error {
	message, _, err := s.store.WakeMessage(name, roleOf(s, name))
	if err != nil || strings.TrimSpace(message) == "" {
		message = WakeMessage
	}

	client, _, err := a.reachWithin(s, name, OpenTries)
	if err != nil {
		return err
	}
	if _, err := keepTryingUpTo(OpenTries, func() error { return client.Poke(message) }); err != nil {
		return err
	}
	return a.say("  " + a.out.Muted(fmt.Sprintf("and told to begin: %s", quoteShort(message))))
}

// nudgeIfInterrupted tells a recovered session to carry on, when the one it
// continues had stopped part-way through a turn.
//
// Only for that case. A session that ended *waiting* had finished what it was doing,
// and poking it would be Orc inventing work; one that ended mid-call was interrupted,
// and the nudge is the difference between an agent that comes back and one that comes
// back and sits there.
//
// The record is cleared either way, so the nudge happens once rather than on every
// pass of a watch loop.
func (a App) nudgeIfInterrupted(s caller, name user.Name) error {
	ended, ok := s.store.LastEnded(name)
	if !ok {
		return nil
	}
	if err := s.store.ForgetEnded(name); err != nil {
		return err
	}
	if !ended.MidTurn {
		return nil
	}

	// The session has only just been asked to start, so its socket is very likely
	// not there yet — this is the widest instance of that window in the tool, since
	// the poke follows the populate by milliseconds. Retried rather than abandoned,
	// and a failure is still worth saying and not worth failing the tend for: the
	// wake cycle is the backstop behind this one.
	client, _, err := a.reach(s, name)
	if err != nil {
		return err
	}
	message, _, err := s.store.WakeMessage(name, roleOf(s, name))
	if err != nil || strings.TrimSpace(message) == "" {
		message = WakeMessage
	}
	if _, err := keepTrying(func() error { return client.Poke(message) }); err != nil {
		return err
	}
	return a.say("  " + a.out.Muted(fmt.Sprintf(
		"it had stopped part-way through a turn, so it was told to carry on: %s", quoteShort(message))))
}

// roleOf is an identity's role, or the zero name when it cannot be read. The wake
// message falls back through the role's layer, and an unreadable role costs that
// layer rather than the whole message.
func roleOf(s caller, name user.Name) model.Name {
	if got, err := s.fleet.Identity(name); err == nil {
		return got.Role()
	}
	return model.Name{}
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

	// A refresh is somebody saying the old conversation is over. Forgetting the
	// ending stops the backstop resurrecting what they chose to end.
	if err := s.store.ForgetEnded(who); err != nil {
		return err
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
		// A refresh that could not start the replacement leaves the identity
		// employed with nothing running, which is `tend`'s to fix — so it is paced
		// like any other failed start rather than retried by every command.
		if err := s.store.RecordFailedStart(who, err.Error()); err != nil {
			a.note("%s: the failed start could not be recorded: %v", who, err)
		}
		return err
	}
	if err := s.store.ClearFailedStarts(who); err != nil {
		a.note("%s: the start record could not be cleared: %v", who, err)
	}

	if err := a.say(fmt.Sprintf("%s %s   session %s, fresh context",
		a.out.Good("refreshed"), a.out.Identity(who.String()), a.out.Muted(short(id)))); err != nil {
		return err
	}
	// And spoken to. A refresh exists to give an agent new instructions, and a
	// conversation with nothing in it is one where the agent has had no turn in
	// which to read them — so a refresh that only started a session would have
	// swapped a working agent for an idle one.
	if err := a.openWith(s, who); err != nil {
		a.note("%s was refreshed but could not be told to begin: %v", who, err)
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

	// Delivered rather than refused. A session that is coming up has state before it
	// has a socket, a restarting one has no socket for a moment, and an employed
	// identity that is not running is a fleet disagreeing with itself — none of
	// those are reasons to tell somebody to go and run a different command. See
	// deliver.go.
	started, tried, err := a.tell(s, who, message)
	if err != nil {
		return unreached(who, tried, err)
	}
	if started {
		a.note("%s was employed and not running, so it was started first", who)
	}
	a.noteTries("poking", who, tried)
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

// tendInterval is how often the backstop reconciles, from the fleet's stored pace.
//
// A store that cannot be read leaves the interval alone: a fleet that stopped being
// tended because a settings file was briefly unreadable would be a worse failure
// than one tending at yesterday's pace.
func (a App) tendInterval(given time.Duration) time.Duration {
	s, err := a.begin()
	if err != nil {
		return given
	}
	return atLeast(s.store.FleetPacing().TendWatch.Duration(given), MinWatch, given)
}

// atLeast keeps a stored interval above the floor its flag would have enforced.
//
// `orc pace` refuses anything under the floor on the way in, so this only ever
// fires on a value that reached the file another way — a hand edit, a restore from
// an older build, a half-written sync. It matters because the read path is what a
// running cycle obeys: a "1s" that slipped past the writer turns the backstop into
// a busy-wait that derives the whole fleet sixty times a minute, on the machine
// nobody is watching. Falling back rather than clamping, so the cycle keeps the
// pace it was actually given rather than silently adopting the floor.
func atLeast(got, floor, fallback time.Duration) time.Duration {
	if got < floor {
		return fallback
	}
	return got
}

// tendPaused reports whether the fleet has turned its backstop off.
//
// A store that cannot be read is *not* paused: a fleet that stopped being tended
// because a settings file was briefly unreadable would be the worst possible
// response to a half-written file.
func (a App) tendPaused() bool {
	s, err := a.begin()
	if err != nil {
		return false
	}
	return s.store.FleetPacing().TendOff.Off()
}
