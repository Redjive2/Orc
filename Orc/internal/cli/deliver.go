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

// Delivery bounds a retry. The values are small on purpose — see above.
const (
	// DeliverTries is how many times a transient failure is retried.
	DeliverTries = 4
	// DeliverBackoff is the first wait; each retry doubles it, so four tries span
	// about a second and a half.
	DeliverBackoff = 150 * time.Millisecond
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
func keepTrying(op func() error) (tried int, err error) {
	wait := DeliverBackoff
	for tried = 1; tried <= DeliverTries; tried++ {
		err = op()
		if err == nil || !transient(err) {
			return tried, err
		}
		if tried < DeliverTries {
			time.Sleep(wait)
			wait *= 2
		}
	}
	return DeliverTries, err
}

// reach opens a session's socket, waiting out the window where a session that is
// coming up has state but no listener yet.
func (a App) reach(s caller, who user.Name) (*session.Client, int, error) {
	var client *session.Client
	tried, err := keepTrying(func() error {
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
func (a App) tell(s caller, who user.Name, message string) (started bool, tried int, err error) {
	client, tried, err := a.reach(s, who)
	if err != nil {
		if !revivable(s, who) {
			return false, tried, err
		}
		// Employed and not running: make it so, then say the thing.
		if err := a.tendOne(s, who); err != nil {
			return false, tried, err
		}
		started = true
		var again int
		client, again, err = a.reach(s, who)
		tried += again
		if err != nil {
			return started, tried, err
		}
	}

	sent, err := keepTrying(func() error { return client.Poke(message) })
	return started, tried + sent, err
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
func (a App) noteTries(what string, who user.Name, tried int) {
	if tried <= 1 {
		return
	}
	a.note("%s %s took %d attempts; the session was slow to answer", what, who, tried)
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
