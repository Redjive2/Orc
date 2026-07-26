package snapshot

import (
	"os"
	"path/filepath"
	"testing"
)

// world builds a small source tree with the shapes a real store has.
func world(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")

	for _, d := range []string{"users/alice", "messages/ab"} {
		if err := os.MkdirAll(filepath.Join(src, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for path, body := range map[string]string{
		"version":                   "1\n",
		"users/alice/user.json":     `{"version":1}`,
		"users/alice/journal.jsonl": "{\"kind\":\"landed\"}\n",
		"messages/ab/abc.msg":       "mailman/1\n",
	} {
		if err := os.WriteFile(filepath.Join(src, path), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return src
}

func TestCopyReproducesTheTree(t *testing.T) {
	src := world(t)
	dst := filepath.Join(t.TempDir(), "copy")

	rep, err := Copy(dst, src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Files != 4 {
		t.Fatalf("copied %d files, want 4", rep.Files)
	}

	got, err := os.ReadFile(filepath.Join(dst, "messages/ab/abc.msg"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "mailman/1\n" {
		t.Fatalf("content came across as %q", got)
	}

	// The digest of the copy must equal the digest of the source, or drift
	// detection compares two different things.
	sourceDigest, err := Digest(src)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Digest != sourceDigest {
		t.Fatal("the copy's digest differs from the source's")
	}
}

func TestCopyLeavesTheSourceAlone(t *testing.T) {
	src := world(t)
	before, err := Digest(src)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Copy(filepath.Join(t.TempDir(), "copy"), src, Options{}); err != nil {
		t.Fatal(err)
	}

	after, err := Digest(src)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("copying changed the source; this is the one thing snapshot must never do")
	}
}

// TestCopyDropsLinksOutOfTheTree is the escape this package exists to refuse: a
// probe with a symlink pointing at the real store is a probe with a live
// pointer at the real world.
func TestCopyDropsLinksOutOfTheTree(t *testing.T) {
	src := world(t)
	outside := filepath.Join(t.TempDir(), "real-store")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(src, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("version", filepath.Join(src, "inside")); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "copy")
	rep, err := Copy(dst, src, Options{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Lstat(filepath.Join(dst, "escape")); !os.IsNotExist(err) {
		t.Fatal("a symlink out of the tree came across")
	}
	if _, err := os.Lstat(filepath.Join(dst, "inside")); err != nil {
		t.Fatal("a symlink inside the tree was dropped; it means the same thing in the copy")
	}
	if rep.Symlinks != 1 {
		t.Fatalf("recorded %d symlinks, want 1", rep.Symlinks)
	}

	var found bool
	for _, d := range rep.Drops {
		if d.Rel == "escape" {
			found = true
		}
	}
	if !found {
		t.Fatal("the dropped link is not in the report; a silent drop is how a probe stops resembling the world")
	}
}

func TestCopyRefusesAnExistingTarget(t *testing.T) {
	src := world(t)
	dst := filepath.Join(t.TempDir(), "copy")
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Copy(dst, src, Options{}); err == nil {
		t.Fatal("Copy wrote onto an existing tree")
	}
}

func TestCopyExcludes(t *testing.T) {
	src := world(t)
	dst := filepath.Join(t.TempDir(), "copy")

	if _, err := Copy(dst, src, Options{Exclude: func(rel string) bool { return rel == "messages" }}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "messages")); !os.IsNotExist(err) {
		t.Fatal("an excluded directory came across")
	}
	if _, err := os.Stat(filepath.Join(dst, "version")); err != nil {
		t.Fatal("exclusion took more than it was asked for")
	}
}

func TestCopyIsOwnerOnly(t *testing.T) {
	src := world(t)
	if err := os.Chmod(filepath.Join(src, "version"), 0o666); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "copy")
	if _, err := Copy(dst, src, Options{}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(dst, "version"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("copied file is %v; a probe holds minted keys and must not widen permissions", perm)
	}
}
