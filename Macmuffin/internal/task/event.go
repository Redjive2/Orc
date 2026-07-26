package task

import (
	"fmt"
	"slices"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
)

// Op is what an event does to a task.
type Op string

const (
	// OpScope declares the editable surface. It replaces any previous scope
	// rather than adding to it, so `muff scope` always states the whole surface
	// and a reader never has to accumulate a history to know what it is.
	OpScope Op = "scope"
	// OpPush publishes a draft to the pool. One-way.
	OpPush Op = "push"
	// OpClaim takes an unowned task.
	OpClaim Op = "claim"
	// OpAssign gives an unheld task to another agent. It is claim performed on
	// somebody else's behalf, and it is legal here for the same reasons claim
	// is; whether the caller may direct that agent is Orc's question, asked
	// before the event is ever built.
	OpAssign Op = "assign"
	// OpStatus sets the health signal.
	OpStatus Op = "status"
	// OpInvite adds a collaborator.
	OpInvite Op = "invite"
	// OpKick removes a collaborator.
	OpKick Op = "kick"
	// OpLeave is a collaborator removing themselves.
	OpLeave Op = "leave"
	// OpSubAdd adds a subtask.
	OpSubAdd Op = "sub.add"
	// OpSubDone marks a subtask complete.
	OpSubDone Op = "sub.done"
	// OpSubDelete removes a subtask.
	OpSubDelete Op = "sub.del"
	// OpComplete marks the whole task done.
	OpComplete Op = "complete"
	// OpWorktree binds the task to a git worktree.
	OpWorktree Op = "worktree"
)

// Ops lists every operation, in the order a help text should show them. It
// exists so a test can assert the fold handles all of them.
func Ops() []Op {
	return []Op{
		OpScope, OpPush, OpClaim, OpAssign, OpStatus, OpInvite, OpKick, OpLeave,
		OpSubAdd, OpSubDone, OpSubDelete, OpComplete, OpWorktree,
	}
}

// Valid reports whether o is a defined operation.
func (o Op) Valid() bool { return slices.Contains(Ops(), o) }

// Event is one thing that happened to a task.
//
// It is a single struct rather than a variant per operation because it is
// stored as one JSON object per line, and a decoder that had to dispatch on the
// operation before it could parse the rest would have two places to keep in
// step. The cost is that most fields are empty most of the time, and validate
// insists on exactly that: an event carrying a field its operation has no use
// for is refused, so a `claim` can never smuggle a scope past the fold.
type Event struct {
	op      Op
	by      user.Name
	at      time.Time
	subtask Name
	agent   user.Name
	paths   []string
	status  Status
	path    string
	forced  bool
	skipped []Name
}

// NewEvent builds an event. Callers use the helpers below rather than this
// directly; it exists so the decoder has one way in that validates.
func NewEvent(op Op, by user.Name, at time.Time) (Event, error) {
	e := Event{op: op, by: by, at: clock.Normalise(at)}
	if err := e.validate(); err != nil {
		return Event{}, err
	}
	return e, nil
}

// The constructors. Each fills exactly the fields its operation uses, which is
// what lets validate refuse anything else.

// Scope declares the editable surface.
func Scope(by user.Name, at time.Time, paths []string) (Event, error) {
	return build(Event{op: OpScope, by: by, at: at, paths: slices.Clone(paths)})
}

// Push publishes a draft.
func Push(by user.Name, at time.Time) (Event, error) {
	return build(Event{op: OpPush, by: by, at: at})
}

// Claim takes an unowned task.
func Claim(by user.Name, at time.Time) (Event, error) {
	return build(Event{op: OpClaim, by: by, at: at})
}

// Assign gives the task to another agent.
func Assign(by user.Name, at time.Time, who user.Name) (Event, error) {
	return build(Event{op: OpAssign, by: by, at: at, agent: who})
}

