package policy_test

import (
	"testing"

	"orc/macmuffin/internal/policy"
)

// OperatorMay is the pure half of the operator's standing: not "is this caller
// the operator" — that is Orc's answer — but "would being the operator make any
// difference to this action on this task".
//
// It is total over the action table on purpose. A new action added to policy gets
// an answer here whether or not anybody remembered this file, and the answer is
// yes for every owner-shaped action, which is the intended default: the operator
// stands in for an owner that does not exist.

func TestTheOperatorStandsInOnlyWhereNobodyOwnsIt(t *testing.T) {
	pooled := build(t, scoped(t), pushed(t))
	owned := build(t, scoped(t), pushed(t), claimedBy(t, "bob"))

	for _, action := range policy.Actions() {
		if action == policy.Leave {
			continue // there is nothing to leave; the next test says so
		}
		if !policy.OperatorMay(agent(t, "boss"), pooled, action) {
			t.Errorf("the operator may not %s a task nobody owns", action)
		}
		if action == policy.Block || action == policy.Unblock {
			continue // ordering crosses owners; the next test says why
		}
		if policy.OperatorMay(agent(t, "boss"), owned, action) {
			t.Errorf("the operator may %s a task bob owns", action)
		}
	}
}

// Ordering is the one thing the operator may state over an owner's head, and it
// has to be: an order between two tasks under two owners is a thing neither
// owner can state. Each speaks only for their own task, so without this the
// most anybody can do is ask and hope — which holds exactly as long as nobody is
// in a hurry, and the first time it matters is the time it fails.
func TestTheOperatorMayOrderATaskSomebodyElseHolds(t *testing.T) {
	owned := build(t, scoped(t), pushed(t), claimedBy(t, "bob"))

	for _, action := range []policy.Action{policy.Block, policy.Unblock} {
		if !policy.OperatorMay(agent(t, "boss"), owned, action) {
			t.Errorf("the operator may not %s a task bob owns, so nobody can sequence two teams", action)
		}
	}
}

// And it stays an exception. Being able to say *when* a task runs must not leak
// into saying what it is, whose it is, or whether it is finished — that is the
// difference between sequencing a fleet and holding a master key.
func TestOrderingDoesNotWidenIntoTheRest(t *testing.T) {
	owned := build(t, scoped(t), pushed(t), claimedBy(t, "bob"))

	for _, action := range []policy.Action{
		policy.Complete, policy.Scope, policy.Delete, policy.Assign,
		policy.Invite, policy.Kick, policy.Push, policy.Worktree, policy.Describe,
	} {
		if policy.OperatorMay(agent(t, "boss"), owned, action) {
			t.Errorf("the operator may %s a task bob owns; ordering has widened into a master key", action)
		}
	}
}

// `leave` is the one action an owner may not take, so standing in for the owner
// cannot make it possible.
func TestStandingInDoesNotLetTheOperatorLeave(t *testing.T) {
	pooled := build(t, scoped(t), pushed(t))
	if policy.OperatorMay(agent(t, "boss"), pooled, policy.Leave) {
		t.Error("the operator may leave a task it is not on")
	}
}

// A draft is private to its author. Privacy that the person at the top can read
// past is not privacy, and an unowned draft is still not the operator's.
func TestStandingInDoesNotReachIntoADraft(t *testing.T) {
	draft := build(t, scoped(t))
	if policy.OperatorMay(agent(t, "boss"), draft, policy.Scope) {
		t.Error("the operator reached into somebody else's draft")
	}
	// alice is the author, and her own draft is hers.
	if !policy.OperatorMay(agent(t, "alice"), draft, policy.Scope) {
		t.Error("an author lost its own draft")
	}
}

func TestAnUnknownActionIsNotAllowed(t *testing.T) {
	pooled := build(t, scoped(t), pushed(t))
	if policy.OperatorMay(agent(t, "boss"), pooled, policy.Action(-1)) {
		t.Error("an action that does not exist was permitted")
	}
}
