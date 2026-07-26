package repo_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"orc/common/fault"
	"orc/macmuffin/internal/repo"
)

// mainTree makes a directory that looks like a main working tree.
func mainTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// linkedTree makes a linked worktree pointing into main's repository.
func linkedTree(t *testing.T, main, name string) string {
	t.Helper()
	root := t.TempDir()
	gitDir := filepath.Join(main, ".git", "worktrees", name)
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestFindAMainWorkingTree(t *testing.T) {
	root := mainTree(t)
	deep := filepath.Join(root, "internal", "tree")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	// Found from the root and from anywhere beneath it.
	for _, from := range []string{root, deep} {
		got, ok, err := repo.Find(from)
		if err != nil || !ok {
			t.Fatalf("Find(%s) = %v, %v", from, ok, err)
		}
		if got.Root() != root {
			t.Errorf("Root() = %q, want %q", got.Root(), root)
		}
		if got.Linked() {
			t.Error("a main working tree should not report as linked")
		}
		if got.Main() != filepath.Join(root, ".git") {
			t.Errorf("Main() = %q", got.Main())
		}
	}
}

func TestFindALinkedWorktree(t *testing.T) {
	main := mainTree(t)
	linked := linkedTree(t, main, "parser")

	got, ok, err := repo.Find(linked)
	if err != nil || !ok {
		t.Fatalf("Find = %v, %v", ok, err)
	}
	if !got.Linked() {
		t.Error("a linked worktree should report as linked")
	}
	if got.Root() != linked {
		t.Errorf("Root() = %q, want %q", got.Root(), linked)
	}

	// Its common repository is the main tree's, which is what makes "same
	// project" answerable.
	wantMain, err := filepath.EvalSymlinks(filepath.Join(main, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Main() != wantMain {
		t.Errorf("Main() = %q, want %q", got.Main(), wantMain)
	}

	mainWT, _, err := repo.Find(main)
	if err != nil {
		t.Fatal(err)
	}
	if !repo.SameProject(got, mainWT) {
		t.Error("a linked worktree and its main tree should be the same project")
	}
}

// TestDifferentProjectsAreNotConfused is what stops a task being bound to a
// worktree of some other repository entirely.
func TestDifferentProjectsAreNotConfused(t *testing.T) {
	a, _, err := repo.Find(mainTree(t))
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := repo.Find(mainTree(t))
	if err != nil {
		t.Fatal(err)
	}
	if repo.SameProject(a, b) {
		t.Error("two unrelated checkouts should not be the same project")
	}
	if repo.SameProject(a, repo.Worktree{}) {
		t.Error("nothing is the same project as an unresolved worktree")
	}
}

// TestOutsideAnyCheckoutIsNotAnError: running `muff` somewhere that is not a
// repository is ordinary, and must not force an error path.
func TestOutsideAnyCheckout(t *testing.T) {
	got, ok, err := repo.Find(t.TempDir())
	if err != nil {
		t.Fatalf("Find outside a checkout = %v", err)
	}
	if ok {
		t.Errorf("Find claimed a worktree at %q", got.Root())
	}
	if !got.Zero() {
		t.Error("the returned worktree should be zero")
	}

	if _, err := repo.At(t.TempDir()); !errors.Is(err, fault.ErrUsage) {
		t.Errorf("At outside a checkout = %v, want a usage fault", err)
	}
	if _, _, err := repo.Find(" "); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Find(\" \") = %v, want an internal fault", err)
	}
}

// TestAtRequiresTheRoot. Binding a subdirectory would make the scope's meaning
// depend on where the binding happened to be made.
func TestAtRequiresTheRoot(t *testing.T) {
	root := mainTree(t)
	sub := filepath.Join(root, "internal")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.At(root); err != nil {
		t.Errorf("At on the root = %v", err)
	}
	_, err := repo.At(sub)
	if !errors.Is(err, fault.ErrUsage) {
		t.Errorf("At on a subdirectory = %v, want a usage fault", err)
	}
}

// TestMalformedGitMarkers are refused rather than guessed at.
func TestMalformedGitMarkers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		{"empty", ""},
		{"no prefix", "/some/path\n"},
		{"empty gitdir", "gitdir:\n"},
		{"wrong key", "worktree: /some/path\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, ".git"), []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, _, err := repo.Find(root); !errors.Is(err, fault.ErrParse) {
				t.Errorf("Find with a %s marker = %v, want a parse fault", tc.name, err)
			}
		})
	}
}
