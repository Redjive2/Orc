// Package view projects the store into the frozen shapes the commands render.
//
// It is where "which tasks does this agent see, and in what order" is decided,
// once, rather than in each command. A board that sorted differently from the
// card's idea of the same pool would be a tracker nobody trusts.
package view

import (
	"slices"

	"orc/common/fault"
	"orc/common/user"
	"orc/macmuffin/internal/policy"
	"orc/macmuffin/internal/store"
	"orc/macmuffin/internal/task"
)

// Scope is which tasks a listing considers.
type Scope int

const (
	// Active is the board's default: pooled tasks that are not finished, plus
	// the caller's own drafts.
	Active Scope = iota
	// All adds completed tasks.
	All
)

// Pool is what an agent can see of the store, frozen at load time.
type Pool struct {
	viewer  user.Name
	tasks   []task.Task
	damaged []Damage
}

// Damage records a task the store lists but could not produce.
//
// It is kept rather than silently dropped, and kept rather than made fatal: one
// unreadable task must not hide the rest of the pool, but a board that quietly
// shows nine of ten tasks is worse than one that shows nine and says so.
type Damage struct {
	Name task.Name
	Err  error
}

// Viewer returns whose view this is.
func (p Pool) Viewer() user.Name { return p.viewer }

// Tasks returns every task the viewer can see, in board order.
func (p Pool) Tasks() []task.Task { return slices.Clone(p.tasks) }

// Damaged returns the tasks that could not be read.
func (p Pool) Damaged() []Damage { return slices.Clone(p.damaged) }

// Load builds an agent's view of the pool.
func Load(s *store.Store, viewer user.Name, scope Scope) (Pool, error) {
	if s == nil {
		return Pool{}, fault.Internal{Where: "view.Load", Detail: "no store given"}
	}
	if viewer.Zero() {
		return Pool{}, fault.Internal{Where: "view.Load", Detail: "no viewer given"}
	}

	names, err := s.Names()
	if err != nil {
		return Pool{}, err
	}

	out := Pool{viewer: viewer}
	for _, name := range names {
		got, err := s.Load(name)
		if err != nil {
			out.damaged = append(out.damaged, Damage{Name: name, Err: err})
			continue
		}
		// A draft belonging to someone else is not an error, it is simply not
		// this agent's business — so a board filters rather than refusing.
		if !policy.Visible(viewer, got) {
			continue
		}
		if scope == Active && got.Completed() {
			continue
		}
		out.tasks = append(out.tasks, got)
	}

	Sort(out.tasks)
	return out, nil
}

// Sort orders tasks the way a board shows them.
//
// Completed tasks sink below active ones, so `--all` reads as "the work, then
// the history" rather than as an interleaving nobody can scan. Above that:
// priority descending, then difficulty descending, then oldest first — the most
// pressing and most demanding work at the top, and among equals the thing that
// has been waiting longest.
func Sort(tasks []task.Task) {
	slices.SortStableFunc(tasks, func(a, b task.Task) int {
		if a.Completed() != b.Completed() {
			if a.Completed() {
				return 1
			}
			return -1
		}
		if c := b.Priority().Value() - a.Priority().Value(); c != 0 {
			return c
		}
		if c := b.Difficulty().Value() - a.Difficulty().Value(); c != 0 {
			return c
		}
		if c := a.Created().Compare(b.Created()); c != 0 {
			return c
		}
		// A total order, so a board never reshuffles between two runs that saw
		// the same pool.
		return a.Name().Compare(b.Name())
	})
}

// Find loads one task, refusing it to an agent who may not see it.
//
// The refusal is the policy table's: a task the viewer cannot see is reported
// as missing rather than forbidden, which is what keeps a draft private.
func Find(s *store.Store, viewer user.Name, name task.Name) (task.Task, error) {
	if s == nil {
		return task.Task{}, fault.Internal{Where: "view.Find", Detail: "no store given"}
	}
	got, err := s.Load(name)
	if err != nil {
		return task.Task{}, err
	}
	if err := policy.Allows(viewer, got, policy.Info); err != nil {
		return task.Task{}, err
	}
	return got, nil
}

// Counts summarises a pool for the board's header bar.
func (p Pool) Counts() (active, drafts, done int) {
	for _, t := range p.tasks {
		switch {
		case t.Completed():
			done++
		case !t.Pooled():
			drafts++
		default:
			active++
		}
	}
	return active, drafts, done
}

// Of builds a pool from tasks already in hand.
//
// It exists for the golden corpus and for any caller that has loaded tasks by
// another route: the sort and the counts are the board's, so a fixture cannot
// accidentally render in an order the real thing would never produce.
func Of(viewer user.Name, tasks []task.Task) (Pool, error) {
	if viewer.Zero() {
		return Pool{}, fault.Internal{Where: "view.Of", Detail: "no viewer given"}
	}
	out := Pool{viewer: viewer, tasks: slices.Clone(tasks)}
	Sort(out.tasks)
	return out, nil
}
