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

// --- the folders a walk has nothing to say about --------------------------

// A walk returns documents, so a folder holding none of them appears in nothing
// downstream — which makes a folder somebody has just made indistinguishable from
// one that was never created. WalkTree names them, with a reason each, because
// "empty" and "cannot be read" are different problems with different fixes.

// folderWhy indexes a WalkTree's folders by their path relative to the tree.
func folderWhy(t *testing.T, dir string) map[string]string {
	t.Helper()

	_, folders, err := root.WalkTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range folders {
		rel, err := filepath.Rel(dir, f.Path)
		if err != nil {
			t.Fatal(err)
		}
		out[rel] = f.Why.String()
	}
	return out
}

func TestWalkTreeNamesTheFoldersWithNothingInThem(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "real.md"), "# §1 A\n")

	mkdir(t, dir, "brand-new")               // made a moment ago, still empty
	pics := mkdir(t, dir, "pics")            // files, none of them documents
	touch(t, filepath.Join(pics, "a.png"), "not a document\n")
	hidden := mkdir(t, dir, ".private")      // skipped by name, not by absence
	touch(t, filepath.Join(hidden, "x.md"), "# §1 Hidden\n")

	got := folderWhy(t, dir)
	for path, want := range map[string]string{
		"brand-new": "empty",
		"pics":      "nothing dock reads",
		".private":  "not walked",
	} {
		if got[path] != want {
			t.Errorf("%s is reported as %q, want %q — a folder nobody can see in a "+
				"listing reads as a folder that was never made", path, got[path], want)
		}
	}
}

// A folder on the way to a document is already visible in that document's path.
// Listing it as barren as well would be noise in the one place noise costs the
// most: the list of things that are *not* where you expected them.
func TestWalkTreeIsQuietAboutFoldersOnTheWayToADocument(t *testing.T) {
	dir := t.TempDir()
	deep := mkdir(t, mkdir(t, dir, "a"), "b")
	touch(t, filepath.Join(deep, "doc.md"), "# §1 Deep\n")

	got := folderWhy(t, dir)
	if len(got) != 0 {
		t.Errorf("folders reported for a tree where every one leads to a document: %v", got)
	}
}

// The tree named on the command line is itself a folder, and an empty one is the
// whole answer to the question that was asked.
func TestWalkTreeNamesTheRootWhenItIsTheEmptyOne(t *testing.T) {
	dir := t.TempDir()
	if got := folderWhy(t, dir); got["."] != "empty" {
		t.Errorf("an empty tree reports %v; it should say the tree itself is empty", got)
	}
}

// A folder that cannot be read is the one worth naming most, and the one a walk
// most easily loses: it is skipped so that one bad corner does not cost the whole
// overview, and skipping it silently says "nothing here" — which is not what is
// true. Nobody can tell whether there is anything there.
func TestWalkTreeNamesAFolderItCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a 0000 directory, so there is nothing to refuse")
	}
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "real.md"), "# §1 A\n")
	locked := mkdir(t, dir, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	got := folderWhy(t, dir)
	if got["locked"] != "cannot be read" {
		t.Errorf("an unreadable folder is reported as %q", got["locked"])
	}
	// WalkDir announces a directory and then reports that it could not be read, so
	// the first visit files it as ordinary. One folder, one reason.
	count := 0
	_, folders, _ := root.WalkTree(dir)
	for _, f := range folders {
		if filepath.Base(f.Path) == "locked" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("the same folder is listed %d times, under more than one reason", count)
	}
}

// Walk is WalkTree's first half and must stay exactly that: every caller of Walk
// is a caller that wants documents and nothing else.
func TestWalkStillReturnsOnlyDocuments(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "real.md"), "# §1 A\n")
	mkdir(t, dir, "empty")

	docs, err := root.Walk(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || filepath.Base(docs[0]) != "real.md" {
		t.Errorf("Walk returned %v", docs)
	}
}
