package task

import (
	"fmt"
	"slices"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
)

// Bounds on a task. Each turns a pathological input into a clear message rather
// than a board nobody can read.
const (
	// MaxSubtasks is far past the point where a flat checklist is the right
	// shape. A task needing more than this wants to be several tasks.
	MaxSubtasks = 256

	// MaxCollaborators bounds the membership of one task.
	MaxCollaborators = 64

	// MaxScopeEntries bounds a declared editable surface. A scope this wide is
	// not a scope.
	MaxScopeEntries = 512
)

// Subtask is one step of a task: a name and whether it is done.
//
// The vision asks for subtasks "arranged in groups as steps"; the reference
// gives no syntax for a group, so the list is flat and grouping is deferred.
// Nothing here carries a group field, which is what makes adding one later an
// additive change rather than a migration.
type Subtask struct {
	name Name
	done bool
	at   time.Time // when it was added
}

// NewSubtask builds a subtask.
func NewSubtask(name Name, at time.Time) (Subtask, error) {
	s := Subtask{name: name, at: clock.Normalise(at)}
	if err := s.validate(); err != nil {
		return Subtask{}, err
	}
	return s, nil
}

func (s Subtask) validate() error {
	const where = "task.Subtask"
	if s.name.Zero() {
		return fault.Internal{Where: where, Detail: "subtask has no name"}
	}
	if err := s.name.validate(); err != nil {
		return err
	}
	return fault.Check(!s.at.IsZero(), where, "subtask %s has no creation time", s.name)
}

// Name returns the subtask's name, which is unique within its task.
func (s Subtask) Name() Name { return s.name }

// Done reports whether the subtask is complete.
func (s Subtask) Done() bool { return s.done }

// Added returns when the subtask was created.
func (s Subtask) Added() time.Time { return s.at }

// Completed returns a copy marked done. The original is untouched: a subtask is
// a value, and the state lives in the journal that produced it.
func (s Subtask) Completed() Subtask {
	out := s
	out.done = true
	return out
}

// Mark returns the glyph for a checklist. It is shown beside the name, never
// instead of it, so a pipe through grep keeps the state.
func (s Subtask) Mark() string {
	if s.done {
		return "✓"
	}
	return "○"
}

// Life is whether a task has been published to the pool.
//
// `create` drafts and `push` publishes, so creation does not expose anything. A
// draft is visible only to its author and can be shaped freely; a pooled task is
// every agent's business. Push is one-way.
type Life int

const (
	// Draft is a task its author has not published.
	Draft Life = iota
	// Pooled is a task in the shared pool.
	Pooled
)

// String implements fmt.Stringer.
func (l Life) String() string {
	switch l {
	case Draft:
		return "draft"
	case Pooled:
		return "pooled"
	default:
		return fmt.Sprintf("Life(%d)", int(l))
	}
}

// Valid reports whether l is a defined life.
func (l Life) Valid() bool { return l == Draft || l == Pooled }

// Task is a task as it stands: the creation record plus everything the journal
// has folded onto it. It is immutable once built.
type Task struct {
	name       Name
	author     user.Name
	created    time.Time
	priority   Score
	difficulty Score

	life          Life
	owner         user.Name // zero when unowned
	collaborators []user.Name
	status        Status
	scope         []string
	subtasks      []Subtask
	completed     bool
	completedAt   time.Time
	worktree      string
}

// NewDraft builds a newly created task, before anything has happened to it.
//
// It is the only constructor that does not take a state: everything else about
// a task arrives by folding its journal, and a task that could be constructed
// mid-life would be a second way to reach a state the journal is supposed to be
// the record of.
func NewDraft(name Name, author user.Name, priority, difficulty Score, at time.Time) (Task, error) {
	t := Task{
		name:       name,
		author:     author,
		created:    clock.Normalise(at),
		priority:   priority,
		difficulty: difficulty,
		life:       Draft,
		status:     StatusUnset,
	}
	if err := t.validate(); err != nil {
		return Task{}, err
	}
	return t, nil
}

