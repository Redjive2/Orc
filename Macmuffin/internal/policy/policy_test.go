package policy_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"orc/common/fault"
	"orc/common/user"
	"orc/macmuffin/internal/policy"
	"orc/macmuffin/internal/task"
)

var epoch = time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

func name(t *testing.T, s string) task.Name {
	t.Helper()
	n, err := task.ParseName(s)
	if err != nil {
		t.Fatalf("ParseName(%q): %v", s, err)
	}
	return n
}

func agent(t *testing.T, s string) user.Name {
	t.Helper()
	n, err := user.Parse(s)
	if err != nil {
		t.Fatalf("user.Parse(%q): %v", s, err)
	}
	return n
}

// build makes a task in a chosen state. The author is always alice.
func build(t *testing.T, apply ...func(task.Task) task.Task) task.Task {
	t.Helper()
	p, err := task.NewPriority(3)
	if err != nil {
		t.Fatal(err)
	}
	d, err := task.NewDifficulty(3)
	if err != nil {
		t.Fatal(err)
	}
	got, err := task.NewDraft(name(t, "fix-the-parser"), agent(t, "alice"), p, d, epoch)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range apply {
		got = f(got)
	}
	return got
}

// with folds an event on, failing the test if it is refused.
func with(t *testing.T, make func() (task.Event, error)) func(task.Task) task.Task {
	t.Helper()
	return func(in task.Task) task.Task {
		t.Helper()
		ev, err := make()
		if err != nil {
			t.Fatalf("building the event: %v", err)
		}
		out, err := in.With(ev)
		if err != nil {
			t.Fatalf("applying %v: %v", ev.Op(), err)
		}
		return out
	}
}

func scoped(t *testing.T) func(task.Task) task.Task {
	return with(t, func() (task.Event, error) {
		return task.Scope(agent(t, "alice"), epoch, []string{"internal/tree/"})
	})
}

func pushed(t *testing.T) func(task.Task) task.Task {
	return with(t, func() (task.Event, error) { return task.Push(agent(t, "alice"), epoch) })
}

func claimedBy(t *testing.T, who string) func(task.Task) task.Task {
	return with(t, func() (task.Event, error) { return task.Claim(agent(t, who), epoch) })
}

func invited(t *testing.T, by, who string) func(task.Task) task.Task {
	return with(t, func() (task.Event, error) {
		return task.Invite(agent(t, by), epoch, agent(t, who))
	})
}

