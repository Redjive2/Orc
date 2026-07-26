package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "file.txt"), "hello\n")
	write(t, filepath.Join(dir, ".git", "config"), strings.Join([]string{
		"[core]",
		"\trepositoryformatversion = 0",
		"[remote \"origin\"]",
		"\turl = https://example.invalid/x.git",
		"\tfetch = +refs/heads/*:refs/remotes/origin/*",
		"[remote \"backup\"]",
		"\turl = git@example.invalid:x.git",
		"[branch \"main\"]",
		"\tremote = origin",
		"",
	}, "\n"))
	write(t, filepath.Join(dir, ".git", "worktrees", "feature", "gitdir"), "/real/checkout/.git\n")
	return dir
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDetachRemovesEveryRouteOut(t *testing.T) {
	dir := gitRepo(t)

	rep, err := Detach(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Remotes) != 2 {
		t.Fatalf("removed %v, want both remotes", rep.Remotes)
	}
	if rep.Worktrees != 1 {
		t.Fatalf("removed %d worktree registrations, want 1", rep.Worktrees)
	}

	config, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(config)
	if strings.Contains(text, "example.invalid") {
		t.Fatalf("a remote URL survived:\n%s", text)
	}
	// Everything that is not a remote is left alone: a probe's repo should
	// still behave like the repo it was copied from.
	for _, want := range []string{"[core]", "repositoryformatversion", "[branch \"main\"]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("detaching took more than the remotes; %q is gone:\n%s", want, text)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, ".git", "worktrees")); !os.IsNotExist(err) {
		t.Fatal("a worktree registration survived; that is the escape itself")
	}
}

func TestDetachWritesAnIdentitylessConfig(t *testing.T) {
	dir := gitRepo(t)
	if _, err := Detach(dir); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ProbeConfig))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"[credential]", "helper =", "probe@invalid"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the probe git config is missing %q:\n%s", want, text)
		}
	}
}

// TestDetachRemovesAWorktreePointer covers the copy of a checkout that is
// itself a worktree: its .git is a file naming a real repository.
func TestDetachRemovesAWorktreePointer(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, ".git"), "gitdir: /real/repo/.git/worktrees/feature\n")

	rep, err := Detach(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Worktrees != 1 {
		t.Fatal("a .git file pointing at a real repository was kept")
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Fatal("the pointer survived")
	}
}

func TestDetachOnSomethingThatIsNotARepo(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "file.txt"), "hello\n")

	rep, err := Detach(dir)
	if err != nil {
		t.Fatalf("detaching a plain directory failed: %v", err)
	}
	if rep.Git {
		t.Fatal("a plain directory was reported as a repository")
	}
	if _, err := os.Stat(filepath.Join(dir, ProbeConfig)); err != nil {
		t.Fatal("the probe git config should be written either way")
	}
}