// SetStatus reports how the work is going.
func SetStatus(by user.Name, at time.Time, s Status) (Event, error) {
	return build(Event{op: OpStatus, by: by, at: at, status: s})
}

// Invite adds a collaborator.
func Invite(by user.Name, at time.Time, who user.Name) (Event, error) {
	return build(Event{op: OpInvite, by: by, at: at, agent: who})
}

// Kick removes a collaborator.
func Kick(by user.Name, at time.Time, who user.Name) (Event, error) {
	return build(Event{op: OpKick, by: by, at: at, agent: who})
}

// Leave is a collaborator removing themselves.
func Leave(by user.Name, at time.Time) (Event, error) {
	return build(Event{op: OpLeave, by: by, at: at})
}

// AddSub adds a subtask.
func AddSub(by user.Name, at time.Time, sub Name) (Event, error) {
	return build(Event{op: OpSubAdd, by: by, at: at, subtask: sub})
}

// DoneSub marks a subtask complete.
func DoneSub(by user.Name, at time.Time, sub Name) (Event, error) {
	return build(Event{op: OpSubDone, by: by, at: at, subtask: sub})
}

// DeleteSub removes a subtask.
func DeleteSub(by user.Name, at time.Time, sub Name) (Event, error) {
	return build(Event{op: OpSubDelete, by: by, at: at, subtask: sub})
}

// Complete marks the task done. forced records that it was completed over
// unfinished subtasks, and skipped names them — the point of a tracker is that
// shortcuts stay visible, so the override leaves a mark.
func Complete(by user.Name, at time.Time, forced bool, skipped []Name) (Event, error) {
	return build(Event{op: OpComplete, by: by, at: at, forced: forced, skipped: slices.Clone(skipped)})
}

// BindWorktree ties the task to a git worktree.
func BindWorktree(by user.Name, at time.Time, path string) (Event, error) {
	return build(Event{op: OpWorktree, by: by, at: at, path: path})
}

func build(e Event) (Event, error) {
	e.at = clock.Normalise(e.at)
	if err := e.validate(); err != nil {
		return Event{}, err
	}
	return e, nil
}

// validate checks the event's shape: that it has what its operation needs and
// nothing it does not.
func (e Event) validate() error {
	const where = "task.Event"

	if !e.op.Valid() {
		return fault.Internal{Where: where, Detail: fmt.Sprintf("unknown operation %q", e.op)}
	}
	if e.by.Zero() {
		return fault.Internal{Where: where, Detail: string(e.op) + " event has no actor"}
	}
	if err := fault.Check(!e.at.IsZero(), where, "%s event has no time", e.op); err != nil {
		return err
	}

	// What each operation must carry, and by omission what it must not.
	wantSub := e.op == OpSubAdd || e.op == OpSubDone || e.op == OpSubDelete
	wantAgent := e.op == OpInvite || e.op == OpKick || e.op == OpAssign
	wantPaths := e.op == OpScope
	wantStatus := e.op == OpStatus
	wantPath := e.op == OpWorktree

	if got := !e.subtask.Zero(); got != wantSub {
		return shapeError(where, e.op, "a subtask name", wantSub)
	}
	if got := !e.agent.Zero(); got != wantAgent {
		return shapeError(where, e.op, "an agent", wantAgent)
	}
	if got := len(e.paths) > 0; got != wantPaths {
		return shapeError(where, e.op, "scope paths", wantPaths)
	}
	if got := e.status != StatusUnset; got != wantStatus {
		return shapeError(where, e.op, "a status", wantStatus)
	}
	if got := e.path != ""; got != wantPath {
		return shapeError(where, e.op, "a worktree path", wantPath)
	}
	if (e.forced || len(e.skipped) > 0) && e.op != OpComplete {
		return shapeError(where, e.op, "a forced flag", false)
	}

	switch e.op {
	case OpStatus:
		if !e.status.Valid() {
			return fault.Internal{Where: where, Detail: fmt.Sprintf("status %d is not settable", int(e.status))}
		}
	case OpScope:
		if len(e.paths) > MaxScopeEntries {
			return fault.Internal{Where: where, Detail: fmt.Sprintf(
				"scope has %d entries, over the %d limit", len(e.paths), MaxScopeEntries)}
		}
		for _, p := range e.paths {
			if p == "" {
				return fault.Internal{Where: where, Detail: "scope contains an empty path"}
			}
		}
	case OpComplete:
		if len(e.skipped) > 0 && !e.forced {
			return fault.Internal{Where: where, Detail: "unforced completion lists skipped subtasks"}
		}
	}
	return nil
}

