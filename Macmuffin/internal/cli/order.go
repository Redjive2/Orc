package cli

import (
	"fmt"
	"strings"

	"orc/common/fault"
	"orc/macmuffin/internal/policy"
	"orc/macmuffin/internal/task"
)

// `block` and `unblock`: the order two tasks run in.
//
// Every other relation in Macmuffin lives inside one task under one owner. A
// checklist orders steps; collaborators share the work; a scope says which files
// it may touch. None of them can say that *this* task waits for *that* one, and
// so the only way to sequence two teams was to ask both owners and hope they
// agreed — which holds until the first time somebody is in a hurry, and the
// first time it fails is the time it mattered.

// block holds a task until other tasks are done.
func (a App) block(args []string) error {
	return a.order(args, policy.Block, "block")
}

// unblock releases prerequisites.
func (a App) unblock(args []string) error {
	return a.order(args, policy.Unblock, "unblock")
}

// order is both commands: they take the same arguments, ask the same table, and
// differ only in the event they build and the sentence they print.
func (a App) order(args []string, action policy.Action, verb string) error {
	if len(args) < 2 {
		return fault.Usage{Reason: fmt.Sprintf(
			"%s takes a task and the tasks it waits for: muff %s <task> <task…>", verb, verb)}
	}

	s, err := a.begin()
	if err != nil {
		return err
	}
	name, err := s.resolve(args[0])
	if err != nil {
		return err
	}

	// Every prerequisite is resolved before anything is written, so a command
	// naming one good task and one typo changes nothing. A partial ordering
	// applied and then refused would leave a board nobody could reason about.
	var until []task.Name
	for _, raw := range args[1:] {
		got, err := s.resolve(raw)
		if err != nil {
			return err
		}
		if got.Equal(name) {
			return fault.Usage{Reason: fmt.Sprintf("%s cannot wait for itself", got)}
		}
		until = append(until, got)
	}

	got, err := s.store.Apply(name, func(current task.Task) (task.Event, error) {
		if err := s.permit(current, action); err != nil {
			return task.Event{}, err
		}
		if action == policy.Block {
			return task.Block(s.who, s.store.Now(), until)
		}
		return task.Unblock(s.who, s.store.Now(), until)
	})
	if err != nil {
		return err
	}

	names := make([]string, 0, len(until))
	for _, n := range until {
		names = append(names, a.out.Task(n.String()))
	}
	if action == policy.Unblock {
		return a.say(fmt.Sprintf("%s no longer waits for %s",
			a.out.Task(got.Name().String()), strings.Join(names, ", ")))
	}
	return a.say(fmt.Sprintf("%s waits for %s",
		a.out.Task(got.Name().String()), strings.Join(names, ", ")))
}
