package cli

import (
	"encoding/json"
	"time"

	"orc/common/fault"
	"orc/common/user"
	"orc/macmuffin/internal/task"
)

// The `--json` projection.
//
// It exists so other Orc tools can read the pool without parsing the board —
// a presentation format is a bad contract, and Communiqué needs a good one to
// mirror the pool to the web.
//
// Two rules keep it usable as a contract:
//
//   - It is a projection of the same task.Task the board renders, so the JSON
//     and the board can never disagree about what the pool holds.
//   - Fields are added, never repurposed or removed. A reader that ignores what
//     it does not recognise keeps working across a version it has not seen.

// jsonTask is one task.
type jsonTask struct {
	Name          string        `json:"name"`
	Author        string        `json:"author"`
	Created       time.Time     `json:"created"`
	Owner         string        `json:"owner,omitempty"`
	Collaborators []string      `json:"collaborators,omitempty"`
	Priority      int           `json:"priority"`
	Difficulty    int           `json:"difficulty"`
	Status        int           `json:"status"`
	StatusWord    string        `json:"status_word"`
	Done          int           `json:"done"`
	Total         int           `json:"total"`
	Draft         bool          `json:"draft"`
	Completed     bool          `json:"completed"`
	Scope         []string      `json:"scope,omitempty"`
	Worktree      string        `json:"worktree,omitempty"`
	Subtasks      []jsonSubtask `json:"subtasks,omitempty"`

	// Described says a description.md sits beside the task, with who last changed
	// it and when. It is on the board as well as in `info`, because "which of
	// these has a specification" is a question about the pool.
	Described   bool      `json:"described,omitempty"`
	DescribedBy string    `json:"described_by,omitempty"`
	DescribedAt time.Time `json:"described_at,omitzero"`
	// Description is the prose itself, and travels only with `info`.
	//
	// The board is a board: carrying up to 32 KiB of markdown per task would make
	// a listing of forty tasks something nobody wants to sync. A reader that needs
	// the text asks for the one task it is about — which is what cq does, and only
	// for the tasks the board says have one.
	Description string `json:"description,omitempty"`
}

// jsonSubtask is one step of one task.
type jsonSubtask struct {
	Name  string    `json:"name"`
	Done  bool      `json:"done"`
	Added time.Time `json:"added"`
}

// taskJSON projects one task. Subtasks are included only for `info`: the pool
// is a board, and a listing that carried every step of every task would be a
// different thing entirely.
func taskJSON(t task.Task, subtasks bool) jsonTask {
	done, total := t.Progress()
	out := jsonTask{
		Name:          t.Name().String(),
		Author:        t.Author().String(),
		Created:       t.Created(),
		Collaborators: names(t.Collaborators()),
		Priority:      t.Priority().Value(),
		Difficulty:    t.Difficulty().Value(),
		Status:        int(t.Status()),
		StatusWord:    t.Status().String(),
		Done:          done,
		Total:         total,
		Draft:         !t.Pooled(),
		Completed:     t.Completed(),
		Scope:         t.Scope(),
		Worktree:      worktree(t),
		Described:     t.Described(),
	}
	if t.Described() {
		out.DescribedBy = t.DescribedBy().String()
		out.DescribedAt = t.DescribedAt()
	}
	if owner, ok := t.Owner(); ok {
		out.Owner = owner.String()
	}
	if subtasks {
		for _, s := range t.Subtasks() {
			out.Subtasks = append(out.Subtasks, jsonSubtask{
				Name: s.Name().String(), Done: s.Done(), Added: s.Added(),
			})
		}
	}
	return out
}

func tasksJSON(list []task.Task) []jsonTask {
	out := make([]jsonTask, 0, len(list))
	for _, t := range list {
		out = append(out, taskJSON(t, false))
	}
	return out
}

func worktree(t task.Task) string {
	if w, ok := t.Worktree(); ok {
		return w
	}
	return ""
}

func names(list []user.Name) []string {
	if len(list) == 0 {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, n := range list {
		out = append(out, n.String())
	}
	return out
}

// emitJSON writes one document, indented so a human can read it too and
// newline-terminated so a shell prompt lands where it should.
func (a App) emitJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fault.IO{Op: "encode", Path: "standard output", Err: err}
	}
	return a.write(string(data) + "\n")
}
