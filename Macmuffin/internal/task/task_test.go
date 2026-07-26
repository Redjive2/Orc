package task_test

import (
	"errors"
	"testing"
	"time"

	"orc/common/fault"
	"orc/common/user"
	"orc/macmuffin/internal/task"
)

var epoch = time.Date(2026, 7, 24, 18, 31, 4, 0, time.UTC)

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

func draft(t *testing.T) task.Task {
	t.Helper()
	p, err := task.NewPriority(4)
	if err != nil {
		t.Fatal(err)
	}
	d, err := task.NewDifficulty(3)
	if err != nil {
		t.Fatal(err)
	}
	got, err := task.NewDraft(name(t, "fix-the-parser"), agent(t, "alice"), p, d, epoch)
	if err != nil {
		t.Fatalf("NewDraft: %v", err)
	}
	return got
}

func TestNewDraft(t *testing.T) {
	got := draft(t)

	if got.Name().String() != "fix-the-parser" {
		t.Errorf("Name() = %q", got.Name())
	}
	if got.Author().String() != "alice" {
		t.Errorf("Author() = %q", got.Author())
	}
	if !got.Created().Equal(epoch) {
		t.Errorf("Created() = %s, want %s", got.Created(), epoch)
	}
	if got.Priority().Value() != 4 || got.Difficulty().Value() != 3 {
		t.Errorf("scores = %d/%d, want 4/3", got.Priority().Value(), got.Difficulty().Value())
	}

	// A newly created task is a private draft with nothing on it yet.
	if got.Pooled() || got.Life() != task.Draft {
		t.Errorf("a new task should be a draft, got %v", got.Life())
	}
	if _, owned := got.Owner(); owned {
		t.Error("a new task should be unowned")
	}
	if got.Status() != task.StatusUnset {
		t.Errorf("a new task should be unreported, got %v", got.Status())
	}
	if got.Scoped() {
		t.Error("a new task should have no scope")
	}
	if got.Completed() || got.Active() {
		t.Error("a new draft is neither completed nor on the board")
	}
	if _, bound := got.Worktree(); bound {
		t.Error("a new task should have no worktree")
	}

	done, total := got.Progress()
	if done != 0 || total != 0 {
		t.Errorf("Progress() = %d/%d, want 0/0", done, total)
	}
}

func TestNewDraftRejectsIncompleteInput(t *testing.T) {
	p, _ := task.NewPriority(1)
	d, _ := task.NewDifficulty(1)
	n := name(t, "x")
	a := agent(t, "alice")

	for _, tc := range []struct {
		name string
		call func() (task.Task, error)
	}{
		{"no name", func() (task.Task, error) { return task.NewDraft(task.Name{}, a, p, d, epoch) }},
		{"no author", func() (task.Task, error) { return task.NewDraft(n, user.Name{}, p, d, epoch) }},
		{"no priority", func() (task.Task, error) { return task.NewDraft(n, a, task.Score{}, d, epoch) }},
		{"no difficulty", func() (task.Task, error) { return task.NewDraft(n, a, p, task.Score{}, epoch) }},
		{"no creation time", func() (task.Task, error) { return task.NewDraft(n, a, p, d, time.Time{}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := tc.call(); !errors.Is(err, fault.ErrInternal) {
				t.Errorf("NewDraft = %q, %v; want an internal fault", got.Name(), err)
			}
		})
	}
}

// TestVisibilityIsTheOnlyRuleAboutExistence: a draft is its author's business
// alone, and anything pooled is every agent's.
func TestVisibility(t *testing.T) {
	d := draft(t)
	alice, bob := agent(t, "alice"), agent(t, "bob")

	if !d.Visible(alice) {
		t.Error("an author should see their own draft")
	}
	if d.Visible(bob) {
		t.Error("a draft should be invisible to everyone else")
	}
	if d.Visible(user.Name{}) {
		t.Error("a draft should be invisible to nobody in particular")
	}
}

func TestInvolves(t *testing.T) {
	d := draft(t)
	// An unowned draft involves nobody — not even its author, who has not
	// claimed it. Authorship is a separate fact from membership.
	if d.Involves(agent(t, "alice")) {
		t.Error("an unclaimed task should involve nobody")
	}
	if d.Involves(user.Name{}) {
		t.Error("the zero user should never be involved")
	}
}

func TestSubtask(t *testing.T) {
	s, err := task.NewSubtask(name(t, "fuzz-the-parser"), epoch)
	if err != nil {
		t.Fatalf("NewSubtask: %v", err)
	}
	if s.Done() {
		t.Error("a new subtask should be outstanding")
	}
	if got := s.Mark(); got != "○" {
		t.Errorf("Mark() = %q for an outstanding subtask", got)
	}

	done := s.Completed()
	if !done.Done() || done.Mark() != "✓" {
		t.Errorf("Completed() gave %v / %q", done.Done(), done.Mark())
	}
	// The original is untouched: a subtask is a value, and the state lives in
	// the journal that produced it.
	if s.Done() {
		t.Error("Completed mutated the subtask it was called on")
	}
	if !done.Added().Equal(epoch) || done.Name().String() != "fuzz-the-parser" {
		t.Error("completing a subtask changed something else about it")
	}

	if _, err := task.NewSubtask(task.Name{}, epoch); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("NewSubtask with no name = %v, want an internal fault", err)
	}
	if _, err := task.NewSubtask(name(t, "x"), time.Time{}); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("NewSubtask with no time = %v, want an internal fault", err)
	}
}

