package cli

import (
	"errors"
	"fmt"
	"time"

	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/session"
)

// Getting an agent into the state that was asked for, rather than trying once.
//
// `poke`, `wake`, and `tend` all end in the same two operations — reach a session's
// socket, and say something to it — and both of them fail for reasons that are
// **transient by construction**:
//
//   - A session that has just been started has a state file before it has a socket:
//     the supervisor writes the state, then listens. A poke in that window is
//     refused by a session that is coming up perfectly well.
//   - A supervisor restarting its child closes the socket while it does. That is a
//     crash being handled correctly, and it lasts milliseconds.
//   - A machine under load makes both windows wider, which is precisely when a
//     fleet is most likely to be poked by a cycle rather than by a person.
//
// One attempt against a moving target is not a check, it is a coin toss. So every
// path through here retries on the errors that mean "not yet" and refuses
// immediately on the ones that mean "no" — a message that cannot be typed is not
// going to become typeable, and an identity that has no session is not going to grow
// one by being asked twice.
//
// Nothing here loops for long. The point is to cross a gap measured in milliseconds,
// not to wait out a broken fleet: a cycle that hung on one wedged agent would stop
// tending the others, which is a worse failure than the one it was covering.

// Delivery bounds a retry.
const (
	// DeliverTries is how many times a transient failure is retried.
	//
	// Six, doubling from 150ms, spans about nine seconds. That is not a number
	// picked for feel: a supervisor restarting a crashed child answers "the session
	// is restarting" and waits 1s, then 2s, then 4s before its next attempt
	// (session.FirstBackoff, doubling), so a delivery that gave up inside two
	// seconds would fail on precisely the case it exists for — an agent that is
	// coming back on its own, right now.
	DeliverTries = 6
	// DeliverBackoff is the first wait; each retry doubles it.
	DeliverBackoff = 150 * time.Millisecond
	// OpenTries bounds the *dial* an opening message makes, which is a different
	// problem from delivering one and gets a different budget.
	//
	// Populate has already waited for the session to exist; all that can still be
	// behind is the listener, which the supervisor opens immediately after writing
	// the state. Half a second covers that. Waiting out a supervisor's restart
	// backoff here would be waiting for a session that has not crashed — and it
	// would put nine seconds on the end of every `orc employ` that could not be
	// spoken to, which is precisely the case where the operator wants to be told
	// quickly rather than kept waiting.
	//
	// What this must **not** bound is the message itself. The paragraph above used
	// to cover both, on the reasoning that a lost opening message costs little
	// because the wake cycle is behind it — and that was the wrong trade twice
	// over. A session nobody has spoken to sits at its prompt until the quiet
	// threshold elapses, which is the gap somebody notices as "it started and never
	// did anything"; and the supervisor now confirms delivery, so the wait behind
	// one attempt is the confirmation ladder rather than a socket that is not there
	// yet. Three attempts at *that* is three chances to walk past a message the
	// ladder was about to rescue.
	OpenTries = 3
)

// transient reports whether an error is worth another attempt.
//
// Unavailable is the whole set: it is what `session.Dial` and every client call
// return when the socket is not there, not listening, or went away mid-write. Every
// other fault in the tree is a decision — denied, not found, usage, conflict — and
// retrying a decision is how a tool turns a clear refusal into a hang.
func transient(err error) bool {
	if err == nil {
		return false
	}
	var unavailable fault.Unavailable
	return errors.As(err, &unavailable) || errors.Is(err, fault.ErrUnavailable)
}

// keepTrying runs an operation until it succeeds, fails for a reason retrying
// cannot fix, or runs out of attempts.
//
// It returns the last error, so a caller reports why it gave up rather than that it
// gave up. `tried` is how many attempts it took, which is what a caller says out
// loud when the answer was not immediate: "poked, on the third try" is a fleet
// somebody should look at, and hiding it would make a machine that is limping look
// like one that is fine.
func keepTrying(op func() error) (tried int, err error) { return keepTryingUpTo(DeliverTries, op) }

