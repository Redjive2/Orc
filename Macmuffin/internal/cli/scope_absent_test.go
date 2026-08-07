package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A scope names paths, and nothing here can know which tree they will be measured
// against — the agent who runs the task may be working somewhere else entirely.
//
// Reported from a live fleet: an architect with a freshly provisioned workspace
// wrote three tasks scoped to a repository it was not standing in. Every path was
// correct and none of them was present, and `muff scope` said nothing. The scopes
// only mean something once the owner has a workspace at that repository, and
// nobody was told.
//
// A refusal would have been worse than silence: it would have stopped the tasks
// being written at all, which is the work. So this cautions.

func scoped(t *testing.T, r *rig, paths ...string) result {
	t.Helper()
	r.run("boss", "create", "job", "2", "2")
	return r.run("boss", append([]string{"scope", "job"}, paths...)...)
}

func TestAScopeThatIsNotHereIsSaidSoAndStillAccepted(t *testing.T) {
	r := newRig(t)
	r.cwd = t.TempDir()

	got := scoped(t, r, "Docs/Orc/Reference.md")
	if got.code != 0 {
		t.Fatalf("a scope naming an absent path was refused (%d): %s", got.code, got.stderr)
	}
	if !strings.Contains(got.stderr, "Docs/Orc/Reference.md") {
		t.Errorf("nothing was said about a path that is not here:\n%s", got.stderr)
	}
	if !strings.Contains(got.stderr, r.cwd) {
		t.Errorf("the caution does not say which directory it was measured in:\n%s", got.stderr)
	}
	// The scope is still recorded — that is the whole point of cautioning rather
	// than refusing.
	if !strings.Contains(got.stdout, "Docs/Orc/Reference.md") {
		t.Errorf("the scope was not set:\n%s", got.stdout)
	}
}

func TestAScopeThatIsHereSaysNothing(t *testing.T) {
	// A caution on every ordinary scope would be a caution nobody reads.
	r := newRig(t)
	r.cwd = t.TempDir()
	if err := os.MkdirAll(filepath.Join(r.cwd, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.cwd, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := scoped(t, r, "internal", "main.go")
	if strings.Contains(got.stderr, "not in") {
		t.Errorf("a scope that is entirely present was cautioned about:\n%s", got.stderr)
	}
}

func TestOnlyTheAbsentEntriesAreNamed(t *testing.T) {
	// A caution listing paths that are there sends somebody to check the wrong
	// ones.
	r := newRig(t)
	r.cwd = t.TempDir()
	if err := os.WriteFile(filepath.Join(r.cwd, "here.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := scoped(t, r, "here.go", "gone.go")
	if !strings.Contains(got.stderr, "gone.go") {
		t.Errorf("the absent path was not named:\n%s", got.stderr)
	}
	if strings.Contains(got.stderr, "here.go") {
		t.Errorf("a path that is present was named as missing:\n%s", got.stderr)
	}
}
