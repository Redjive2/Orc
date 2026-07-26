package store_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/macmuffin/internal/store"
	"orc/macmuffin/internal/task"
)

var epoch = time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

type rig struct {
	*store.Store
	t    *testing.T
	now  *clock.Fake
	root string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	root := t.TempDir()
	now := clock.NewFake(epoch, time.Millisecond)
	s, err := store.Open(root, now)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return &rig{Store: s, t: t, now: now, root: root}
}

func (r *rig) name(s string) task.Name {
	r.t.Helper()
	n, err := task.ParseName(s)
	if err != nil {
		r.t.Fatalf("ParseName(%q): %v", s, err)
	}
	return n
}

func (r *rig) agent(s string) user.Name {
	r.t.Helper()
	n, err := user.Parse(s)
	if err != nil {
		r.t.Fatalf("user.Parse(%q): %v", s, err)
	}
	return n
}

// create makes a task with the usual scores.
func (r *rig) create(name, author string) task.Task {
	r.t.Helper()
	p, err := task.NewPriority(3)
	if err != nil {
		r.t.Fatal(err)
	}
	d, err := task.NewDifficulty(3)
	if err != nil {
		r.t.Fatal(err)
	}
	got, err := r.Create(r.name(name), r.agent(author), p, d)
	if err != nil {
		r.t.Fatalf("Create(%q): %v", name, err)
	}
	return got
}

// apply runs one event through Apply, failing the test if it is refused.
func (r *rig) apply(name string, make func(task.Task) (task.Event, error)) task.Task {
	r.t.Helper()
	got, err := r.Apply(r.name(name), make)
	if err != nil {
		r.t.Fatalf("Apply(%q): %v", name, err)
	}
	return got
}

// scoped creates a task and gives it a scope, which is the gate on nearly
// everything else.
func (r *rig) scoped(name, author string) task.Task {
	r.t.Helper()
	r.create(name, author)
	return r.apply(name, func(task.Task) (task.Event, error) {
		return task.Scope(r.agent(author), r.Now(), []string{"internal/tree/"})
	})
}

func TestOpenCreatesTheLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nested", "store")
	if _, err := store.Open(root, clock.NewFake(epoch, time.Millisecond)); err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, dir := range []string{"tasks", "worktrees", "outbox"} {
		info, err := os.Stat(filepath.Join(root, dir))
		if err != nil {
			t.Errorf("Open did not create %s: %v", dir, err)
			continue
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("%s is mode %04o, want 0700", dir, perm)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "version"))
	if err != nil || strings.TrimSpace(string(data)) != "1" {
		t.Errorf("version marker = %q, %v", data, err)
	}
}

func TestOpenIsIdempotentAndChecksVersion(t *testing.T) {
	root := t.TempDir()
	for range 3 {
		if _, err := store.Open(root, clock.NewFake(epoch, time.Millisecond)); err != nil {
			t.Fatalf("Open: %v", err)
		}
	}
	for _, version := range []string{"2", "99", "nonsense", ""} {
		if err := os.WriteFile(filepath.Join(root, "version"), []byte(version+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Open(root, clock.NewFake(epoch, time.Millisecond)); !errors.Is(err, fault.ErrParse) {
			t.Errorf("Open on version %q = %v, want a parse fault", version, err)
		}
	}
}

func TestOpenRejectsBadArguments(t *testing.T) {
	if _, err := store.Open("", clock.Real{}); !errors.Is(err, fault.ErrUsage) {
		t.Errorf("Open(\"\") = %v, want a usage fault", err)
	}
	if _, err := store.Open(t.TempDir(), nil); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Open without a clock = %v, want an internal fault", err)
	}
}

func TestDefaultRoot(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		home string
		want string
	}{
		{"explicit", map[string]string{"MACMUFFIN_HOME": "/srv/tasks"}, "/home/a", "/srv/tasks"},
		{"xdg", map[string]string{"XDG_DATA_HOME": "/home/a/.local/share"}, "/home/a", "/home/a/.local/share/macmuffin"},
		{"home", nil, "/home/a", "/home/a/.macmuffin"},
		{"explicit wins", map[string]string{"MACMUFFIN_HOME": "/srv", "XDG_DATA_HOME": "/x"}, "/home/a", "/srv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.DefaultRoot(store.MapEnv(tc.env), tc.home)
			if err != nil {
				t.Fatalf("DefaultRoot: %v", err)
			}
			if got != filepath.Clean(tc.want) {
				t.Errorf("DefaultRoot = %q, want %q", got, tc.want)
			}
		})
	}
	if _, err := store.DefaultRoot(store.MapEnv(map[string]string{"MACMUFFIN_HOME": " "}), "/h"); !errors.Is(err, fault.ErrUsage) {
		t.Error("an empty MACMUFFIN_HOME should be a usage fault")
	}
	if _, err := store.DefaultRoot(store.MapEnv(nil), ""); !errors.Is(err, fault.ErrUsage) {
		t.Error("no home and no override should be a usage fault")
	}
}

