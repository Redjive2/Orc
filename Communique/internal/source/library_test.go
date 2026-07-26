package source_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"orc/cq/internal/protocol"
	"orc/cq/internal/source"
)

// repo builds a small checkout to collect.
func repo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("Docs/Vision.md", "# §1 Thing\n\nprose\n")
	write("internal/app.go", "package app\n\nfunc Main() {}\n")
	write("internal/app.js", "export const x = 1;\n")
	// Not carried: neither is anything a reader browses.
	write("bin/tool", "\x7fELF binary-ish")
	write("internal/app.wasm", "not readable by extension")
	write(".git/config", "[core]\n")
	write("node_modules/pkg/index.js", "module.exports = 1\n")
	return root
}

func collect(t *testing.T, root string, run *fakeRun) *protocol.Library {
	t.Helper()
	c := newCLI(run)
	lib, err := c.Library(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if lib == nil {
		t.Fatal("no library was collected")
	}
	return lib
}

// lenses answers both tools for a checkout at root.
//
// The keys are the whole command line, because that is what the adapter runs:
// dock is asked once for the tree, and anno once per directory, since its
// overview reads a directory rather than a tree.
func lenses(t *testing.T, root string) *fakeRun {
	t.Helper()
	f := newFakeRun()
	f.out[dockKey(root)] = "[]"
	for _, dir := range dirsOf(t, root) {
		f.out["anno overview "+dir+" --json"] = "[]"
	}
	return f
}

func dockKey(root string) string { return "dock overview " + root + " --json" }

// dirsOf lists every directory anno will be asked about.
func dirsOf(t *testing.T, root string) []string {
	t.Helper()
	seen := map[string]bool{}
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		dir := filepath.Dir(path)
		if !seen[dir] {
			seen[dir] = true
			out = append(out, dir)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func paths(lib *protocol.Library) []string {
	out := make([]string, 0, len(lib.Files))
	for _, f := range lib.Files {
		out = append(out, f.Path)
	}
	return out
}

func find(lib *protocol.Library, path string) (protocol.File, bool) {
	for _, f := range lib.Files {
		if f.Path == path {
			return f, true
		}
	}
	return protocol.File{}, false
}

// TestOnlyReadableFilesAreCarried: a repository is mostly things nobody browses,
// and walking past them silently is the difference between a library and a disk
// dump.
func TestOnlyReadableFilesAreCarried(t *testing.T) {
	root := repo(t)
	lib := collect(t, root, lenses(t, root))

	got := strings.Join(paths(lib), " ")
	for _, want := range []string{"Docs/Vision.md", "internal/app.go", "internal/app.js"} {
		if !strings.Contains(got, want) {
			t.Errorf("%s was not carried: %v", want, paths(lib))
		}
	}
	for _, unwanted := range []string{"bin/tool", "app.wasm", ".git", "node_modules"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("%s should not have been carried: %v", unwanted, paths(lib))
		}
	}
}

func TestPathsAreRelativeAndSlashed(t *testing.T) {
	root := repo(t)
	lib := collect(t, root, lenses(t, root))
	for _, f := range lib.Files {
		if strings.HasPrefix(f.Path, "/") || strings.Contains(f.Path, "\\") {
			t.Errorf("path %q is not a relative slashed path", f.Path)
		}
		// The wire refuses anything else, so this is also what makes the
		// snapshot valid.
		if err := f.Validate(); err != nil {
			t.Errorf("collected file does not validate: %v", err)
		}
	}
}

// TestAFileTooLargeSaysSoRatherThanVanishing: a reader who cannot find something
// must be able to tell "too big to carry" from "not there", and an omission says
// neither.
func TestAFileTooLargeSaysSoRatherThanVanishing(t *testing.T) {
	root := repo(t)
	big := filepath.Join(root, "generated.go")
	if err := os.WriteFile(big, make([]byte, protocol.MaxFileBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}

	lib := collect(t, root, lenses(t, root))
	f, ok := find(lib, "generated.go")
	if !ok {
		t.Fatal("the large file was dropped instead of reported")
	}
	if f.Text != "" {
		t.Error("a file past the limit should carry no text")
	}
	if !strings.Contains(f.Skipped, "limit") {
		t.Errorf("skipped = %q, want it to say why", f.Skipped)
	}
	// Its real size is still reported, so the interface can say what it is
	// missing rather than showing an empty file.
	if f.Bytes <= protocol.MaxFileBytes {
		t.Errorf("bytes = %d, want the real size", f.Bytes)
	}
}

// TestAFileWithAControlCharacterCostsOnlyItself is the bug this test exists for.
//
// A file can be valid UTF-8 and still hold a NUL — a fixture, a test corpus,
// something generated. The wire refuses control characters, so carrying it
// failed the *whole* sync: one file, and the mailbox stopped mirroring too.
func TestAFileWithAControlCharacterCostsOnlyItself(t *testing.T) {
	root := repo(t)
	if err := os.WriteFile(filepath.Join(root, "corpus.txt"), []byte("a\x00b\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	lib := collect(t, root, lenses(t, root))
	f, ok := find(lib, "corpus.txt")
	if !ok {
		t.Fatal("the file was dropped instead of reported")
	}
	if f.Text != "" {
		t.Error("a file with a control character should carry no text")
	}
	if !strings.Contains(f.Skipped, "control character") {
		t.Errorf("skipped = %q, want it to say why", f.Skipped)
	}

	// The rest of the library is intact, and the whole thing still validates —
	// which is the property that was actually broken.
	if err := lib.Validate(); err != nil {
		t.Errorf("one bad file invalidated the library: %v", err)
	}
	if _, ok := find(lib, "internal/app.go"); !ok {
		t.Error("the other files were lost with it")
	}
}

// Tab, newline and carriage return are text and must survive: refusing them
// would skip every file in the repository.
func TestOrdinaryWhitespaceIsNotAControlCharacter(t *testing.T) {
	root := repo(t)
	body := "package app\r\n\tif true {\n\t\treturn\n\t}\n"
	if err := os.WriteFile(filepath.Join(root, "tabs.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	f, ok := find(collect(t, root, lenses(t, root)), "tabs.go")
	if !ok || f.Skipped != "" {
		t.Fatalf("a file with tabs and CRLF was skipped: %+v", f)
	}
	if f.Text != body {
		t.Error("the text was altered")
	}
}

// Invalid UTF-8 is not text whatever its extension says, and carrying it would
// put bytes on a wire that refuses them — failing the whole sync over one file.
func TestSomethingThatIsNotTextIsNotCarriedAsText(t *testing.T) {
	root := repo(t)
	if err := os.WriteFile(filepath.Join(root, "broken.go"), []byte{0xff, 0xfe, 0x00}, 0o644); err != nil {
		t.Fatal(err)
	}

	lib := collect(t, root, lenses(t, root))
	f, ok := find(lib, "broken.go")
	if !ok {
		t.Fatal("the file was dropped instead of reported")
	}
	if f.Text != "" || !strings.Contains(f.Skipped, "not text") {
		t.Errorf("file = %+v", f)
	}
	if err := lib.Validate(); err != nil {
		t.Errorf("the library should still be valid: %v", err)
	}
}

// TestTheLensesAttachToTheRightFiles: Dock and Anno report their own paths, and
// both have to key the same file the same way or the structure lands on the
// wrong row — or nowhere.
func TestTheLensesAttachToTheRightFiles(t *testing.T) {
	root := repo(t)
	f := lenses(t, root)
	f.out[dockKey(root)] = `[{"path":"` + filepath.ToSlash(filepath.Join(root, "Docs/Vision.md")) + `",
		"lines":3,"sections":[{"number":"1","name":"Thing","depth":1,"start":1,"end":3,"lines":3,"out":0}]}]`
	f.out["anno overview "+filepath.Join(root, "internal")+" --json"] = `[{"path":"` + filepath.ToSlash(filepath.Join(root, "internal/app.go")) + `",
		"lines":3,"nodes":[{"kind":"section","name":"code","start":1,"end":3,"lines":3,
		"content_start":1,"content_end":3,"children":[]}]}]`

	lib := collect(t, root, f)

	doc, ok := find(lib, "Docs/Vision.md")
	if !ok || len(doc.Sections) != 1 || doc.Sections[0].Name != "Thing" {
		t.Errorf("the document's sections did not attach: %+v", doc)
	}
	code, ok := find(lib, "internal/app.go")
	if !ok || len(code.Annotations) != 1 || code.Annotations[0].Name != "code" {
		t.Errorf("the annotations did not attach: %+v", code)
	}
	// And they did not attach to each other.
	if len(doc.Annotations) != 0 || len(code.Sections) != 0 {
		t.Error("the two lenses crossed over")
	}
}

// TestALensThatFailsCostsOnlyItself: Dock and Anno are two views of one tree,
// and losing one should cost its structure, not the library.
func TestALensThatFailsCostsOnlyItself(t *testing.T) {
	root := repo(t)
	f := lenses(t, root)
	f.err[dockKey(root)] = errors.New("dock: command not found")

	var warned []string
	c := newCLI(f)
	c.Warn = func(format string, args ...any) { warned = append(warned, format) }

	lib, err := c.Library(t.Context(), root)
	if err != nil {
		t.Fatalf("a missing lens should not fail the collection: %v", err)
	}
	if len(lib.Files) == 0 {
		t.Error("the files should still be there")
	}
	if len(warned) == 0 {
		t.Error("a missing lens should be said, not silently absent")
	}
}

func TestNoRootMeansNoLibrary(t *testing.T) {
	lib, err := newCLI(newFakeRun()).Library(t.Context(), "")
	if err != nil || lib != nil {
		t.Errorf("library = %v, err = %v; want nothing collected", lib, err)
	}
}

// A snapshot only carries a library when one was asked for, so the ordinary
// machine pays nothing for the feature.
func TestASnapshotCarriesNoLibraryUnlessAsked(t *testing.T) {
	snap, err := newCLI(newFakeRun()).Snapshot(t.Context(), source.Options{Machine: "studio"})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Library != nil {
		t.Errorf("library = %+v, want none", snap.Library)
	}
}

// TestAMissingLensIsSaidRatherThanShownAsEmptiness.
//
// Both notes exist for the same reason: the reader is at a browser on another
// machine, so a tool missing on the agent is invisible to them. Without the
// note the docs tab says no document has sections and the code tab says nothing
// is annotated — two statements about the fleet, made from a fact about one
// machine's PATH.
func TestAMissingLensIsSaidRatherThanShownAsEmptiness(t *testing.T) {
	root := repo(t)

	for _, tc := range []struct {
		name string
		tool string
		want string
	}{
		{"dock", "dock", "no document has sections"},
		{"anno", "anno", "no file carries annotations"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := lenses(t, root)
			missing := &exec.Error{Name: tc.tool, Err: exec.ErrNotFound}
			f.err[dockKey(root)] = missing
			for _, dir := range dirsOf(t, root) {
				f.err["anno overview "+dir+" --json"] = missing
			}
			// Only the tool under test is missing; the other answers normally,
			// so the note has to be the one this absence produces.
			if tc.tool == "dock" {
				for _, dir := range dirsOf(t, root) {
					delete(f.err, "anno overview "+dir+" --json")
				}
			} else {
				delete(f.err, dockKey(root))
			}

			lib := collect(t, root, f)
			if !strings.Contains(strings.Join(lib.Notes, " | "), tc.want) {
				t.Fatalf("notes = %q, want one mentioning %q", lib.Notes, tc.want)
			}
		})
	}
}

// TestAMissingAnnoIsAskedOnceRatherThanPerDirectory: without the early return,
// a tree of a few hundred directories forks a few hundred doomed processes to
// learn the same thing each time.
func TestAMissingAnnoIsAskedOnceRatherThanPerDirectory(t *testing.T) {
	root := repo(t)
	f := lenses(t, root)
	for _, dir := range dirsOf(t, root) {
		f.err["anno overview "+dir+" --json"] = &exec.Error{Name: "anno", Err: exec.ErrNotFound}
	}

	collect(t, root, f)

	if asked := annoCalls(f); asked != 1 {
		t.Errorf("anno was run %d times, want 1", asked)
	}
}

// annoCalls counts how many times the sweep reached anno.
func annoCalls(f *fakeRun) int {
	var n int
	for _, call := range f.calls {
		if call[0] == "anno" {
			n++
		}
	}
	return n
}

// A directory anno merely refuses is a different thing, and must not stop the
// rest of the tree from being asked: a file that mentions the marker syntax is
// not a file with annotations.
func TestARefusedDirectoryDoesNotStopTheSweep(t *testing.T) {
	root := repo(t)

	// The baseline is how many directories the collector asks about at all —
	// which is fewer than the tree holds, since a directory with nothing
	// carried in it is never reached.
	base := lenses(t, root)
	collect(t, root, base)
	want := annoCalls(base)

	f := lenses(t, root)
	f.err["anno overview "+filepath.Join(root, "internal")+" --json"] =
		errors.New("anno: that is not a thing I read")

	lib := collect(t, root, f)

	if asked := annoCalls(f); asked != want {
		t.Errorf("anno was run %d times, want %d — one refusal ended the sweep", asked, want)
	}
	for _, note := range lib.Notes {
		if strings.Contains(note, "annotations") {
			t.Errorf("a refused directory was reported as anno being missing: %q", note)
		}
	}
}

// TestACheckoutOfCRLFFilesIsCarried is the collector's half of what makes cq
// usable on Windows.
//
// Every file in a checkout there ends its lines with a carriage return. The
// wire refuses control characters and one refused file fails the whole
// snapshot — so a collector that treated CR as binary would not skip a file, it
// would take the entire mirror down on that machine. The two rules are the same
// function now, and this is what proves they agree in practice.
func TestACheckoutOfCRLFFilesIsCarried(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"main.go":       "package main\r\n\r\nfunc main() {}\r\n",
		"Docs/Guide.md": "# §1 Thing\r\n\r\nprose with ─ box drawing\r\n",
		"unix.go":       "package unix\n\nvar x = 1\n",
	}
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	lib := collect(t, root, lenses(t, root))
	if err := lib.Validate(); err != nil {
		t.Fatalf("a CRLF checkout did not survive validation: %v", err)
	}
	if len(lib.Files) != len(files) {
		t.Fatalf("carried %d files, want %d", len(lib.Files), len(files))
	}

	for _, f := range lib.Files {
		want, ok := files[f.Path]
		if !ok {
			t.Errorf("unexpected file %q", f.Path)
			continue
		}
		if f.Skipped != "" {
			t.Errorf("%s was skipped: %s", f.Path, f.Skipped)
			continue
		}
		// Byte for byte. The carriage returns have to survive the trip, or the
		// digest the browser takes describes a file that is not on disk and
		// every edit is refused as stale.
		if f.Text != want {
			t.Errorf("%s = %q, want %q", f.Path, f.Text, want)
		}
	}
}