func (t Task) validate() error {
	const where = "task.Task"

	if t.name.Zero() {
		return fault.Internal{Where: where, Detail: "task has no name"}
	}
	if err := t.name.validate(); err != nil {
		return err
	}
	if t.author.Zero() {
		return fault.Internal{Where: where, Detail: "task " + t.name.String() + " has no author"}
	}
	if err := fault.Check(!t.created.IsZero(), where, "task %s has no creation time", t.name); err != nil {
		return err
	}
	if err := fault.Check(t.life.Valid(), where, "task %s has life %d", t.name, int(t.life)); err != nil {
		return err
	}
	if err := fault.Check(t.status.Known(), where, "task %s has status %d", t.name, int(t.status)); err != nil {
		return err
	}

	// Both scores are set at creation and never change, so a task without them
	// could not have come from Draft.
	if t.priority.Zero() || t.difficulty.Zero() {
		return fault.Internal{Where: where, Detail: fmt.Sprintf("task %s is missing a score", t.name)}
	}
	if err := t.priority.validate(); err != nil {
		return err
	}
	if err := t.difficulty.validate(); err != nil {
		return err
	}

	if err := fault.Check(len(t.subtasks) <= MaxSubtasks, where,
		"task %s has %d subtasks, over the %d limit", t.name, len(t.subtasks), MaxSubtasks); err != nil {
		return err
	}
	if err := fault.Check(len(t.collaborators) <= MaxCollaborators, where,
		"task %s has %d collaborators, over the %d limit", t.name, len(t.collaborators), MaxCollaborators); err != nil {
		return err
	}
	if err := fault.Check(len(t.scope) <= MaxScopeEntries, where,
		"task %s has %d scope entries, over the %d limit", t.name, len(t.scope), MaxScopeEntries); err != nil {
		return err
	}

	// A subtask name is unique per task; two with one name could not be told
	// apart by `--sub`, which is how every command addresses them.
	seen := make(map[string]bool, len(t.subtasks))
	for _, s := range t.subtasks {
		if err := s.validate(); err != nil {
			return err
		}
		if seen[s.name.String()] {
			return fault.Internal{Where: where, Detail: fmt.Sprintf(
				"task %s has two subtasks named %s", t.name, s.name)}
		}
		seen[s.name.String()] = true
	}

	// The owner is never also a collaborator: the permission table gives them
	// strictly more, so being both would make the second membership meaningless
	// and `leave` ambiguous.
	for _, c := range t.collaborators {
		if c.Zero() {
			return fault.Internal{Where: where, Detail: "task " + t.name.String() + " has an unset collaborator"}
		}
		if !t.owner.Zero() && c.String() == t.owner.String() {
			return fault.Internal{Where: where, Detail: fmt.Sprintf(
				"task %s lists its owner %s as a collaborator", t.name, c)}
		}
	}
	if dup := firstDuplicate(t.collaborators); dup != "" {
		return fault.Internal{Where: where, Detail: fmt.Sprintf(
			"task %s lists %s as a collaborator twice", t.name, dup)}
	}

	// Completion carries a time, and only a completed task has one.
	if t.completed != !t.completedAt.IsZero() {
		return fault.Internal{Where: where, Detail: fmt.Sprintf(
			"task %s is completed=%v with completion time %v", t.name, t.completed, t.completedAt)}
	}
	return nil
}

func firstDuplicate(names []user.Name) string {
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		if seen[n.String()] {
			return n.String()
		}
		seen[n.String()] = true
	}
	return ""
}

// Name returns the task's handle.
func (t Task) Name() Name { return t.name }

// Author returns who created it.
func (t Task) Author() user.Name { return t.author }

// Created returns when it was created.
func (t Task) Created() time.Time { return t.created }

// Priority returns the priority score.
func (t Task) Priority() Score { return t.priority }

// Difficulty returns the difficulty score.
func (t Task) Difficulty() Score { return t.difficulty }

// Life reports whether the task is a draft or pooled.
func (t Task) Life() Life { return t.life }

// Pooled reports whether the task has been published.
func (t Task) Pooled() bool { return t.life == Pooled }

// Owner returns who holds the task, and whether anyone does. An unowned pooled
// task is the normal resting state — that is what a pool is.
func (t Task) Owner() (user.Name, bool) { return t.owner, !t.owner.Zero() }

// Collaborators returns a copy of the collaborator list, in join order.
func (t Task) Collaborators() []user.Name { return slices.Clone(t.collaborators) }

// Status returns the health signal.
func (t Task) Status() Status { return t.status }

// Scope returns a copy of the declared editable surface.
func (t Task) Scope() []string { return slices.Clone(t.scope) }

// Scoped reports whether a scope has been declared.
//
// It is the gate on nearly everything: the reference says a task "cannot be
// edited or completed, only claimed or deleted, until a scope is added", so a
// scopeless task is a stub. A task with no declared surface tells an onlooker
// nothing about what is about to change under them, which is the opposite of
// what the pool is for.
func (t Task) Scoped() bool { return len(t.scope) > 0 }

// Subtasks returns a copy of the checklist, in creation order.
func (t Task) Subtasks() []Subtask { return slices.Clone(t.subtasks) }

// Progress returns how many subtasks are done and how many there are.
func (t Task) Progress() (done, total int) {
	for _, s := range t.subtasks {
		if s.done {
			done++
		}
	}
	return done, len(t.subtasks)
}

// Unfinished returns the subtasks still outstanding, which is what `complete`
// lists when it refuses.
func (t Task) Unfinished() []Subtask {
	var out []Subtask
	for _, s := range t.subtasks {
		if !s.done {
			out = append(out, s)
		}
	}
	return out
}

// Completed reports whether the task is finished.
func (t Task) Completed() bool { return t.completed }

// CompletedAt returns when it was finished, or the zero time.
func (t Task) CompletedAt() time.Time { return t.completedAt }

// Worktree returns the bound git worktree, and whether there is one.
func (t Task) Worktree() (string, bool) { return t.worktree, t.worktree != "" }

// Active reports whether the task belongs on the board by default: pooled and
// not yet finished. Completed tasks leave the board and come back under --all.
func (t Task) Active() bool { return t.Pooled() && !t.completed }

// Involves reports whether a user is the owner or a collaborator. It is the
// membership question the permission table asks, and the one `reply`-style
// notifications are addressed from.
func (t Task) Involves(name user.Name) bool {
	if name.Zero() {
		return false
	}
	if !t.owner.Zero() && t.owner.String() == name.String() {
		return true
	}
	return user.Contains(t.collaborators, name)
}

// Visible reports whether a user may see the task at all.
//
// A draft is its author's business, plus anyone they deliberately added to it;
// anything pooled is every agent's. Membership implies visibility because the
// alternative is an invitation to a task the invitee cannot see, which is worse
// than either sharing the draft or refusing the invite.
//
// This is the only visibility rule in the tool, and it is here rather than in
// the permission table because it decides whether a task exists for a caller,
// not what they may do with it.
func (t Task) Visible(to user.Name) bool {
	if t.Pooled() {
		return true
	}
	return !to.Zero() && (t.author.String() == to.String() || t.Involves(to))
}