// TestThePermissionTableInFull enumerates every actor against every action, in
// every task state the plan distinguishes. It is exhaustive because the table
// is small and because a single wrong cell would let an agent edit files it
// should not have been able to reach.
func TestThePermissionTableInFull(t *testing.T) {
	// The states.
	unownedDraft := build(t)                        // alice's private sketch
	scopedDraft := build(t, scoped(t))              // still private, now scoped
	unownedPooled := build(t, scoped(t), pushed(t)) // in the pool, nobody's
	ownedByBob := build(t, scoped(t), pushed(t), claimedBy(t, "bob"))
	withCarol := build(t, scoped(t), pushed(t), claimedBy(t, "bob"), invited(t, "bob", "carol"))

	const (
		yes = true
		no  = false
	)

	for _, tc := range []struct {
		state  string
		task   task.Task
		actor  string
		action policy.Action
		want   bool
	}{
		// An unowned pooled task: readable by all, claimable by all, and every
		// owner-only action refused with "claim it first".
		{"unowned pooled", unownedPooled, "dave", policy.Info, yes},
		{"unowned pooled", unownedPooled, "dave", policy.Claim, yes},
		{"unowned pooled", unownedPooled, "alice", policy.Claim, yes},
		{"unowned pooled", unownedPooled, "dave", policy.Scope, no},
		{"unowned pooled", unownedPooled, "dave", policy.Push, no},
		{"unowned pooled", unownedPooled, "dave", policy.Invite, no},
		{"unowned pooled", unownedPooled, "dave", policy.Complete, no},
		{"unowned pooled", unownedPooled, "dave", policy.Delete, no},
		{"unowned pooled", unownedPooled, "dave", policy.Status, no},
		{"unowned pooled", unownedPooled, "dave", policy.SubAdd, no},
		{"unowned pooled", unownedPooled, "dave", policy.Leave, no},
		// The author keeps what the task *is* — the scope and the description —
		// whatever has happened to it since. Everything that decides who does the
		// work, or whether it is finished, has left their hands.
		{"unowned pooled", unownedPooled, "alice", policy.Scope, yes},
		{"unowned pooled", unownedPooled, "alice", policy.Describe, yes},
		{"unowned pooled", unownedPooled, "alice", policy.Delete, no},
		{"unowned pooled", unownedPooled, "alice", policy.Push, no},

		// A task bob owns.
		{"owned", ownedByBob, "bob", policy.Info, yes},
		{"owned", ownedByBob, "bob", policy.Scope, yes},
		{"owned", ownedByBob, "bob", policy.Invite, yes},
		{"owned", ownedByBob, "bob", policy.Kick, yes},
		{"owned", ownedByBob, "bob", policy.Complete, yes},
		{"owned", ownedByBob, "bob", policy.Delete, yes},
		{"owned", ownedByBob, "bob", policy.Worktree, yes},
		{"owned", ownedByBob, "bob", policy.Status, yes},
		{"owned", ownedByBob, "bob", policy.SubAdd, yes},
		{"owned", ownedByBob, "bob", policy.SubDone, yes},
		// The owner may not leave, and may not re-claim.
		{"owned", ownedByBob, "bob", policy.Leave, no},

		// A stranger sees it and can do nothing else.
		{"owned", ownedByBob, "dave", policy.Info, yes},
		{"owned", ownedByBob, "dave", policy.Claim, yes}, // refused later by the fold
		{"owned", ownedByBob, "dave", policy.Scope, no},
		{"owned", ownedByBob, "dave", policy.Status, no},
		{"owned", ownedByBob, "dave", policy.SubAdd, no},
		{"owned", ownedByBob, "dave", policy.Invite, no},
		{"owned", ownedByBob, "dave", policy.Delete, no},
		{"owned", ownedByBob, "dave", policy.Leave, no},
		// The author is the exception, and only for the specification. Whoever
		// wrote a task knows what it was for, and that does not transfer with the
		// claim: an author who spots a wrong line number in their own description
		// could otherwise only watch the work be done from it.
		{"owned", ownedByBob, "alice", policy.Scope, yes},
		{"owned", ownedByBob, "alice", policy.Describe, yes},
		// And nothing beyond it. Everything that decides who does the work, or
		// whether it is done, is bob's now.
		{"owned", ownedByBob, "alice", policy.Delete, no},
		{"owned", ownedByBob, "alice", policy.Push, no},
		{"owned", ownedByBob, "alice", policy.Complete, no},
		{"owned", ownedByBob, "alice", policy.Invite, no},
		{"owned", ownedByBob, "alice", policy.Kick, no},
		{"owned", ownedByBob, "alice", policy.Worktree, no},

		// A collaborator does the work but does not shape the task.
		{"with collaborator", withCarol, "carol", policy.Info, yes},
		{"with collaborator", withCarol, "carol", policy.Status, yes},
		{"with collaborator", withCarol, "carol", policy.SubAdd, yes},
		{"with collaborator", withCarol, "carol", policy.SubDone, yes},
		{"with collaborator", withCarol, "carol", policy.SubDelete, yes},
		{"with collaborator", withCarol, "carol", policy.Leave, yes},
		{"with collaborator", withCarol, "carol", policy.Scope, no},
		{"with collaborator", withCarol, "carol", policy.Invite, no},
		{"with collaborator", withCarol, "carol", policy.Kick, no},
		{"with collaborator", withCarol, "carol", policy.Complete, no},
		{"with collaborator", withCarol, "carol", policy.Delete, no},
		{"with collaborator", withCarol, "carol", policy.Push, no},
		{"with collaborator", withCarol, "carol", policy.Worktree, no},

		// A draft is its author's to shape and to throw away, without claiming.
		{"unowned draft", unownedDraft, "alice", policy.Info, yes},
		{"unowned draft", unownedDraft, "alice", policy.Scope, yes},
		{"unowned draft", unownedDraft, "alice", policy.Delete, yes},
		{"unowned draft", unownedDraft, "alice", policy.Claim, yes},
		{"scoped draft", scopedDraft, "alice", policy.Push, yes},
		// But not to invite to or complete: those need an owner.
		{"unowned draft", unownedDraft, "alice", policy.Invite, no},
		{"unowned draft", unownedDraft, "alice", policy.Complete, no},
		{"unowned draft", unownedDraft, "alice", policy.Status, no},
	} {
		t.Run(tc.state+"/"+tc.actor+"/"+tc.action.String(), func(t *testing.T) {
			err := policy.Allows(agent(t, tc.actor), tc.task, tc.action)
			if tc.want && err != nil {
				t.Errorf("Allows = %v, want it permitted", err)
			}
			if !tc.want && err == nil {
				t.Error("Allows permitted something it should not have")
			}
			if !tc.want && err != nil && !errors.Is(err, fault.ErrDenied) {
				t.Errorf("Allows = %v, want a denied fault", err)
			}
		})
	}
}

// TestADraftIsInvisibleToEveryoneElse — and reported as missing rather than
// forbidden, since "you may not look at that" discloses the very thing privacy
// is for.
func TestADraftIsInvisibleToEveryoneElse(t *testing.T) {
	draft := build(t, scoped(t))

	for _, action := range policy.Actions() {
		err := policy.Allows(agent(t, "bob"), draft, action)
		if !errors.Is(err, fault.ErrNotFound) {
			t.Errorf("%v on someone else's draft = %v, want not found", action, err)
		}
		if strings.Contains(err.Error(), "may not") {
			t.Errorf("%v disclosed that the draft exists: %v", action, err)
		}
	}

	if policy.Visible(agent(t, "bob"), draft) {
		t.Error("a draft should not be visible to a stranger")
	}
	if !policy.Visible(agent(t, "alice"), draft) {
		t.Error("an author should see their own draft")
	}
}

