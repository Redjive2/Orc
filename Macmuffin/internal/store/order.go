package store

import (
	"fmt"
	"strings"

	"orc/common/fault"
	"orc/macmuffin/internal/task"
)

// Ordering between tasks, and the one place it is enforced.
//
// A task records the names it waits for; whether those are finished is a
// question about other tasks, so it cannot be answered where the record lives.
// It is answered here, and it is asked in exactly one place — Apply, the choke
// point every mutation already passes through. A gate written into each command
// instead would be as many chances to forget it as there are commands, and the
// one that was forgotten would be found by an agent doing work out of order.

// MaxOrderDepth bounds how far a cycle search will walk.
//
// A chain longer than this is not an ordering anybody is reading, and the bound
// means a store that somehow holds a cycle cannot hang the command that found
// it. Reaching the bound is reported rather than assumed safe.
const MaxOrderDepth = 64

// gated are the operations ordering holds back.
//
// Start and finish, and nothing between them. `status` is deliberately absent: it
// is a report on how work is going, and a tool that refused to let an agent state
// the truth about a task would be teaching agents that the record is fiction.
// Subtasks are absent for the same reason.
var gated = map[task.Op]string{
	task.OpClaim:    "claimed",
	task.OpAssign:   "assigned",
	task.OpComplete: "completed",
}

// Open returns the prerequisites of t that are not yet finished.
//
// A prerequisite that no longer exists is treated as cleared, and named in the
// second return so a caller can say so. The alternative is a task held forever
// behind something deleted, which no command could then release — the ordering
// would outlive the thing it was ordering against.
func (s *Store) Open(t task.Task) (open []task.Name, gone []task.Name, err error) {
	for _, name := range t.BlockedOn() {
		other, err := s.Load(name)
		if err != nil {
			if isNotFound(err) {
				gone = append(gone, name)
				continue
			}
			return nil, nil, err
		}
		if !other.Clears() {
			open = append(open, name)
		}
	}
	return open, gone, nil
}

// holds refuses a gated operation while any prerequisite is outstanding.
//
// The refusal is a Conflict and names every task still outstanding, with its
// owner. An agent told only "blocked" has to go and find out by whom; an agent
// told who holds the work it is waiting for can go and ask them.
func (s *Store) holds(t task.Task, op task.Op) error {
	verb, ok := gated[op]
	if !ok || !t.Blocked() {
		return nil
	}
	open, _, err := s.Open(t)
	if err != nil {
		return err
	}
	if len(open) == 0 {
		return nil
	}

	var parts []string
	for _, name := range open {
		if other, err := s.Load(name); err == nil {
			if owner, owned := other.Owner(); owned {
				parts = append(parts, fmt.Sprintf("%s (%s, %s)", name, owner, other.Status()))
				continue
			}
			parts = append(parts, fmt.Sprintf("%s (unclaimed)", name))
			continue
		}
		parts = append(parts, name.String())
	}
	return fault.Conflict{Path: t.Name().String(), Reason: fmt.Sprintf(
		"cannot be %s until it is done waiting for %s", verb, strings.Join(parts, ", "))}
}

// reaches reports whether from waits, directly or through a chain, for target.
//
// It is the cycle check, and it runs when an ordering is declared rather than
// when one is enforced. A cycle refused at the gate would be a board that
// accepted the statement and then refused every task in the ring, with nothing
// to say which declaration caused it.
func (s *Store) reaches(from, target task.Name, seen map[string]bool, depth int) (bool, error) {
	if depth > MaxOrderDepth {
		return false, fault.Conflict{Path: from.String(), Reason: fmt.Sprintf(
			"the chain of prerequisites is over %d deep; it cannot be checked for a cycle", MaxOrderDepth)}
	}
	if from.Equal(target) {
		return true, nil
	}
	if seen[from.String()] {
		return false, nil
	}
	seen[from.String()] = true

	t, err := s.Load(from)
	if err != nil {
		if isNotFound(err) {
			// Nothing to walk. A missing task clears rather than blocks, so it
			// cannot be part of a live cycle either.
			return false, nil
		}
		return false, err
	}
	for _, next := range t.BlockedOn() {
		got, err := s.reaches(next, target, seen, depth+1)
		if err != nil || got {
			return got, err
		}
	}
	return false, nil
}

// wouldCycle refuses an ordering that closes a ring.
func (s *Store) wouldCycle(t task.Task, ev task.Event) error {
	if ev.Op() != task.OpBlock {
		return nil
	}
	for _, want := range ev.Until() {
		got, err := s.reaches(want, t.Name(), map[string]bool{}, 0)
		if err != nil {
			return err
		}
		if got {
			return fault.Conflict{Path: t.Name().String(), Reason: fmt.Sprintf(
				"%s already waits for %s, so this would make a cycle", want, t.Name())}
		}
	}
	return nil
}
