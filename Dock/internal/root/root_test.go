package root_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/dock/internal/root"
)

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func touch(t *testing.T, path string, body string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestFindPrefersAnExplicitRoot: a tree that says what it is wins over one that
// has to be guessed at.
func TestFindPrefersAnExplicitRoot(t *testing.T) {
	base := t.TempDir()
	repo := mkdir(t, base, "repo")
	mkdir(t, repo, ".git")
	docs := mkdir(t, repo, "docs")
	touch(t, filepath.Join(docs, root.Marker), "")
	deep := mkdir(t, docs, "guide", "part")
	file := touch(t, filepath.Join(deep, "a.md"), "# §1 A\n")

	got, err := root.Find(file)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(docs); !sameDir(t, got, want) {
		t.Errorf("Find = %q, want the .dock directory %q", got, docs)
	}
}

func TestFindFallsBackToTheRepository(t *testing.T) {
	base := t.TempDir()
	repo := mkdir(t, base, "repo")
	mkdir(t, repo, ".git")
	deep := mkdir(t, repo, "a", "b")
	file := touch(t, filepath.Join(deep, "a.md"), "# §1 A\n")

	got, err := root.Find(file)
	if err != nil {
		t.Fatal(err)
	}
	if !sameDir(t, got, repo) {
		t.Errorf("Find = %q, want the repository %q", got, repo)
	}
}

// TestFindFallsBackToTheDocumentItself: Dock works on a directory nobody has
// prepared, which is most directories.
func TestFindFallsBackToTheDocumentItself(t *testing.T) {
	dir := t.TempDir()
	file := touch(t, filepath.Join(dir, "a.md"), "# §1 A\n")
	got, err := root.Find(file)
	if err != nil {
		t.Fatal(err)
	}
	if !sameDir(t, got, dir) {
		t.Errorf("Find = %q, want the file's own directory %q", got, dir)
	}
}

func TestFindAcceptsADirectory(t *testing.T) {
	dir := t.TempDir()
	sub := mkdir(t, dir, "sub")
	touch(t, filepath.Join(dir, root.Marker), "")
	got, err := root.Find(sub)
	if err != nil {
		t.Fatal(err)
	}
	if !sameDir(t, got, dir) {
		t.Errorf("Find = %q, want %q", got, dir)
	}
}

func TestWithin(t *testing.T) {
	base := t.TempDir()
	docs := mkdir(t, base, "docs")
	sub := mkdir(t, docs, "guide")
	inside := touch(t, filepath.Join(sub, "a.md"), "")

	rel, err := root.Within(docs, inside)
	if err != nil {
		t.Fatalf("Within: %v", err)
	}
	if rel != "guide/a.md" {
		t.Errorf("rel = %q, want guide/a.md", rel)
	}

	// A path that does not exist yet still has a place in the tree.
	if rel, err := root.Within(docs, filepath.Join(sub, "unwritten.md")); err != nil {
		t.Errorf("an unwritten document escaped: %v", err)
	} else if rel != "guide/unwritten.md" {
		t.Errorf("rel = %q", rel)
	}
}

func TestWithinRefusesAnEscape(t *testing.T) {
	base := t.TempDir()
	docs := mkdir(t, base, "docs")
	outside := touch(t, filepath.Join(base, "secret.md"), "")

	for _, path := range []string{
		outside,
		filepath.Join(docs, "..", "secret.md"),
		filepath.Join(docs, "..", "..", "etc", "passwd"),
	} {
		t.Run(path, func(t *testing.T) {
			_, err := root.Within(docs, path)
			if err == nil {
				t.Fatal("the path was accepted")
			}
			if !errors.Is(err, fault.ErrEscape) {
				t.Errorf("not an escape fault: %v", err)
			}
			if got := fault.Code(err); got != fault.CodeEscape {
				t.Errorf("code = %d, want %d — an escape is not an ordinary refusal", got, fault.CodeEscape)
			}
		})
	}
}

// TestASymlinkCannotWalkOut is the reason containment resolves symlinks: a
// check a link can step around is decoration.
func TestASymlinkCannotWalkOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on windows")
	}
	base := t.TempDir()
	docs := mkdir(t, base, "docs")
	outside := mkdir(t, base, "outside")
	touch(t, filepath.Join(outside, "secret.md"), "secret")

	link := filepath.Join(docs, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}

	_, err := root.Within(docs, filepath.Join(link, "secret.md"))
	if err == nil {
		t.Fatal("a symlink walked out of the root")
	}
	if !errors.Is(err, fault.ErrEscape) {
		t.Errorf("not an escape fault: %v", err)
	}
}

func TestWalkIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"c.md", "a.md", "b.md"} {
		touch(t, filepath.Join(dir, name), "# §1 "+name+"\n")
	}
	mkdir(t, dir, "sub")
	touch(t, filepath.Join(dir, "sub", "d.md"), "# §1 D\n")

	var first []string
	for i := 0; i < 5; i++ {
		got, err := root.Walk(dir)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = got
			continue
		}
		if strings.Join(got, "|") != strings.Join(first, "|") {
			t.Fatalf("walk %d differs:\n%v\n%v", i, first, got)
		}
	}
	if len(first) != 4 {
		t.Fatalf("walked %d files, want 4: %v", len(first), first)
	}
	// Lexicographic within a directory, and a subdirectory in its place.
	for i, want := range []string{"a.md", "b.md", "c.md", "d.md"} {
		if filepath.Base(first[i]) != want {
			t.Errorf("file %d is %s, want %s", i, filepath.Base(first[i]), want)
		}
	}
}

// TestWalkSkipsMachinery: a doc root is usually a repository, and .git is
// thousands of files that cannot be documents.
func TestWalkSkipsMachinery(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "real.md"), "# §1 A\n")
	for _, skip := range []string{".git", "node_modules", ".hidden"} {
		sub := mkdir(t, dir, skip)
		touch(t, filepath.Join(sub, "noise.md"), "# §1 Noise\n")
	}

	got, err := root.Walk(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("walked %d files, want 1: %v", len(got), got)
	}
	if filepath.Base(got[0]) != "real.md" {
		t.Errorf("walked %s", got[0])
	}
}

func TestWalkSkipsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on windows")
	}
	dir := t.TempDir()
	real := touch(t, filepath.Join(dir, "real.md"), "# §1 A\n")
	if err := os.Symlink(real, filepath.Join(dir, "alias.md")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	got, err := root.Walk(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("a symlinked document was walked twice: %v", got)
	}
}

func TestWalkOfAMissingDirectory(t *testing.T) {
	got, err := root.Walk(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Errorf("a missing directory should walk to nothing, not fail: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("walked %v", got)
	}
}

func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		ra = a
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		rb = b
	}
	return ra == rb
}