// TestUnownedRefusalsRedirect: an unowned task is refused for a different
// reason than someone else's, and the difference is the whole distance between
// a dead end and a next step.
func TestUnownedRefusalsRedirect(t *testing.T) {
	unowned := build(t, scoped(t), pushed(t))
	owned := build(t, scoped(t), pushed(t), claimedBy(t, "bob"))

	err := policy.Allows(agent(t, "dave"), unowned, policy.Scope)
	if !errors.Is(err, fault.ErrDenied) {
		t.Fatalf("Allows = %v, want a denied fault", err)
	}
	if !strings.Contains(err.Error(), "claim it first") {
		t.Errorf("an unowned refusal should point at claim: %v", err)
	}
	if !strings.Contains(err.Error(), "muff claim fix-the-parser") {
		t.Errorf("the refusal should be a command the caller can run: %v", err)
	}

	err = policy.Allows(agent(t, "dave"), owned, policy.Scope)
	if !errors.Is(err, fault.ErrDenied) {
		t.Fatalf("Allows = %v, want a denied fault", err)
	}
	if !strings.Contains(err.Error(), "bob") {
		t.Errorf("an owned refusal should name the owner: %v", err)
	}
	if strings.Contains(err.Error(), "claim it first") {
		t.Errorf("an owned task should not tell the caller to claim it: %v", err)
	}
}

// TestEveryDenialExitsEight: the code is what a hook or a script branches on.
func TestEveryDenialMapsToTheDeniedCode(t *testing.T) {
	owned := build(t, scoped(t), pushed(t), claimedBy(t, "bob"))

	for _, action := range policy.Actions() {
		err := policy.Allows(agent(t, "dave"), owned, action)
		if err == nil {
			continue
		}
		if got := fault.Code(err); got != fault.CodeDenied {
			t.Errorf("%v was refused with exit %d, want %d", action, got, fault.CodeDenied)
		}
	}
}

// TestEveryActionHasARule guards the table itself: an action added without a
// rule would take the zero rule, which permits everyone.
func TestEveryActionHasARule(t *testing.T) {
	owned := build(t, scoped(t), pushed(t), claimedBy(t, "bob"))

	for _, action := range policy.Actions() {
		if !action.Valid() {
			t.Errorf("Actions() returned the invalid %v", action)
		}
		if got := action.String(); got == "" || strings.HasPrefix(got, "Action(") {
			t.Errorf("%d has no verb", int(action))
		}
		// A stranger must be refused everything except reading, claiming, and
		// assigning — assign is claim on somebody else's behalf, so it asks the
		// table for exactly what claim asks. What stops a stranger assigning an
		// owned task is the event refusing the transition, and what stops them
		// directing an agent they do not control is Orc.
		err := policy.Allows(agent(t, "dave"), owned, action)
		switch action {
		case policy.Info, policy.Claim, policy.Assign:
			if err != nil {
				t.Errorf("%v should be open to anyone, got %v", action, err)
			}
		default:
			if err == nil {
				t.Errorf("%v has no rule: a stranger was permitted", action)
			}
		}
	}
}

func TestAllowsRejectsBadArguments(t *testing.T) {
	owned := build(t, scoped(t), pushed(t), claimedBy(t, "bob"))

	if err := policy.Allows(user.Name{}, owned, policy.Info); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Allows with no actor = %v, want an internal fault", err)
	}
	if err := policy.Allows(agent(t, "bob"), task.Task{}, policy.Info); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Allows with no task = %v, want an internal fault", err)
	}
	if err := policy.Allows(agent(t, "bob"), owned, policy.Action(99)); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Allows with an unknown action = %v, want an internal fault", err)
	}
}

// TestOwnerAndCollaboratorAreDisjoint. Claiming clears collaboration, so an
// agent is never both — which is what keeps `leave` unambiguous.
func TestOwnerIsNeverAlsoACollaborator(t *testing.T) {
	// Carol collaborates, then the task is rebuilt with carol as owner.
	withCarol := build(t, scoped(t), pushed(t), claimedBy(t, "bob"), invited(t, "bob", "carol"))
	if err := policy.Allows(agent(t, "carol"), withCarol, policy.Leave); err != nil {
		t.Fatalf("a collaborator should be able to leave: %v", err)
	}

	asOwner := build(t, scoped(t), pushed(t), claimedBy(t, "carol"))
	if err := policy.Allows(agent(t, "carol"), asOwner, policy.Leave); err == nil {
		t.Error("an owner should not be able to leave")
	}
}