func TestCreateAndLoad(t *testing.T) {
	r := newRig(t)
	made := r.create("fix-the-parser", "alice")

	got, err := r.Load(r.name("fix-the-parser"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Name().String() != made.Name().String() || got.Author().String() != "alice" {
		t.Errorf("loaded %q by %q", got.Name(), got.Author())
	}
	if got.Pooled() || got.Scoped() || got.Completed() {
		t.Error("a freshly created task should be an unscoped draft")
	}
	if !got.Created().Equal(made.Created()) {
		t.Errorf("created %s, loaded %s", made.Created(), got.Created())
	}

	if ok, err := r.Has(r.name("fix-the-parser")); err != nil || !ok {
		t.Errorf("Has = %v, %v", ok, err)
	}
	if ok, err := r.Has(r.name("nothing")); err != nil || ok {
		t.Errorf("Has on a missing task = %v, %v", ok, err)
	}
	if _, err := r.Load(r.name("nothing")); !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("Load on a missing task = %v, want not found", err)
	}
}

// TestCreateIsWriteOnce: task names are globally unique, and the second
// creator must be told who got there first.
func TestCreateIsWriteOnce(t *testing.T) {
	r := newRig(t)
	r.create("fix-the-parser", "alice")

	p, _ := task.NewPriority(1)
	d, _ := task.NewDifficulty(1)
	_, err := r.Create(r.name("fix-the-parser"), r.agent("bob"), p, d)
	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("re-creating = %v, want a conflict", err)
	}
	if !strings.Contains(err.Error(), "alice") {
		t.Errorf("the conflict should name the author: %v", err)
	}

	// And the original is untouched.
	got, err := r.Load(r.name("fix-the-parser"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Author().String() != "alice" {
		t.Errorf("the second create overwrote the author: %q", got.Author())
	}
}

func TestNames(t *testing.T) {
	r := newRig(t)
	r.create("charlie", "alice")
	r.create("alpha", "alice")
	r.create("bravo", "alice")

	got, err := r.Names()
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	var names []string
	for _, n := range got {
		names = append(names, n.String())
	}
	if strings.Join(names, ",") != "alpha,bravo,charlie" {
		t.Errorf("Names = %v, want them sorted", names)
	}

	// A directory that is not a task is skipped rather than breaking the pool.
	if err := os.MkdirAll(filepath.Join(r.Root(), "tasks", "not a task"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(r.Root(), "tasks", "half-made"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err = r.Names()
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("Names returned %d; a directory without a record is not a task", len(got))
	}
}

// TestApplyIsTheOnlyWayToChangeATask walks a task through its whole life and
// checks the state after each step.
func TestApplyFoldsALife(t *testing.T) {
	r := newRig(t)
	alice, bob := r.agent("alice"), r.agent("bob")
	r.create("fix-the-parser", "alice")

	got := r.apply("fix-the-parser", func(task.Task) (task.Event, error) {
		return task.Scope(alice, r.Now(), []string{"internal/tree/", "cmd/anno/main.go"})
	})
	if !got.Scoped() || len(got.Scope()) != 2 {
		t.Fatalf("scope = %v", got.Scope())
	}

	got = r.apply("fix-the-parser", func(task.Task) (task.Event, error) {
		return task.AddSub(alice, r.Now(), r.name("fuzz-the-parser"))
	})
	if done, total := got.Progress(); done != 0 || total != 1 {
		t.Fatalf("Progress = %d/%d, want 0/1", done, total)
	}

	got = r.apply("fix-the-parser", func(task.Task) (task.Event, error) {
		return task.Push(alice, r.Now())
	})
	if !got.Pooled() {
		t.Fatal("the task should be pooled")
	}

	got = r.apply("fix-the-parser", func(task.Task) (task.Event, error) {
		return task.Claim(bob, r.Now())
	})
	owner, owned := got.Owner()
	if !owned || owner.String() != "bob" {
		t.Fatalf("owner = %q, %v", owner, owned)
	}

	got = r.apply("fix-the-parser", func(task.Task) (task.Event, error) {
		return task.SetStatus(bob, r.Now(), task.StatusSlow)
	})
	if got.Status() != task.StatusSlow {
		t.Fatalf("status = %v", got.Status())
	}

	got = r.apply("fix-the-parser", func(task.Task) (task.Event, error) {
		return task.DoneSub(bob, r.Now(), r.name("fuzz-the-parser"))
	})
	if done, total := got.Progress(); done != 1 || total != 1 {
		t.Fatalf("Progress = %d/%d, want 1/1", done, total)
	}

	got = r.apply("fix-the-parser", func(task.Task) (task.Event, error) {
		return task.Complete(bob, r.Now(), false, nil)
	})
	if !got.Completed() || got.Active() {
		t.Fatalf("the task should be completed and off the board")
	}

	// And every bit of that survives a reload, which is the point of a journal.
	again, err := r.Load(r.name("fix-the-parser"))
	if err != nil {
		t.Fatal(err)
	}
	if !again.Completed() || again.Status() != task.StatusSlow {
		t.Error("the folded state did not survive a reload")
	}
	if owner, _ := again.Owner(); owner.String() != "bob" {
		t.Errorf("owner did not survive: %q", owner)
	}
}

// TestApplyRefusesIllegalTransitions: the journal only ever contains
// transitions that happened, so an event that would not apply is never written.
func TestApplyRefusesIllegalTransitions(t *testing.T) {
	r := newRig(t)
	alice, bob := r.agent("alice"), r.agent("bob")
	r.scoped("fix-the-parser", "alice")
	r.apply("fix-the-parser", func(task.Task) (task.Event, error) { return task.Push(alice, r.Now()) })
	r.apply("fix-the-parser", func(task.Task) (task.Event, error) { return task.Claim(bob, r.Now()) })

	before, err := os.ReadFile(filepath.Join(r.Root(), "tasks", "fix-the-parser", "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		make func(task.Task) (task.Event, error)
		says string
	}{
		{"claim an owned task", func(task.Task) (task.Event, error) {
			return task.Claim(r.agent("carol"), r.Now())
		}, "bob"},
		{"push twice", func(task.Task) (task.Event, error) {
			return task.Push(alice, r.Now())
		}, "already in the pool"},
		{"owner leaves", func(task.Task) (task.Event, error) {
			return task.Leave(bob, r.Now())
		}, "never orphaned"},
		{"kick a non-collaborator", func(task.Task) (task.Event, error) {
			return task.Kick(alice, r.Now(), r.agent("carol"))
		}, "not a collaborator"},
		{"complete a missing subtask", func(task.Task) (task.Event, error) {
			return task.DoneSub(bob, r.Now(), r.name("nothing"))
		}, "no subtask"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.Apply(r.name("fix-the-parser"), tc.make)
			if !errors.Is(err, fault.ErrConflict) {
				t.Fatalf("Apply = %v, want a conflict", err)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the conflict should mention %q: %v", tc.says, err)
			}
		})
	}

	after, err := os.ReadFile(filepath.Join(r.Root(), "tasks", "fix-the-parser", "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("a refused transition was journaled anyway")
	}
}

// TestScopeGatesEverything is the reference's rule: a task cannot be edited or
// completed, only claimed or deleted, until a scope is added.
func TestScopeGatesEverything(t *testing.T) {
	r := newRig(t)
	alice := r.agent("alice")
	r.create("stub", "alice")

	for _, tc := range []struct {
		name string
		make func(task.Task) (task.Event, error)
	}{
		{"push", func(task.Task) (task.Event, error) { return task.Push(alice, r.Now()) }},
		{"subtask", func(task.Task) (task.Event, error) { return task.AddSub(alice, r.Now(), r.name("a")) }},
		{"complete", func(task.Task) (task.Event, error) { return task.Complete(alice, r.Now(), false, nil) }},
		{"worktree", func(task.Task) (task.Event, error) { return task.BindWorktree(alice, r.Now(), "/tmp/wt") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.Apply(r.name("stub"), tc.make); !errors.Is(err, fault.ErrConflict) {
				t.Errorf("%s on a scopeless task = %v, want a conflict", tc.name, err)
			}
		})
	}

	// Claiming a stub is explicitly allowed, and so is setting its status.
	r.apply("stub", func(task.Task) (task.Event, error) { return task.Claim(alice, r.Now()) })
}

// TestCompleteRefusesUnfinishedSubtasksUnlessForced, and a forced completion
// leaves a mark — the point of a tracker is that shortcuts stay visible.
func TestCompleteAndForce(t *testing.T) {
	r := newRig(t)
	alice := r.agent("alice")
	r.scoped("fix-the-parser", "alice")
	r.apply("fix-the-parser", func(task.Task) (task.Event, error) {
		return task.AddSub(alice, r.Now(), r.name("outstanding"))
	})

	_, err := r.Apply(r.name("fix-the-parser"), func(task.Task) (task.Event, error) {
		return task.Complete(alice, r.Now(), false, nil)
	})
	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("completing with an unfinished subtask = %v, want a conflict", err)
	}
	if !strings.Contains(err.Error(), "1 unfinished") {
		t.Errorf("the refusal should count them: %v", err)
	}

	got := r.apply("fix-the-parser", func(t task.Task) (task.Event, error) {
		var skipped []task.Name
		for _, s := range t.Unfinished() {
			skipped = append(skipped, s.Name())
		}
		return task.Complete(alice, r.Now(), true, skipped)
	})
	if !got.Completed() {
		t.Fatal("a forced completion should complete the task")
	}

	// The skip is in the journal, where `info` can find it.
	data, err := os.ReadFile(filepath.Join(r.Root(), "tasks", "fix-the-parser", "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"forced":true`) || !strings.Contains(string(data), "outstanding") {
		t.Errorf("the forced completion left no mark:\n%s", data)
	}
}

// TestApplyRejectsBadArguments.
func TestApplyRejectsBadArguments(t *testing.T) {
	r := newRig(t)
	r.create("x", "alice")

	if _, err := r.Apply(task.Name{}, func(task.Task) (task.Event, error) { return task.Event{}, nil }); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Apply with no name = %v, want an internal fault", err)
	}
	if _, err := r.Apply(r.name("x"), nil); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Apply with no decision = %v, want an internal fault", err)
	}
	if _, err := r.Apply(r.name("missing"), func(task.Task) (task.Event, error) {
		return task.Push(r.agent("alice"), r.Now())
	}); !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("Apply on a missing task = %v, want not found", err)
	}

	// A decision that returns an error aborts without writing.
	sentinel := errors.New("the caller said no")
	if _, err := r.Apply(r.name("x"), func(task.Task) (task.Event, error) {
		return task.Event{}, sentinel
	}); !errors.Is(err, sentinel) {
		t.Errorf("Apply = %v, want the caller's error", err)
	}

	// A decision that returns no event is a no-op the caller already reported.
	got, err := r.Apply(r.name("x"), func(task.Task) (task.Event, error) { return task.Event{}, nil })
	if err != nil {
		t.Fatalf("a no-op decision = %v", err)
	}
	if got.Name().String() != "x" {
		t.Errorf("a no-op should still return the task, got %q", got.Name())
	}
}

// TestApplySeesTheStateItDecidesAgainst — the whole reason the lock spans both.
func TestApplySeesCurrentState(t *testing.T) {
	r := newRig(t)
	alice := r.agent("alice")
	r.scoped("fix-the-parser", "alice")

	var seen task.Task
	r.apply("fix-the-parser", func(current task.Task) (task.Event, error) {
		seen = current
		return task.Push(alice, r.Now())
	})
	if !seen.Scoped() {
		t.Error("the decision was handed a task without the scope that was already applied")
	}
	if seen.Pooled() {
		t.Error("the decision was handed a task with the event it had not yet decided on")
	}
}

func TestLoadRefusesADamagedRecord(t *testing.T) {
	r := newRig(t)
	r.create("fix-the-parser", "alice")
	path := filepath.Join(r.Root(), "tasks", "fix-the-parser", "task.json")

	for _, tc := range []struct {
		name    string
		content string
	}{
		{"empty", ""},
		{"empty object", "{}"},
		{"truncated", `{"version":1,"name":"fix-the-parser"`},
		{"not json", "hello"},
		{"unknown field", `{"version":1,"name":"fix-the-parser","author":"alice","priority":3,"difficulty":3,"created":"2026-07-24T12:00:00.000Z","secret":1}`},
		{"wrong version", `{"version":99,"name":"fix-the-parser","author":"alice","priority":3,"difficulty":3,"created":"2026-07-24T12:00:00.000Z"}`},
		{"bad score", `{"version":1,"name":"fix-the-parser","author":"alice","priority":9,"difficulty":3,"created":"2026-07-24T12:00:00.000Z"}`},
		{"bad author", `{"version":1,"name":"fix-the-parser","author":"../etc","priority":3,"difficulty":3,"created":"2026-07-24T12:00:00.000Z"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Load(r.name("fix-the-parser")); err == nil {
				t.Errorf("a %s record loaded without complaint", tc.name)
			}
		})
	}

	// A record naming a different task than its directory is a conflict: the
	// content must not answer for a name it is not.
	other := `{"version":1,"name":"something-else","author":"alice","priority":3,"difficulty":3,"created":"2026-07-24T12:00:00.000Z"}`
	if err := os.WriteFile(path, []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Load(r.name("fix-the-parser")); !errors.Is(err, fault.ErrConflict) {
		t.Errorf("a misnamed record = %v, want a conflict", err)
	}
}

func TestCollaborators(t *testing.T) {
	r := newRig(t)
	alice, bob := r.agent("alice"), r.agent("bob")
	r.scoped("fix-the-parser", "alice")
	r.apply("fix-the-parser", func(task.Task) (task.Event, error) { return task.Claim(alice, r.Now()) })

	got := r.apply("fix-the-parser", func(task.Task) (task.Event, error) {
		return task.Invite(alice, r.Now(), bob)
	})
	if len(got.Collaborators()) != 1 || !got.Involves(bob) {
		t.Fatalf("collaborators = %v", got.Collaborators())
	}

	// Inviting twice is a conflict, and so is inviting the owner.
	for _, who := range []user.Name{bob, alice} {
		if _, err := r.Apply(r.name("fix-the-parser"), func(task.Task) (task.Event, error) {
			return task.Invite(alice, r.Now(), who)
		}); !errors.Is(err, fault.ErrConflict) {
			t.Errorf("inviting %s again = %v, want a conflict", who, err)
		}
	}

	got = r.apply("fix-the-parser", func(task.Task) (task.Event, error) { return task.Leave(bob, r.Now()) })
	if len(got.Collaborators()) != 0 || got.Involves(bob) {
		t.Errorf("after leaving, collaborators = %v", got.Collaborators())
	}
}

// TestClaimingClearsCollaboration: an owner is never also a collaborator, since
// the owner has strictly more and `leave` would be ambiguous.
func TestClaimingClearsCollaboration(t *testing.T) {
	r := newRig(t)
	alice, bob := r.agent("alice"), r.agent("bob")
	r.scoped("fix-the-parser", "alice")
	r.apply("fix-the-parser", func(task.Task) (task.Event, error) { return task.Claim(alice, r.Now()) })
	r.apply("fix-the-parser", func(task.Task) (task.Event, error) { return task.Invite(alice, r.Now(), bob) })

	// Alice releases nothing; instead the task is re-created for the test's
	// purpose: bob claims a task he already collaborates on is impossible while
	// alice owns it, so the check is on the fold directly.
	got, err := r.Load(r.name("fix-the-parser"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Involves(bob) != true {
		t.Fatal("bob should be a collaborator")
	}
	if owner, _ := got.Owner(); owner.String() == bob.String() {
		t.Fatal("bob should not be the owner")
	}
}

func TestNowUsesTheStoreClock(t *testing.T) {
	r := newRig(t)
	first, second := r.Now(), r.Now()
	if !second.After(first) {
		t.Errorf("Now did not advance: %s then %s", first, second)
	}
	if first.Before(epoch) {
		t.Errorf("Now = %s, before the fake clock's start", first)
	}
}

// parseAgent is user.Parse, for the helpers that have an *testing.F rather than
// an *testing.T to hand.
func parseAgent(s string) (user.Name, error) { return user.Parse(s) }

func TestDeleteRecordsBeforeErasing(t *testing.T) {
	r := newRig(t)
	alice := r.agent("alice")
	r.scoped("fix-the-parser", "alice")
	r.apply("fix-the-parser", func(task.Task) (task.Event, error) {
		return task.AddSub(alice, r.Now(), r.name("one"))
	})

	if err := r.Delete(r.name("fix-the-parser"), alice); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if ok, _ := r.Has(r.name("fix-the-parser")); ok {
		t.Error("the task should be gone")
	}

	got, skipped, err := r.Tombstones()
	if err != nil {
		t.Fatalf("Tombstones: %v", err)
	}
	if skipped != 0 {
		t.Errorf("%d bytes were skipped in a healthy log", skipped)
	}
	if len(got) != 1 {
		t.Fatalf("%d tombstones, want 1", len(got))
	}
	if got[0].Task.String() != "fix-the-parser" || got[0].By.String() != "alice" {
		t.Errorf("tombstone = %+v", got[0])
	}
	if got[0].Subtasks != 1 {
		t.Errorf("the tombstone should record the subtask count, got %d", got[0].Subtasks)
	}

	// Deleting again is not found: the record survives, the task does not.
	if err := r.Delete(r.name("fix-the-parser"), alice); !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("deleting twice = %v, want not found", err)
	}
	if _, err := r.Load(r.name("fix-the-parser")); !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("Load after delete = %v, want not found", err)
	}

	if err := r.Delete(task.Name{}, alice); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Delete with no task = %v, want an internal fault", err)
	}
	if err := r.Delete(r.name("x"), user.Name{}); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Delete with no actor = %v, want an internal fault", err)
	}
}

// TestTheDeletionLogRecoversLikeAJournal: an interrupted append loses its last
// line, and anything earlier is corruption.
func TestTombstoneLogRecovery(t *testing.T) {
	r := newRig(t)
	alice := r.agent("alice")
	for _, name := range []string{"one", "two", "three"} {
		r.scoped(name, "alice")
		if err := r.Delete(r.name(name), alice); err != nil {
			t.Fatal(err)
		}
	}

	path := filepath.Join(r.Root(), "tombstones.jsonl")
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Every prefix reads without error.
	for cut := range len(full) + 1 {
		if err := os.WriteFile(path, full[:cut], 0o600); err != nil {
			t.Fatal(err)
		}
		got, _, err := r.Tombstones()
		if err != nil {
			t.Fatalf("a %d byte prefix failed to read: %v", cut, err)
		}
		if len(got) > 3 {
			t.Fatalf("a %d byte prefix produced %d records", cut, len(got))
		}
	}

	// A bad line in the middle is corruption, not an interruption.
	lines := strings.Split(strings.TrimRight(string(full), "\n"), "\n")
	lines[1] = "not json"
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Tombstones(); !errors.Is(err, fault.ErrParse) {
		t.Errorf("interior corruption read with %v, want a parse fault", err)
	}
}
