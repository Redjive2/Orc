package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/fault"
)

// TestVerifyOnAHealthyStore. The common answer, and the one that has to be
// unambiguous: a checker whose "fine" looks like its "broken" gets ignored.
func TestVerifyClean(t *testing.T) {
	r := newRig(t)
	r.worktree(t, "internal/tree")
	r.ready("alice", "fix-the-parser", "one")

	got := r.ok("alice", "verify")
	for _, want := range []string{"verify", "no problems found", "tasks", "worktrees", "outbox", "deletions"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("verify should mention %q:\n%s", want, got.stdout)
		}
	}
	// The verdict is a word, not only a colour or a code.
	if !strings.Contains(got.stdout, "✓") {
		t.Errorf("the verdict should be visible:\n%s", got.stdout)
	}
}

func TestVerifyEmptyStore(t *testing.T) {
	r := newRig(t)
	got := r.ok("alice", "verify")
	if !strings.Contains(got.stdout, "no problems found") {
		t.Errorf("an empty store is not a broken one:\n%s", got.stdout)
	}
}

// damage runs a health check against a store somebody has broken, and returns
// what verify said.
func (r *rig) damaged(t *testing.T, break_ func()) result {
	t.Helper()
	break_()

	got := r.run("alice", "verify")
	if got.code != fault.CodeConflict {
		t.Fatalf("verify exited %d, want %d — a damaged store is a real failure\nstdout:\n%s\nstderr:\n%s",
			got.code, fault.CodeConflict, got.stdout, got.stderr)
	}
	return got
}

func (r *rig) storePath(parts ...string) string {
	return filepath.Join(append([]string{r.root, "store"}, parts...)...)
}