// keepTryingUpTo is keepTrying with the number of attempts named, for the callers
// whose "not yet" is a different length of not-yet.
func keepTryingUpTo(tries int, op func() error) (tried int, err error) {
	if tries < 1 {
		tries = 1
	}
	wait := DeliverBackoff
	for tried = 1; tried <= tries; tried++ {
		err = op()
		if err == nil || !transient(err) {
			return tried, err
		}
		if tried < tries {
			time.Sleep(wait)
			wait *= 2
		}
	}
	return tries, err
}

// reach opens a session's socket, waiting out the window where a session that is
// coming up has state but no listener yet.
func (a App) reach(s caller, who user.Name) (*session.Client, int, error) {
	return a.reachWithin(s, who, DeliverTries)
}

// reachWithin is reach with the attempts named.
func (a App) reachWithin(s caller, who user.Name, tries int) (*session.Client, int, error) {
	var client *session.Client
	tried, err := keepTryingUpTo(tries, func() error {
		got, err := a.dial(s, who)
		if err != nil {
			return err
		}
		client = got
		return nil
	})
	return client, tried, err
}

// tell delivers a message to a session, and makes the session exist first when the
// fleet says it should.
//
// This is the whole of what `poke` and `wake` do at the end, and the ordering is the
// point. An identity that is *employed* and not running is a fleet that disagrees
// with itself, and the disagreement is exactly what `tend` fixes — so the message
// is not refused with advice to run another command. It is delivered, after making
// the state true, which is what somebody asking for it wanted.
//
// An identity that is **not employed** is refused, unchanged. Starting a session
// nobody employed spends budget on a decision the caller did not make, and doing
// that inside a poke would make the quietest verb in the tool the one that grows the
// fleet.
// The count it returns is **retries**, not attempts.
//
// Delivery is two steps — reach the session, then speak to it — and each reports the
// attempts it made, counting from one. Adding those together made a poke that worked
// perfectly report two, which tripped the "slow to answer" note on every single
// message a fleet ever sent. A warning that fires every time is one nobody reads,
// and one that tells an operator their healthy fleet is struggling is worse than no
// warning at all: it is what makes a working thing feel unreliable.
//
// So each step's first attempt is subtracted, and what is left is the thing the
// caller actually wants to know — how much harder than it should have been.
func (a App) tell(s caller, who user.Name, message string) (started bool, retries int, err error) {
	client, tried, err := a.reach(s, who)
	retries = tried - 1
	if err != nil {
		if !revivable(s, who) {
			return false, retries, err
		}
		// Employed and not running: make it so, then say the thing.
		if err := a.tendOne(s, who); err != nil {
			return false, retries, err
		}
		started = true
		var again int
		client, again, err = a.reach(s, who)
		retries += again - 1
		if err != nil {
			return started, retries, err
		}
	}

	sent, err := keepTrying(func() error { return client.Poke(message) })
	return started, retries + sent - 1, err
}

// revivable reports whether a session that is not running is one the fleet says
// ought to be.
func revivable(s caller, who user.Name) bool {
	// From the store rather than the derived snapshot: an `employ` in the same
	// command has already written the journal, and the snapshot predates it.
	got, err := s.store.Identity(who)
	if err != nil {
		return false
	}
	return got.Employed()
}

// noteTries says how hard something was, when it was not easy.
//
// Silent on the first try, which is the overwhelming majority: a line saying "on
// the first attempt" every time is one nobody reads, and the whole value of this is
// that the exceptions stand out.
func (a App) noteTries(what string, who user.Name, retries int) {
	if retries < 1 {
		return
	}
	a.note("%s %s needed %d retr%s; the session was slow to answer", what, who,
		retries, map[bool]string{true: "y", false: "ies"}[retries == 1])
}

// unreached wraps a delivery failure with what was actually tried, so the operator
// is told the difference between "it refused" and "it never answered".
func unreached(who user.Name, tried int, err error) error {
	if tried <= 1 {
		return err
	}
	return fault.Unavailable{Peer: who.String(), Err: fmt.Errorf(
		"it did not answer in %d attempts over %s: %w", tried, roundTries(tried), err)}
}

// roundTries renders how long keepTrying spent, for that message.
func roundTries(tried int) time.Duration {
	total := time.Duration(0)
	wait := DeliverBackoff
	for i := 1; i < tried; i++ {
		total += wait
		wait *= 2
	}
	return total
}