func shapeError(where string, op Op, what string, wanted bool) error {
	if wanted {
		return fault.Internal{Where: where, Detail: fmt.Sprintf("%s event needs %s", op, what)}
	}
	return fault.Internal{Where: where, Detail: fmt.Sprintf("%s event must not carry %s", op, what)}
}

// Accessors. The store needs these to encode an event; nothing else should.

// Op returns what the event does.
func (e Event) Op() Op { return e.op }

// By returns who did it.
func (e Event) By() user.Name { return e.by }

// At returns when.
func (e Event) At() time.Time { return e.at }

// Subtask returns the subtask named, if any.
func (e Event) Subtask() Name { return e.subtask }

// Agent returns the agent named, if any.
func (e Event) Agent() user.Name { return e.agent }

// Paths returns a copy of the scope declared, if any.
func (e Event) Paths() []string { return slices.Clone(e.paths) }

// Status returns the status set, if any.
func (e Event) Status() Status { return e.status }

// Path returns the worktree bound, if any.
func (e Event) Path() string { return e.path }

// Forced reports whether a completion overrode unfinished subtasks.
func (e Event) Forced() bool { return e.forced }

// Skipped returns the subtasks a forced completion left undone.
func (e Event) Skipped() []Name { return slices.Clone(e.skipped) }

// Zero reports whether the event was never constructed.
func (e Event) Zero() bool { return e.op == "" }

