package policy

import (
	"orc/common/user"
	"orc/macmuffin/internal/task"
)

// What the fleet's operator may do to a task nobody owns.
//
// Ownership is how a task gets a decision-maker: the owner scopes it, completes
// it, invites to it, and throws it away. A task in the pool has none, so those
// refuse with "claim it first" — which is the right answer to an agent, because
// taking the work is exactly how it acquires the say over it.
//
// It is the wrong answer to the person running the fleet. Retiring a stale task,
// correcting a scope somebody wrote badly, or handing one to an agent are all
// things the operator does *without* wanting the work, and making them claim it
// first would put their name on a task they are not doing and then need a second
// command to take it off again.
//
// So the operator ranks as owner on an unowned task, and on nothing else. Two
// limits keep that narrow, and both are load-bearing:
//
//   - **Only where nobody owns it.** A task with an owner is that owner's, and an
//     operator who wants it takes it the way anybody does, in the open, with the
//     journal saying so. This is not a master key.
//   - **Only what is visible.** A draft is private to its author (§1.3), and the
//     operator does not see other people's drafts — so an unowned draft is still
//     not found rather than open. Privacy that the person at the top can read
//     past is not privacy, and nothing in a fleet needs it less than a sketch
//     somebody has not published.
//
// Whether the caller *is* the operator is not decided here: it is a fact about
// the fleet, held by Orc, asked over in internal/control. This package stays pure
// and answers only "would that standing make a difference to this action".

// OperatorMay reports whether an action refused by Allows would be permitted for
// the fleet's operator.
//
// It is consulted only after Allows has already refused, so it answers the
// narrow question and never re-decides an ordinary one.
func OperatorMay(actor user.Name, t task.Task, action Action) bool {
	if !action.Valid() || actor.Zero() || t.Name().Zero() {
		return false
	}
	if !t.Visible(actor) {
		return false
	}
	if _, owned := t.Owner(); owned {
		return false
	}
	// `leave` is the one action an owner may not take, and standing in for the
	// owner cannot make it possible: there is nothing to leave.
	return !rules[action].notOwner
}
