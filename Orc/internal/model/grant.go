package model

import (
	"fmt"
	"time"

	"orc/common/clock"
	"orc/common/fault"
)

// UnpopulatedGrant is how long a grant to an identity with no session lasts.
//
// A session-scoped grant needs a session to be scoped to. An identity that is
// not populated has none, so the grant takes a wall-clock expiry instead — one
// hour, long enough to be useful and short enough that forgetting about it costs
// nothing. The alternative was to let such a grant mean "forever", and a grant
// that never lapses is not the thing Reference.md calls temporary.
const UnpopulatedGrant = time.Hour

// MaxGrantSpan bounds `--until`. A grant longer than a week is a role.
const MaxGrantSpan = 7 * 24 * time.Hour

// Grant is a permission handed directly to an identity, bypassing its role, and
// always with an expiry.
//
// There are exactly two ways for one to end on its own, and a third by hand:
//
//   - tied to a session, it lapses when that session ends — a refresh or a
//     depopulate is a clean slate in permissions as well as in context;
//   - given `--until`, it lapses at a wall-clock instant, which survives a
//     refresh;
//   - `orc revoke permission` ends either one immediately.
//
// There is deliberately no third *shape*. A grant with no expiry is one nobody
// remembers making, and the word in the specification is "temporarily".
type Grant struct {
	permission Name
	by         string // the actor who granted it, as text; empty in a revoke stub
	granted    time.Time

	// session is the session id the grant is tied to, or empty for a wall-clock
	// grant. until is the wall-clock deadline, or zero for a session grant.
	// Exactly one of the two is set, which validate enforces.
	session string
	until   time.Time
}

// SessionGrant builds a grant tied to a session.
func SessionGrant(permission Name, by string, at time.Time, session string) (Grant, error) {
	if session == "" {
		return Grant{}, fault.Internal{Where: "model.SessionGrant", Detail: "no session id given"}
	}
	g := Grant{permission: permission, by: by, granted: clock.Normalise(at), session: session}
	if err := g.validate(); err != nil {
		return Grant{}, err
	}
	return g, nil
}

// TimedGrant builds a grant that lapses at a wall-clock instant.
func TimedGrant(permission Name, by string, at time.Time, span time.Duration) (Grant, error) {
	if span <= 0 {
		return Grant{}, fault.Usage{Reason: "a grant's duration must be positive"}
	}
	if span > MaxGrantSpan {
		return Grant{}, fault.Usage{Reason: fmt.Sprintf(
			"a grant may last at most %s; longer than that is a role, not a grant", MaxGrantSpan)}
	}
	g := Grant{permission: permission, by: by, granted: clock.Normalise(at),
		until: clock.Normalise(at.Add(span))}
	if err := g.validate(); err != nil {
		return Grant{}, err
	}
	return g, nil
}

// RestoreGrant rebuilds a grant from stored fields. It is the journal codec's
// door, and it runs the same validation as the constructors so a hand-edited
// journal cannot introduce a grant shape the code never produces.
func RestoreGrant(permission Name, by string, granted time.Time, session string, until time.Time) (Grant, error) {
	g := Grant{
		permission: permission,
		by:         by,
		granted:    clock.Normalise(granted),
		session:    session,
	}
	if !until.IsZero() {
		g.until = clock.Normalise(until)
	}
	if err := g.validate(); err != nil {
		return Grant{}, err
	}
	return g, nil
}

func (g Grant) validate() error {
	const where = "model.Grant"
	if err := fault.Check(!g.permission.Zero(), where, "grant names no permission"); err != nil {
		return err
	}
	if err := fault.Check(!g.granted.IsZero(), where, "grant of %s has no timestamp", g.permission); err != nil {
		return err
	}
	// Exactly one expiry. Both would mean two answers to "when does this
	// lapse?"; neither would mean forever, which this type does not offer.
	hasSession, hasUntil := g.session != "", !g.until.IsZero()
	if err := fault.Check(hasSession != hasUntil, where,
		"grant of %s must be tied to either a session or a deadline, not both or neither", g.permission); err != nil {
		return err
	}
	return nil
}

// Permission returns what was granted.
func (g Grant) Permission() Name { return g.permission }

// By returns who granted it.
func (g Grant) By() string { return g.by }

// Granted returns when it was given.
func (g Grant) Granted() time.Time { return g.granted }

// Session returns the session the grant is tied to, or empty for a timed grant.
func (g Grant) Session() string { return g.session }

// Until returns the wall-clock deadline, or the zero time for a session grant.
func (g Grant) Until() time.Time { return g.until }

// Zero reports whether the grant was never constructed.
func (g Grant) Zero() bool { return g.permission.Zero() }

// Live reports whether the grant is still in force.
//
// session is the identity's *current* session, or empty when it is not
// populated. A session-scoped grant whose session is no longer the current one
// has lapsed — which covers both a refresh, where the id changed, and a
// depopulate, where there is no id at all. That is the whole mechanism: nothing
// has to run to expire a grant, because expiry is a question rather than a job.
func (g Grant) Live(now time.Time, session string) bool {
	switch {
	case g.Zero():
		return false
	case g.session != "":
		return session != "" && g.session == session
	default:
		return clock.Normalise(now).Before(g.until)
	}
}

// Lapse describes when a grant ends, for a card. It is text rather than a
// duration because the two kinds end for different reasons and a column of
// durations would hide which is which.
func (g Grant) Lapse(now time.Time) string {
	switch {
	case g.Zero():
		return "—"
	case g.session != "":
		return "at session end"
	default:
		left := g.until.Sub(clock.Normalise(now))
		if left <= 0 {
			return "lapsed"
		}
		return showSpan(left) + " left"
	}
}

// showSpan renders a duration the way `orc status` shows one: the largest unit
// that leaves a number under a hundred, so a column of them stays narrow.
//
// It rounds **up**, which matters more than it looks. A grant made for thirty
// minutes is read back a few microseconds later, and truncating would show it as
// "29m left" — a number that is not wrong by much and is wrong in the direction
// that makes an operator think the tool mis-set their grant.
func showSpan(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	ceil := func(unit time.Duration) int {
		n := int(d / unit)
		if d%unit != 0 {
			n++
		}
		return n
	}

	// The unit is chosen from the value *after* rounding, not before. Rounding up
	// can carry a value across a boundary — 59m59s becomes sixty minutes — and
	// choosing the unit first would print that as "60m" while an exact hour printed
	// "1h", which is one column showing the same duration two ways.
	if secs := ceil(time.Second); secs < 60 {
		return itoa(secs) + "s"
	}
	if mins := ceil(time.Minute); mins < 60 {
		return itoa(mins) + "m"
	}
	if hours := ceil(time.Hour); hours < 24 {
		return itoa(hours) + "h"
	}
	return itoa(ceil(24*time.Hour)) + "d"
}