// With folds one event onto a task, returning the result.
//
// This is where a transition is legal or not — `claim` on an owned task,
// `push` on a task already pooled, a subtask completed twice. Who is *allowed*
// to make the transition is a separate question, answered by the policy table
// before this is reached; what this enforces is whether the transition means
// anything at all, which is a property of the task rather than of the caller.
//
// An illegal transition is a Conflict, and it names the state that blocks it —
// an agent that lost a claim race needs to know who won, not that it failed.
func (t Task) With(e Event) (Task, error) {
	if err := e.validate(); err != nil {
		return Task{}, err
	}
	if err := t.validate(); err != nil {
		return Task{}, err
	}

	out := t
	out.collaborators = slices.Clone(t.collaborators)
	out.scope = slices.Clone(t.scope)
	out.subtasks = slices.Clone(t.subtasks)

	conflict := func(format string, args ...any) (Task, error) {
		return Task{}, fault.Conflict{Path: t.name.String(), Reason: fmt.Sprintf(format, args...)}
	}

	switch e.op {
	case OpScope:
		out.scope = slices.Clone(e.paths)

	case OpPush:
		if t.Pooled() {
			return conflict("already in the pool")
		}
		if !t.Scoped() {
			return conflict("needs a scope before it can be pushed")
		}
		out.life = Pooled

	case OpClaim:
		// The compare-and-set the whole tool rests on. Two agents scanning the
		// same pool will claim within milliseconds of each other; the second
		// must lose, and must be told by whom.
		if owner, owned := t.Owner(); owned {
			if owner.String() == e.by.String() {
				return conflict("you already own it")
			}
			return conflict("already claimed by %s", owner)
		}
		out.owner = e.by
		// An owner is never also a collaborator: the owner has strictly more,
		// so keeping both would make `leave` ambiguous.
		out.collaborators = without(out.collaborators, e.by)

	case OpAssign:
		// The same compare-and-set as claim, and it must lose the same race:
		// two bosses assigning the same pooled task to two subagents is exactly
		// the situation the check exists for.
		if owner, owned := t.Owner(); owned {
			if owner.String() == e.agent.String() {
				return conflict("%s already owns it", e.agent)
			}
			return conflict("already claimed by %s", owner)
		}
		out.owner = e.agent
		out.collaborators = without(out.collaborators, e.agent)

	case OpStatus:
		out.status = e.status

	case OpInvite:
		if owner, owned := t.Owner(); owned && owner.String() == e.agent.String() {
			return conflict("%s already owns it", e.agent)
		}
		if user.Contains(t.collaborators, e.agent) {
			return conflict("%s is already a collaborator", e.agent)
		}
		if len(t.collaborators) >= MaxCollaborators {
			return conflict("already has %d collaborators, the limit", MaxCollaborators)
		}
		out.collaborators = append(out.collaborators, e.agent)

	case OpKick:
		if !user.Contains(t.collaborators, e.agent) {
			return conflict("%s is not a collaborator", e.agent)
		}
		out.collaborators = without(out.collaborators, e.agent)

	case OpLeave:
		if owner, owned := t.Owner(); owned && owner.String() == e.by.String() {
			return conflict("the owner cannot leave; a task is never orphaned by accident")
		}
		if !user.Contains(t.collaborators, e.by) {
			return conflict("%s is not a collaborator", e.by)
		}
		out.collaborators = without(out.collaborators, e.by)

	case OpSubAdd:
		if !t.Scoped() {
			return conflict("needs a scope before subtasks can be added")
		}
		if findSub(t.subtasks, e.subtask) >= 0 {
			return conflict("already has a subtask called %s", e.subtask)
		}
		if len(t.subtasks) >= MaxSubtasks {
			return conflict("already has %d subtasks, the limit", MaxSubtasks)
		}
		sub, err := NewSubtask(e.subtask, e.at)
		if err != nil {
			return Task{}, err
		}
		out.subtasks = append(out.subtasks, sub)

	case OpSubDone:
		at := findSub(t.subtasks, e.subtask)
		if at < 0 {
			return conflict("has no subtask called %s", e.subtask)
		}
		if t.subtasks[at].Done() {
			// Not an error: two agents finishing the same step at once is
			// ordinary, and the first completion is the one that is true.
			return out, nil
		}
		out.subtasks[at] = out.subtasks[at].Completed()

	case OpSubDelete:
		at := findSub(t.subtasks, e.subtask)
		if at < 0 {
			return conflict("has no subtask called %s", e.subtask)
		}
		out.subtasks = slices.Delete(out.subtasks, at, at+1)

	case OpComplete:
		if t.completed {
			return conflict("already completed")
		}
		if !t.Scoped() {
			return conflict("needs a scope before it can be completed")
		}
		if len(t.Unfinished()) > 0 && !e.forced {
			return conflict("has %d unfinished subtasks", len(t.Unfinished()))
		}
		out.completed = true
		out.completedAt = e.at

	case OpWorktree:
		if !t.Scoped() {
			return conflict("needs a scope before a worktree can be bound")
		}
		out.worktree = e.path

	default:
		return Task{}, fault.Internal{Where: "task.With", Detail: fmt.Sprintf("unhandled operation %q", e.op)}
	}

	if err := out.validate(); err != nil {
		return Task{}, err
	}
	return out, nil
}

func without(names []user.Name, drop user.Name) []user.Name {
	out := names[:0:0]
	for _, n := range names {
		if n.String() != drop.String() {
			out = append(out, n)
		}
	}
	return out
}

func findSub(subs []Subtask, name Name) int {
	for i, s := range subs {
		if s.Name().Equal(name) {
			return i
		}
	}
	return -1
}
