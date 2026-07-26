// Package fixture holds the corpus every golden test draws against.
//
// It is one place so a change to the board or the card breaks exactly one
// constant, the way Anno's fixture holds the worked example from its own
// documentation. The pool below is deliberately awkward: a private draft, an
// unowned pooled task, a claimed one with a long checklist, a scopeless stub,
// and a completed one — which is every state §6's retention rule distinguishes.
package fixture

import (
	"fmt"
	"time"

	"orc/common/user"
	"orc/macmuffin/internal/task"
	"orc/macmuffin/internal/view"
)

// Epoch is the instant the corpus is built around, fixed so every rendered
// timestamp is a constant.
var Epoch = time.Date(2026, 7, 24, 18, 31, 4, 0, time.UTC)

// Viewer is whose board the goldens render. Alice authored everything, so she
// can see the drafts too.
const Viewer = "alice"

// Spec describes one task in the corpus.
type Spec struct {
	Name       string
	Priority   int
	Difficulty int
	Offset     time.Duration
	Scope      []string
	Subtasks   []string
	Done       []string
	Push       bool
	Owner      string
	With       []string
	Status     task.Status
	Complete   bool
	Worktree   string
}

// Corpus is the pool the golden tests render.
var Corpus = []Spec{
	{
		Name: "fix-the-parser", Priority: 4, Difficulty: 3,
		Scope:    []string{"internal/tree/", "internal/marker/", "cmd/anno/main.go"},
		Subtasks: []string{"recover-the-grammar", "table-the-sigils", "pin-the-example", "golden-the-index", "classify-every-sigil", "fuzz-the-parser", "wire-the-hook", "document-the-closers"},
		Done:     []string{"recover-the-grammar", "table-the-sigils", "pin-the-example", "golden-the-index", "classify-every-sigil"},
		Push:     true, Owner: "bob", With: []string{"carol"},
		Status: task.StatusNominal, Worktree: "../orc-parser",
	},
	{
		Name: "ship-the-docs", Priority: 5, Difficulty: 2, Offset: time.Hour,
		Scope: []string{"Docs/"}, Push: true,
	},
	{
		Name: "sweep-the-store", Priority: 2, Difficulty: 4, Offset: 2 * time.Hour,
		Scope: []string{"internal/store/"}, Subtasks: []string{"one", "two"},
		Push: true, Owner: "dave", Status: task.StatusBroken,
	},
	{
		// A stub: no scope, so it can only be claimed or deleted.
		Name: "think-about-caching", Priority: 1, Difficulty: 5, Offset: 3 * time.Hour,
	},
	{
		Name: "retire-the-old-hook", Priority: 3, Difficulty: 1, Offset: 4 * time.Hour,
		Scope: []string{"cmd/anno-hook/"}, Subtasks: []string{"done-it"}, Done: []string{"done-it"},
		Push: true, Owner: "alice", Status: task.StatusDone, Complete: true,
	},
}

// Tasks builds the corpus.
func Tasks() ([]task.Task, error) {
	out := make([]task.Task, 0, len(Corpus))
	for _, spec := range Corpus {
		got, err := build(spec)
		if err != nil {
			return nil, fmt.Errorf("building %q: %w", spec.Name, err)
		}
		out = append(out, got)
	}
	view.Sort(out)
	return out, nil
}

// Named returns one task from the corpus by name.
func Named(want string) (task.Task, error) {
	all, err := Tasks()
	if err != nil {
		return task.Task{}, err
	}
	for _, got := range all {
		if got.Name().String() == want {
			return got, nil
		}
	}
	return task.Task{}, fmt.Errorf("no task called %q in the corpus", want)
}

func build(spec Spec) (task.Task, error) {
	at := Epoch.Add(spec.Offset)

	name, err := task.ParseName(spec.Name)
	if err != nil {
		return task.Task{}, err
	}
	author, err := user.Parse(Viewer)
	if err != nil {
		return task.Task{}, err
	}
	p, err := task.NewPriority(spec.Priority)
	if err != nil {
		return task.Task{}, err
	}
	d, err := task.NewDifficulty(spec.Difficulty)
	if err != nil {
		return task.Task{}, err
	}

	got, err := task.NewDraft(name, author, p, d, at)
	if err != nil {
		return task.Task{}, err
	}

	// Everything else arrives the way it does in life: as events folded on in
	// the order they could actually have happened.
	steps := []func() (task.Event, error){}
	if len(spec.Scope) > 0 {
		steps = append(steps, func() (task.Event, error) { return task.Scope(author, at, spec.Scope) })
	}
	for _, sub := range spec.Subtasks {
		steps = append(steps, func() (task.Event, error) {
			n, err := task.ParseName(sub)
			if err != nil {
				return task.Event{}, err
			}
			return task.AddSub(author, at, n)
		})
	}
	if spec.Push {
		steps = append(steps, func() (task.Event, error) { return task.Push(author, at) })
	}
	if spec.Owner != "" {
		steps = append(steps, func() (task.Event, error) {
			who, err := user.Parse(spec.Owner)
			if err != nil {
				return task.Event{}, err
			}
			return task.Claim(who, at)
		})
	}
	for _, with := range spec.With {
		steps = append(steps, func() (task.Event, error) {
			by, err := user.Parse(spec.Owner)
			if err != nil {
				return task.Event{}, err
			}
			who, err := user.Parse(with)
			if err != nil {
				return task.Event{}, err
			}
			return task.Invite(by, at, who)
		})
	}
	for _, sub := range spec.Done {
		steps = append(steps, func() (task.Event, error) {
			n, err := task.ParseName(sub)
			if err != nil {
				return task.Event{}, err
			}
			return task.DoneSub(author, at, n)
		})
	}
	if spec.Status != task.StatusUnset {
		steps = append(steps, func() (task.Event, error) { return task.SetStatus(author, at, spec.Status) })
	}
	if spec.Worktree != "" {
		steps = append(steps, func() (task.Event, error) { return task.BindWorktree(author, at, spec.Worktree) })
	}
	if spec.Complete {
		steps = append(steps, func() (task.Event, error) { return task.Complete(author, at, false, nil) })
	}

	for _, step := range steps {
		ev, err := step()
		if err != nil {
			return task.Task{}, err
		}
		if got, err = got.With(ev); err != nil {
			return task.Task{}, err
		}
	}
	return got, nil
}
