// Package policy answers one question: may this agent do this to this task?
//
// It is one function over one table, consulted by every command, rather than an
// `if` scattered through twelve of them. That matters because the rules are not
// obvious — an owner may not `leave`, a collaborator may not `scope`, and an
// unowned task refuses owner-only actions for a different reason than a
// someone-else's task does. Twelve copies of that would be twelve chances to
// get one wrong, and the wrong one would be discovered by an agent editing
// files it should not have been able to touch.
//
// What this does *not* decide is whether a transition means anything: claiming
// an already-claimed task, pushing twice, completing with unfinished subtasks.
// Those are properties of the task, and they live in task.Task.With. This
// package only ever answers about the actor.
package policy

import (
	"fmt"

	"orc/common/fault"
	"orc/common/user"
	"orc/macmuffin/internal/task"
)

// Action is something an agent might do to a task.
type Action int

const (
	// Info reads a task.
	Info Action = iota
	// Claim takes an unowned task.
	Claim
	// Assign gives an unheld task to another agent.
	Assign
	// Push publishes a draft to the pool.
	Push
	// Scope declares the editable surface.
	Scope
	// Status reports how the work is going.
	Status
	// SubAdd adds a subtask.
	SubAdd
	// SubDone marks a subtask complete.
	SubDone
	// SubDelete removes a subtask.
	SubDelete
	// Invite adds a collaborator.
	Invite
	// Kick removes a collaborator.
	Kick
	// Leave drops collaboration.
	Leave
	// Complete marks the task done.
	Complete
	// Delete removes the task.
	Delete
	// Worktree binds the task to a git worktree.
	Worktree
	// Describe writes the prose that says what the work is.
	Describe
	// Block holds a task until other tasks are done.
	Block
	// Unblock releases one.
	Unblock
	actionCount
)

// verbs name each action as a command, for the refusal message.
var verbs = [actionCount]string{
	Info: "read", Claim: "claim", Assign: "assign", Push: "push", Scope: "scope",
	Status: "set the status of", SubAdd: "add a subtask to",
	SubDone: "complete a subtask of", SubDelete: "delete a subtask of",
	Invite: "invite to", Kick: "kick from", Leave: "leave",
	Complete: "complete", Delete: "delete", Worktree: "bind a worktree to",
	Describe: "describe", Block: "set the order of", Unblock: "release",
}

// String implements fmt.Stringer.
func (a Action) String() string {
	if !a.Valid() {
		return fmt.Sprintf("Action(%d)", int(a))
	}
	return verbs[a]
}

// Valid reports whether a is a defined action.
func (a Action) Valid() bool { return a >= Info && a < actionCount }

// Actions lists every action, so a test can enumerate the table exhaustively.
func Actions() []Action {
	out := make([]Action, 0, actionCount)
	for a := Info; a < actionCount; a++ {
		out = append(out, a)
	}
	return out
}

// who is an actor's relationship to a task. Author is separate from the rest
// because it is the one thing that survives a task having no owner: an agent
// may always delete a draft it made and nobody has taken.
type who int

const (
	stranger who = iota
	author
	collaborator
	owner
)

// rule is what an action requires.
type rule struct {
	// minimum is the weakest relationship that may perform the action.
	minimum who
	// ownerOnly marks the actions an owner alone may take, which are refused
	// differently when nobody owns the task at all.
	ownerOnly bool
	// notOwner marks the actions the owner specifically may *not* take.
	notOwner bool
	// authorMayOnDraft lets a task's author act on an unowned draft they made.
	authorMayOnDraft bool
	// operatorAnyOwner lets the fleet's operator act on a task somebody holds.
	//
	// Everywhere else the operator stands in for a *missing* owner and nothing
	// more; a task with an owner is that owner's, and that limit is what keeps
	// the standing from being a master key. Ordering is the one exception, and
	// it is an exception because of what it is rather than who wants it.
	//
	// An order between two tasks under two owners is by definition a thing
	// neither owner can state. Each may only speak for their own task, so the
	// most either can do is ask the other and hope — which is not an order, it
	// is a suggestion, and it holds exactly as long as nobody is in a hurry. The
	// only party who can sequence two teams is the one above both of them.
	//
	// It stays narrow. This grants the ordering and nothing else: the operator
	// still cannot complete somebody's task, rescope it, take it, or give it
	// away. It says when the work may run, never what it is or whose it is.
	operatorAnyOwner bool
	// authorMayAlways lets a task's author act whoever holds it.
	//
	// For the two actions that say what the work *is*. Whoever wrote a task knows
	// what it was for, and that knowledge does not transfer with the claim — an
	// author who spots a wrong line number, a missing dependency or a scope that
	// names the wrong tree can only watch somebody work from it otherwise, or
	// re-describe it by hand in mail.
	//
	// It is not co-ownership. Everything about *running* the task — completing it,
	// inviting, kicking, pushing it back to the pool, deleting it — stays the
	// owner's alone, because those decide who does the work and whether it is done.
	// This is only the specification, and the specification is the author's.
	authorMayAlways bool
}