// TestVerifyReportsACorruptJournal: an unreadable line anywhere but the end is
// corruption, and the task will not load at all.
func TestVerifyCorruptJournal(t *testing.T) {
	r := newRig(t)
	r.worktree(t, "internal/tree")
	r.ready("alice", "fix-the-parser")

	got := r.damaged(t, func() {
		path := r.storePath("tasks", "fix-the-parser", "journal.jsonl")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append([]byte("{ not a line\n"), data...), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(got.stdout, "fix-the-parser: will not load") {
		t.Errorf("verify should name the task that will not load:\n%s", got.stdout)
	}
}

// An interrupted append is recovered rather than fatal — but a store collecting
// them is a store something keeps killing, so it is still reported.
func TestVerifyReportsAnInterruptedAppend(t *testing.T) {
	r := newRig(t)
	r.worktree(t, "internal/tree")
	r.ready("alice", "fix-the-parser")

	got := r.damaged(t, func() {
		path := r.storePath("tasks", "fix-the-parser", "journal.jsonl")
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(`{"version":1,"op":"claim"`); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(got.stdout, "interrupted write") {
		t.Errorf("verify should report the interrupted append:\n%s", got.stdout)
	}
	// And the task still loads, because the fold recovered it.
	if !strings.Contains(got.stdout, "1") {
		t.Errorf("the task should still be counted:\n%s", got.stdout)
	}
}

// A binding pointing at a task that is gone would make the hook enforce a scope
// nobody owns.
func TestVerifyReportsADanglingBinding(t *testing.T) {
	r := newRig(t)
	tree := r.worktree(t, "internal/tree")
	r.ready("alice", "fix-the-parser")
	r.ok("alice", "worktree", "fix-the-parser", tree)

	got := r.damaged(t, func() {
		if err := os.RemoveAll(r.storePath("tasks", "fix-the-parser")); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(got.stdout, "not a task in this store") {
		t.Errorf("verify should report the dangling binding:\n%s", got.stdout)
	}
}

func TestVerifyReportsADamagedBinding(t *testing.T) {
	r := newRig(t)
	tree := r.worktree(t, "internal/tree")
	r.ready("alice", "fix-the-parser")
	r.ok("alice", "worktree", "fix-the-parser", tree)

	got := r.damaged(t, func() {
		entries, err := os.ReadDir(r.storePath("worktrees"))
		if err != nil || len(entries) == 0 {
			t.Fatalf("no bindings to damage: %v", err)
		}
		path := filepath.Join(r.storePath("worktrees"), entries[0].Name())
		if err := os.WriteFile(path, []byte("{ broken"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(got.stdout, "will not decode") {
		t.Errorf("verify should name the damaged binding:\n%s", got.stdout)
	}
	// The task's own side is reported too, so a reader sees both halves rather
	// than guessing which is wrong. It says "will not read" rather than "not
	// filed": the binding is there, it just cannot be decoded, and the two are
	// different repairs.
	if !strings.Contains(got.stdout, "its worktree binding will not read") {
		t.Errorf("verify should report the task's side of it:\n%s", got.stdout)
	}
}

// A tombstone for a task that is still there is a delete that was interrupted
// after it was recorded — exactly what the log exists to make visible.
func TestVerifyReportsAnUnfinishedDelete(t *testing.T) {
	r := newRig(t)
	r.worktree(t, "internal/tree")
	r.ready("alice", "fix-the-parser")

	got := r.damaged(t, func() {
		line := `{"version":1,"task":"fix-the-parser","by":"alice","at":"2026-07-24T12:00:00.000Z"}` + "\n"
		if err := os.WriteFile(r.storePath("tombstones.jsonl"), []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(got.stdout, "the delete did not finish") {
		t.Errorf("verify should report the unfinished delete:\n%s", got.stdout)
	}
}

func TestVerifyReportsADamagedNotice(t *testing.T) {
	r := newRig(t)
	r.worktree(t, "internal/tree")
	r.ready("alice", "fix-the-parser")

	got := r.damaged(t, func() {
		dir := r.storePath("outbox")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "deadbeef.json"), []byte("{ nope"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(got.stdout, "will never be delivered") {
		t.Errorf("verify should report the undeliverable notice:\n%s", got.stdout)
	}
}

// A notice that has given up is not retried, so verify is the only thing that
// will ever mention it.
func TestVerifyReportsAStuckNotice(t *testing.T) {
	r := newRig(t)
	r.worktree(t, "internal/tree")
	r.ready("alice", "fix-the-parser")

	r.mail.breaks(errStuck{})
	r.ok("alice", "invite", "bob", "fix-the-parser")
	for range 12 {
		r.ok("alice", "pool")
	}

	got := r.run("alice", "verify")
	if got.code != fault.CodeConflict {
		t.Fatalf("verify exited %d with a stuck notice\n%s", got.code, got.stdout)
	}
	if !strings.Contains(got.stdout, "gave up after") || !strings.Contains(got.stdout, "bob") {
		t.Errorf("verify should name the stuck notice and its recipients:\n%s", got.stdout)
	}
	// The reason is kept on one line, so one broken notice cannot wreck the
	// shape of the report.
	if strings.Contains(got.stdout, "\n\n\n") {
		t.Errorf("the report lost its shape:\n%s", got.stdout)
	}
}

type errStuck struct{}

func (errStuck) Error() string { return "mailman refused\nwith a multi-line\r\nmessage" }

// TestVerifyChangesNothing. It reports; it never repairs. An automatic repair of
// damage nobody has understood is how one bad file becomes many.
func TestVerifyIsReadOnly(t *testing.T) {
	r := newRig(t)
	tree := r.worktree(t, "internal/tree")
	r.ready("alice", "fix-the-parser", "one")
	r.ok("alice", "worktree", "fix-the-parser", tree)

	// Break something, so verify has work to do and every chance to "fix" it.
	path := r.storePath("tasks", "fix-the-parser", "journal.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"version":1,"op":"cl`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	before := fingerprint(t, r.storePath())
	r.run("alice", "verify")
	if after := fingerprint(t, r.storePath()); after != before {
		t.Errorf("verify changed the store.\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func fingerprint(t *testing.T, root string) string {
	t.Helper()

	var b strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		b.WriteString(rel + " " + info.Mode().String() + " " + string(data) + "\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestVerifyTakesNoArguments(t *testing.T) {
	r := newRig(t)
	if got := r.run("alice", "verify", "everything"); got.code != fault.CodeUsage {
		t.Errorf("verify with an argument exited %d, want %d", got.code, fault.CodeUsage)
	}
}
