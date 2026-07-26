package cli

import (
	"fmt"
	"strings"

	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/model"
	"orc/orc/internal/session"
)

// `orc model` — what an identity thinks with.
//
// It lives in its own file rather than beside `employ` because the two answer
// different questions. `employ` is "start this", and the model is one of the things
// it needs to know to do that; this is "run this on something else", asked about an
// agent that is usually already working.
//
// Everything it touches is the same, though, and deliberately so: the same control
// check, the same budget arithmetic, the same journal. A second way to change load
// that skipped the budget would make the budget advisory.

// tune is `orc model <identity> [<model>] [--effort <e>] [--now]`.
//
// With no model it reports; with one it changes what the identity thinks with. The
// reporting form exists because "what is it on?" is asked far more often than the
// change is made, and `orc status <identity>` is a whole card to read for one line.
func (a App) tune(args []string) error {
	var effortName string
	var now bool
	rest, err := flagged(args, options{
		values:   map[string]*string{"--effort": &effortName},
		switches: map[string]*bool{"--now": &now},
	})
	if err != nil {
		return err
	}
	if len(rest) == 0 || len(rest) > 2 {
		return fault.Usage{Reason: "model takes an identity, and a model to change it to"}
	}

	s, err := a.begin()
	if err != nil {
		return err
	}
	if err := s.mayRunVerb("model"); err != nil {
		return err
	}

	who, err := user.Parse(rest[0])
	if err != nil {
		return err
	}
	target, err := s.fleet.Identity(who)
	if err != nil {
		return err
	}

	if len(rest) == 1 && strings.TrimSpace(effortName) == "" {
		return a.reportTuning(s, who, target)
	}
	return a.retune(s, who, target, rest, effortName, now)
}

// reportTuning answers `orc model <identity>`: what it is on, and what that costs.
//
// Reading is not directing, so it asks only that the caller can see the fleet — the
// same bar `status` sets. Refusing to say what an agent is running would make the
// fleet less legible to no end.
func (a App) reportTuning(s caller, who user.Name, target model.Identity) error {
	m, e := tuningOf(target)
	load := model.SessionLoad(m, e)

	line := fmt.Sprintf("%s is on %s/%s   load %s",
		a.out.Identity(who.String()), a.out.Value(m.String()), a.out.Value(e.Short()),
		a.out.Authority(fmt.Sprintf("%d", load)))
	if !target.Employed() {
		line += "   " + a.out.Muted("(not employed, so it costs nothing yet)")
	}
	return a.say(line)
}

// retune changes it.
func (a App) retune(s caller, who user.Name, target model.Identity, rest []string, effortName string, now bool) error {
	// Directing somebody else's agent is the boss's call, and raising the load of
	// your own would be granting yourself budget. Both are the same check, and it
	// is the one `employ` makes.
	if err := s.controls(who, "change the model of"); err != nil {
		return err
	}

	m, e := tuningOf(target)
	if len(rest) == 2 {
		got, err := model.ParseModel(rest[1])
		if err != nil {
			return err
		}
		m = got
	}
	if strings.TrimSpace(effortName) != "" {
		got, err := model.ParseEffort(effortName)
		if err != nil {
			return err
		}
		e = got
	}

	if m == target.Model() && e == target.Effort() {
		// Not an error: a script that sets the model every pass should be a no-op
		// on the passes where nothing changed, not a failure.
		return a.say(fmt.Sprintf("%s is already on %s/%s",
			a.out.Identity(who.String()), a.out.Value(m.String()), a.out.Value(e.Short())))
	}

	// The budget, before anything is written. An identity that is not employed
	// costs nothing, so retuning it is free until somebody employs it — and that
	// employment is where the arithmetic is checked again.
	load := model.SessionLoad(m, e)
	if target.Employed() {
		if err := s.affordable(who, target, load); err != nil {
			return err
		}
	}

	if _, err := s.store.ApplyIdentity(who, func(model.Identity) (model.IdentityEvent, error) {
		return model.Retune(s.who, s.store.Now(), m, e)
	}); err != nil {
		return err
	}

	if err := a.sayRetuned(s, who, target, m, e, load); err != nil {
		return err
	}
	return a.applyTuning(s, who, target, m, e, now)
}

// sayRetuned reports the change, and what it did to the fleet's spending.
func (a App) sayRetuned(s caller, who user.Name, before model.Identity, m model.Model, e model.Effort, load int) error {
	was, wasEffort := tuningOf(before)

	line := fmt.Sprintf("%s %s   %s → %s   load %s → %s",
		a.out.Good("retuned"), a.out.Identity(who.String()),
		a.out.Muted(was.String()+"/"+wasEffort.Short()),
		a.out.Value(m.String()+"/"+e.Short()),
		a.out.Muted(fmt.Sprintf("%d", model.SessionLoad(was, wasEffort))),
		a.out.Authority(fmt.Sprintf("%d", load)))

	// The fleet total only means anything for something that is actually running.
	if before.Employed() {
		after, err := s.store.Fleet()
		if err != nil {
			return err
		}
		total, loads := after.Load(s.who)
		if budget, held := after.Budget(s.who); held {
			line += fmt.Sprintf("   fleet %d of %d over %d session%s",
				total, budget, len(loads), plural(len(loads)))
		}
	}
	return a.say(line)
}

// applyTuning makes the change reach the session, or says why it has not.
//
// A model is fixed when Claude starts, so a running session keeps the one it was
// launched with until it is replaced — and replacing it costs the conversation.
// That is not a decision to make on somebody's behalf inside a command they ran to
// change a setting, so the default is to say so and leave the session alone. --now
// is how they ask for it, and the message names the cost either way.
func (a App) applyTuning(s caller, who user.Name, target model.Identity, m model.Model, e model.Effort, now bool) error {
	if !target.Employed() {
		return a.say("  " + a.out.Muted(fmt.Sprintf(
			"it is not employed; `orc employ %s` will start it on this", who)))
	}

	_, live, err := s.store.Session(who)
	if err != nil {
		return err
	}
	if !live {
		return a.say("  " + a.out.Muted("no session is running; the next one starts on this"))
	}

	if !now {
		return a.say("  " + a.out.Muted(fmt.Sprintf(
			"the running session keeps %s until it is replaced — `orc model %s --now` "+
				"or `orc refresh %s` starts a fresh one, and loses its context",
			target.Model(), who, who)))
	}

	if err := a.depopulate(s.store, who); err != nil {
		return err
	}
	id, err := session.NewID()
	if err != nil {
		return err
	}
	if err := a.populate(s.store, who, id, m, e, false); err != nil {
		return err
	}
	return a.say("  " + a.out.Good("restarted") + " " +
		a.out.Muted(fmt.Sprintf("session %s, fresh context, on %s/%s", short(id), m, e.Short())))
}

// tuningOf reads an identity's model and effort, filling in the defaults an
// identity that has never been employed has not got.
func tuningOf(target model.Identity) (model.Model, model.Effort) {
	m, e := target.Model(), target.Effort()
	if !m.Valid() {
		m = model.DefaultModel
	}
	if !e.Valid() {
		e = model.DefaultEffort
	}
	return m, e
}