// rules is the permission table from the plan, stated once.
var rules = [actionCount]rule{
	// Anyone who can see it can read it, and anyone can take what nobody holds.
	Info:  {minimum: stranger},
	Claim: {minimum: stranger},

	// Assign is claim on somebody else's behalf, so it asks for exactly what
	// claim asks for. The extra condition — that the caller may direct the
	// agent being given the work — is Orc's to answer, not this table's: it is
	// a fact about the fleet, and Macmuffin has no view of one.
	Assign: {minimum: stranger},

	// The owner's call alone: these change what the task is or who can work on
	// it, and a collaborator inheriting that would make ownership meaningless.
	// Push moves a task back to the pool, which takes it off whoever holds it. That
	// is a decision about who does the work rather than about what the work is, so
	// it stays the owner's once there is one.
	Push: {minimum: owner, ownerOnly: true, authorMayOnDraft: true},
	// Scope and Describe say what the task *is*, as against how it is going — and
	// they stay with the author for as long as the task exists.
	//
	// They used to lapse the moment somebody claimed it, which put the person who
	// knows what the work is on the outside of it. An author who spotted a wrong
	// line number in their own description could not fix it; the choice was to mail
	// the owner and hope, or watch the task be done from a specification known to
	// be wrong. Nobody is served by that, least of all the owner.
	//
	// The owner keeps it too. Two people may amend, and the last word is whoever
	// wrote last — which is what the journal is for, and a great deal better than
	// one of them being unable to write at all.
	Scope:    {minimum: owner, ownerOnly: true, authorMayOnDraft: true, authorMayAlways: true},
	Describe: {minimum: owner, ownerOnly: true, authorMayOnDraft: true, authorMayAlways: true},
	// Ordering is the author's for the same reason the scope is — when the work
	// may run is part of what the task is — and the operator's on top of that,
	// because two owners cannot sequence each other.
	Block:    {minimum: owner, ownerOnly: true, authorMayOnDraft: true, authorMayAlways: true, operatorAnyOwner: true},
	Unblock:  {minimum: owner, ownerOnly: true, authorMayOnDraft: true, authorMayAlways: true, operatorAnyOwner: true},
	Invite:   {minimum: owner, ownerOnly: true},
	Kick:     {minimum: owner, ownerOnly: true},
	Complete: {minimum: owner, ownerOnly: true},
	Worktree: {minimum: owner, ownerOnly: true},
	// A draft nobody has claimed is still its author's to throw away.
	Delete: {minimum: owner, ownerOnly: true, authorMayOnDraft: true},

	// Anyone doing the work may report on it and move the checklist.
	Status:    {minimum: collaborator},
	SubAdd:    {minimum: collaborator},
	SubDone:   {minimum: collaborator},
	SubDelete: {minimum: collaborator},

	// An owner cannot leave: a task is never orphaned by accident.
	Leave: {minimum: collaborator, notOwner: true},
}

// Allows reports whether actor may perform action on t.
//
// A refusal is fault.Denied (exit 8) and names the owner, because an agent that
// wanted a task needs to know who to ask — a refusal that ends the conversation
// is worse than one that redirects it.
//
// A task the actor cannot see is reported as *missing* rather than forbidden.
// Drafts are private, and "you may not look at that" would disclose the very
// thing privacy is for.
func Allows(actor user.Name, t task.Task, action Action) error {
	if !action.Valid() {
		return fault.Internal{Where: "policy.Allows", Detail: fmt.Sprintf("unknown action %d", int(action))}
	}
	if actor.Zero() {
		return fault.Internal{Where: "policy.Allows", Detail: "no actor given"}
	}
	if t.Name().Zero() {
		return fault.Internal{Where: "policy.Allows", Detail: "no task given"}
	}

	if !t.Visible(actor) {
		return fault.NotFound{Target: t.Name().String()}
	}

	r := rules[action]
	rank := relationship(actor, t)
	ownerName, owned := t.Owner()

	// The owner is excluded from a few things they would otherwise qualify for.
	if r.notOwner && rank == owner {
		return denied(actor, action, t, "the owner cannot leave their own task; a task is never orphaned by accident. "+
			"complete it with `muff complete <name>`, or delete it")
	}

	if rank >= r.minimum {
		return nil
	}

	// An author keeps the specification of what they wrote, whoever holds it. See
	// authorMayAlways: this is the two actions that say what the work is, and not
	// any of the ones that decide who does it.
	if r.authorMayAlways && rank == author {
		return nil
	}

	// An author may act on a draft they made and nobody has claimed. This is
	// what keeps `create` cheap: sketching a task and then scoping or deleting
	// it should not require claiming it first.
	if r.authorMayOnDraft && rank == author && !owned && !t.Pooled() {
		return nil
	}

	// An unowned task is refused for a different reason than someone else's,
	// and the difference is the whole distance between a dead end and a next
	// step.
	if r.ownerOnly && !owned {
		return denied(actor, action, t, "nobody owns it yet — claim it first with `muff claim "+t.Name().String()+"`")
	}
	if r.minimum == collaborator && !owned {
		return denied(actor, action, t, "nobody owns it yet — claim it first with `muff claim "+t.Name().String()+"`")
	}

	if owned && ownerName.String() != actor.String() {
		return fault.Denied{
			Actor:  actor.String(),
			Action: action.String(),
			Target: t.Name().String(),
			Owner:  ownerName.String(),
		}
	}
	return denied(actor, action, t, "you are not on this task")
}

func denied(actor user.Name, action Action, t task.Task, reason string) error {
	d := fault.Denied{
		Actor:  actor.String(),
		Action: action.String(),
		Target: t.Name().String(),
		Reason: reason,
	}
	if ownerName, owned := t.Owner(); owned {
		d.Owner = ownerName.String()
	}
	return d
}

// relationship reports how close an actor is to a task.
func relationship(actor user.Name, t task.Task) who {
	if ownerName, owned := t.Owner(); owned && ownerName.String() == actor.String() {
		return owner
	}
	if user.Contains(t.Collaborators(), actor) {
		return collaborator
	}
	if t.Author().String() == actor.String() {
		return author
	}
	return stranger
}

// Visible reports whether a task should appear in a listing for this actor.
//
// It is separated from Allows because a board filters rather than refuses: a
// draft belonging to someone else is not an error, it is simply not shown.
func Visible(actor user.Name, t task.Task) bool { return t.Visible(actor) }
