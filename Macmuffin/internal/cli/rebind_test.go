package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/fault"
)

// `muff rebind` exists for one failure: a directory moves, the bindings under it
// address nowhere, and the hook goes quiet without saying so. So the tests are about
// the quiet parts — that a binding follows, that one which cannot is *named* rather
// than dropped, and that the command fails when enforcement did not survive.

// tree makes a directory that looks like a git working tree, without touching the
// rig's cwd — a rebind test needs two of them at once.
func tree(t *testing.T, at string, dirs ...string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(at, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(at, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	resolved, err := filepath.EvalSymlinks(at)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// bound is a rig with one owned, scoped, bound task.
func bound(t *testing.T) (*rig, string) {
	t.Helper()
	r := newRig(t)
	root := r.worktree(t, "internal/tree")
	r.ok("alice", "create", "fix-the-parser", "3", "3")
	r.ok("alice", "claim", "fix-the-parser")
	r.ok("alice", "scope", "fix-the-parser", "internal/tree")
	r.ok("alice", "worktree", "fix-the-parser", root)
	return r, root
}

// boundAt asks the question the hook asks: is anything bound to this directory?
// `rebind --dry-run` answers it without changing anything, which is cheaper than a
// second door into the store and exercises the lookup the hook actually uses.
func boundAt(t *testing.T, r *rig, dir string) bool {
	t.Helper()
	got := r.ok("alice", "rebind", "--dry-run", dir, filepath.Join(t.TempDir(), "elsewhere"))
	return !strings.Contains(got.stdout, "no task is bound")
}

func TestRebindFollowsAMovedWorktree(t *testing.T) {
	r, old := bound(t)
	moved := tree(t, filepath.Join(t.TempDir(), "moved"), "internal/tree")

	got := r.ok("alice", "rebind", old, moved)
	if !strings.Contains(got.stdout, moved) {
		t.Errorf("it does not say where the binding went:\n%s", got.stdout)
	}

	// The binding is looked up by directory, so the test asks the same question the
	// hook does: the new directory answers, and the old one no longer does.
	if !boundAt(t, r, moved) {
		t.Error("nothing is bound to the new directory")
	}
	if again := r.ok("alice", "rebind", old, moved); !strings.Contains(again.stdout, "no task is bound") {
		t.Errorf("the old binding survived the move:\n%s", again.stdout)
	}
}

// A binding under the directory, not just one exactly at it: an agent's workspace
// can hold several worktrees, and a move takes all of them.
func TestRebindFollowsWhatIsUnderneath(t *testing.T) {
	r := newRig(t)
	holder := t.TempDir()
	inner := tree(t, filepath.Join(holder, "parser"), "internal/tree")
	r.cwd = inner
	r.ok("alice", "create", "fix-the-parser", "3", "3")
	r.ok("alice", "claim", "fix-the-parser")
	r.ok("alice", "scope", "fix-the-parser", "internal/tree")
	r.ok("alice", "worktree", "fix-the-parser", inner)

	to := t.TempDir()
	tree(t, filepath.Join(to, "parser"), "internal/tree")

	got := r.ok("alice", "rebind", holder, to)
	if !strings.Contains(got.stdout, filepath.Join(to, "parser")) {
		t.Errorf("a binding one level down did not follow:\n%s", got.stdout)
	}
}

// A neighbour is not underneath. `/a/bc` is not under `/a/b`, and a prefix
// comparison that thought otherwise would rewrite a binding belonging to another
// tree — pointing it at a directory that does not exist.
func TestRebindLeavesNeighboursAlone(t *testing.T) {
	r := newRig(t)
	base := t.TempDir()
	mine := tree(t, filepath.Join(base, "parser"), "internal/tree")
	r.cwd = mine
	r.ok("alice", "create", "fix-the-parser", "3", "3")
	r.ok("alice", "claim", "fix-the-parser")
	r.ok("alice", "scope", "fix-the-parser", "internal/tree")
	r.ok("alice", "worktree", "fix-the-parser", mine)

	got := r.ok("alice", "rebind", filepath.Join(base, "pars"), filepath.Join(base, "elsewhere"))
	if !strings.Contains(got.stdout, "no task is bound") {
		t.Errorf("a neighbouring directory was treated as a parent:\n%s", got.stdout)
	}
}

// TestABindingThatCannotFollowIsNamedNotDropped. This is the whole reason the
// command reports rather than just doing: a task whose binding did not survive is a
// task with no scope enforcement anywhere, and it has to be said out loud.
func TestRebindNamesWhatItCouldNotMove(t *testing.T) {
	r, old := bound(t)
	// Nothing is at the destination, so it is not a worktree and the binding
	// cannot follow.
	nowhere := filepath.Join(t.TempDir(), "gone")

	got := r.run("alice", "rebind", old, nowhere)
	if got.code != fault.CodeConflict {
		t.Fatalf("exited %d, want %d\n%s%s", got.code, fault.CodeConflict, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stdout, "fix-the-parser") {
		t.Errorf("the stranded task is not named:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "no longer enforced") {
		t.Errorf("it does not say what was lost:\n%s", got.stdout)
	}
	// And the command that puts it back, spelled out.
	if !strings.Contains(got.stdout, "muff worktree fix-the-parser ") || !strings.Contains(got.stdout, "gone") {
		t.Errorf("it does not say how to restore it:\n%s", got.stdout)
	}

	// The old binding is still there: a rebind that could not complete has not
	// turned enforcement off in the meantime.
	if again := r.run("alice", "rebind", old, nowhere); !strings.Contains(again.stdout, "fix-the-parser") {
		t.Errorf("the binding was dropped when it could not be moved:\n%s", again.stdout)
	}
}

// --dry-run is what somebody runs before a migration, so it must change nothing.
func TestRebindDryRun(t *testing.T) {
	r, old := bound(t)
	moved := tree(t, filepath.Join(t.TempDir(), "moved"), "internal/tree")

	got := r.ok("alice", "rebind", "--dry-run", old, moved)
	if !strings.Contains(got.stdout, "nothing has changed") {
		t.Errorf("a dry run does not say so:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, moved) {
		t.Errorf("a dry run does not say where things would go:\n%s", got.stdout)
	}

	if !boundAt(t, r, old) {
		t.Error("a dry run moved the binding")
	}
}

// Two spellings of one directory are not a move. On macOS every path through /tmp
// has two, and rewriting every binding to an identical path is work that can only
// go wrong.
func TestRebindOntoItselfIsANoOp(t *testing.T) {
	r, old := bound(t)

	got := r.ok("alice", "rebind", old, old)
	if !strings.Contains(got.stdout, "same directory") {
		t.Errorf("rebinding a directory onto itself:\n%s", got.stdout)
	}
}

func TestRebindRefusals(t *testing.T) {
	r, old := bound(t)

	for _, tc := range []struct {
		what string
		args []string
		code int
	}{
		{"no arguments", []string{"rebind"}, fault.CodeUsage},
		{"only one", []string{"rebind", old}, fault.CodeUsage},
		{"three", []string{"rebind", old, old, old}, fault.CodeUsage},
	} {
		if got := r.run("alice", tc.args...); got.code != tc.code {
			t.Errorf("%s exited %d, want %d\n%s", tc.what, got.code, tc.code, got.stderr)
		}
	}
}

// Binding is owner-only, and so is following one: rebinding is that operation aimed
// somewhere else, and an agent who may not bind a task may not silently move where
// its scope is enforced either.
func TestRebindNeedsTheSameAuthorityAsBinding(t *testing.T) {
	r, old := bound(t)
	moved := tree(t, filepath.Join(t.TempDir(), "moved"), "internal/tree")

	got := r.run("bob", "rebind", old, moved)
	if got.code != fault.CodeConflict {
		t.Fatalf("a stranger's rebind exited %d, want %d\n%s%s", got.code, fault.CodeConflict, got.stdout, got.stderr)
	}
	// It reports the refusal rather than swallowing it, and the binding stands.
	if !strings.Contains(got.stdout, "fix-the-parser") {
		t.Errorf("the refused task is not named:\n%s", got.stdout)
	}
	if !boundAt(t, r, old) {
		t.Error("a refused rebind moved the binding anyway")
	}
}