// TestMarksCarryStateWithoutColour: a checklist read through grep must still
// say which items are done.
func TestSubtaskMarksAreDistinct(t *testing.T) {
	s, err := task.NewSubtask(name(t, "x"), epoch)
	if err != nil {
		t.Fatal(err)
	}
	if s.Mark() == s.Completed().Mark() {
		t.Error("done and outstanding subtasks should not share a glyph")
	}
}

func TestLife(t *testing.T) {
	if got := task.Draft.String(); got != "draft" {
		t.Errorf("Draft.String() = %q", got)
	}
	if got := task.Pooled.String(); got != "pooled" {
		t.Errorf("Pooled.String() = %q", got)
	}
	if !task.Draft.Valid() || !task.Pooled.Valid() {
		t.Error("both lives should be valid")
	}
	if task.Life(9).Valid() {
		t.Error("Life(9) should not be valid")
	}
	if got := task.Life(9).String(); got == "draft" || got == "pooled" {
		t.Errorf("an undefined life rendered as %q", got)
	}
}

// TestAccessorsReturnCopies. A Task is immutable, and a caller that could reach
// into its slices would make it not so.
func TestAccessorsReturnCopies(t *testing.T) {
	d := draft(t)

	if scope := d.Scope(); len(scope) > 0 {
		scope[0] = "elsewhere"
		if d.Scope()[0] == "elsewhere" {
			t.Error("Scope() handed out the internal slice")
		}
	}
	if subs := d.Subtasks(); len(subs) > 0 {
		subs[0] = task.Subtask{}
		if d.Subtasks()[0].Name().Zero() {
			t.Error("Subtasks() handed out the internal slice")
		}
	}
	if cols := d.Collaborators(); len(cols) > 0 {
		cols[0] = user.Name{}
		if d.Collaborators()[0].Zero() {
			t.Error("Collaborators() handed out the internal slice")
		}
	}
	// The empty case must not panic, which is what the guards above check.
	if len(d.Scope()) != 0 || len(d.Subtasks()) != 0 || len(d.Collaborators()) != 0 {
		t.Error("a fresh draft should have nothing on it")
	}
}

// TestUnfinishedIsWhatCompleteWouldList.
func TestUnfinished(t *testing.T) {
	d := draft(t)
	if got := d.Unfinished(); len(got) != 0 {
		t.Errorf("a task with no subtasks has %d unfinished", len(got))
	}
}
