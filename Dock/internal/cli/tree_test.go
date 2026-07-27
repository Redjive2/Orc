package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/dock/internal/fixture"
)

// tree writes the fixture corpus plus the awkward files an overview has to
// survive: a binary, a non-UTF-8 file, an unreadable one, and machinery.
func tree(t *testing.T) string {
	t.Helper()
	dir := corpus(t)

	write := func(name string, body []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// Not a document by extension: never walked, never mentioned.
	write("logo.png", []byte{0x89, 'P', 'N', 'G', 0, 0, 1, 2, 3})
	// A source file whose string literals hold documentation. Walking Dock's
	// own repository showed why this must not be read as a document.
	write("fixture.go", []byte("package x\n\nconst doc = `\n## §1.1 Orphan\n`\n"))
	// Documents by extension that will not load: these are the ones a note is
	// for, because the caller plainly meant them to be documentation.
	write("binary.md", []byte{'#', ' ', 0, 0, 1, 2, 3})
	write("latin1.md", []byte{'#', ' ', 0xa7, '1', ' ', 0xe9, 't', 0xe9, '\n'})
	write("plain.md", []byte("# A document with no sections\n\nprose\n"))

	sub := filepath.Join(dir, "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "nested.md"), []byte("# §1 Nested\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	git := filepath.Join(dir, ".git")
	if err := os.MkdirAll(git, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(git, "COMMIT_EDITMSG"), []byte("# §1 Not a doc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestOverview(t *testing.T) {
	dir := tree(t)
	out, errs, code := run(t, "overview", dir)
	if code != fault.CodeOK {
		t.Fatalf("code = %d, stderr = %s", code, errs)
	}

	// Every real document appears.
	for _, want := range []string{"guide.md", "grammar.md", "trouble.md", "nested.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("overview is missing %s:\n%s", want, out)
		}
	}
	// Machinery does not.
	if strings.Contains(out, "COMMIT_EDITMSG") {
		t.Errorf("overview walked into .git:\n%s", out)
	}
	// A file with no sections is not a document, and is skipped silently.
	if strings.Contains(out, "plain.md") {
		t.Errorf("overview listed a file with no sections:\n%s", out)
	}
	if strings.Contains(errs, "plain.md") {
		t.Errorf("overview complained about a file that simply is not a document:\n%s", errs)
	}
}

// TestOverviewSkipsWithANote is the milestone's requirement: one unreadable
// corner of a tree must not cost the whole overview.
func TestOverviewSkipsWithANote(t *testing.T) {
	dir := tree(t)
	out, errs, code := run(t, "overview", dir)
	if code != fault.CodeOK {
		t.Fatalf("a bad file failed the command: code = %d\n%s", code, errs)
	}
	for _, noted := range []string{"binary.md", "latin1.md"} {
		if !strings.Contains(errs, noted) {
			t.Errorf("%s was skipped without a note:\n%s", noted, errs)
		}
	}
	if !strings.Contains(errs, "skipped") {
		t.Errorf("the note does not say what happened:\n%s", errs)
	}
	// A file that is not documentation is not a document's problem: it is
	// never walked, so it is never mentioned in either stream.
	for _, silent := range []string{"logo.png", "fixture.go"} {
		if strings.Contains(errs, silent) || strings.Contains(out, silent) {
			t.Errorf("%s was walked; only documentation should be:\nout: %s\nerr: %s", silent, out, errs)
		}
	}
	// The good documents still came through.
	if !strings.Contains(out, "guide.md") {
		t.Errorf("a skip cost the whole overview:\n%s", out)
	}
}

func TestOverviewIsDeterministic(t *testing.T) {
	dir := tree(t)
	first, _, _ := run(t, "overview", dir)
	for i := 0; i < 4; i++ {
		got, _, _ := run(t, "overview", dir)
		if got != first {
			t.Fatalf("overview %d differs from the first run", i)
		}
	}
}

func TestOverviewOfATreeWithNoDocuments(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("no sections here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errs, code := run(t, "overview", dir)
	if code != fault.CodeNotFound {
		t.Errorf("code = %d, want %d", code, fault.CodeNotFound)
	}
	if !strings.Contains(errs, "§") {
		t.Errorf("the diagnostic does not say what is missing: %s", errs)
	}
}

func TestFindByNumber(t *testing.T) {
	dir := tree(t)
	out, errs, code := run(t, "find", dir+"§1.1")
	if code != fault.CodeOK {
		t.Fatalf("code = %d, stderr = %s", code, errs)
	}
	// guide§1.1 is Install; trouble§1.1 is "Nothing resolves". Both match.
	if !strings.Contains(out, "guide.md§1.1") || !strings.Contains(out, "trouble.md§1.1") {
		t.Errorf("find did not report every match:\n%s", out)
	}
	if !strings.Contains(out, "go install") {
		t.Errorf("find printed no content:\n%s", out)
	}
}

func TestFindByName(t *testing.T) {
	dir := tree(t)
	out, _, code := run(t, "find", dir+"§'Install'")
	if code != fault.CodeOK {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(out, "go install") {
		t.Errorf("wrong section:\n%s", out)
	}
	if strings.Contains(out, "Nothing resolves") {
		t.Errorf("find matched a section it should not have:\n%s", out)
	}
}

// TestFindHeadersArePasteable: every header find prints is a target read
// accepts, so following a result is a copy-paste.
func TestFindHeadersArePasteable(t *testing.T) {
	dir := tree(t)
	out, _, code := run(t, "find", dir+"§1.1")
	if code != fault.CodeOK {
		t.Fatal(code)
	}
	found := 0
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "§") || !strings.HasPrefix(line, dir) {
			continue
		}
		targetText := strings.Fields(line)[0]
		if _, _, code := run(t, "read", targetText); code != fault.CodeOK {
			t.Errorf("header %q does not resolve as a target", targetText)
		}
		found++
	}
	if found == 0 {
		t.Errorf("no headers found in:\n%s", out)
	}
}

func TestFindTree(t *testing.T) {
	dir := tree(t)
	own, _, _ := run(t, "find", dir+"§1.2")
	tree, _, _ := run(t, "find", dir+"§1.2", "--tree")
	if len(tree) <= len(own) {
		t.Errorf("--tree returned no more than the own prose:\n%s", tree)
	}
	if !strings.Contains(tree, "Numbering") {
		t.Errorf("--tree did not include the subsection:\n%s", tree)
	}
}

func TestFindExitCodes(t *testing.T) {
	dir := tree(t)
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"no match", []string{"find", dir + "§8.8"}, fault.CodeNotFound},
		{"no such directory", []string{"find", filepath.Join(dir, "nope") + "§1"}, fault.CodeNotFound},
		{"not a target", []string{"find", dir}, fault.CodeUsage},
		{"malformed", []string{"find", dir + "§1..2"}, fault.CodeParse},
		{"no argument", []string{"find"}, fault.CodeUsage},
		{"overview no argument", []string{"overview"}, fault.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, code := run(t, tc.args...); code != tc.want {
				t.Errorf("code = %d, want %d", code, tc.want)
			}
		})
	}
}

// TestOverviewCostsATablePerDocument. Frugality again: an overview of a tree is
// the tables, never the documents.
func TestOverviewCostsATablePerDocument(t *testing.T) {
	dir := tree(t)
	out, _, code := run(t, "overview", dir)
	if code != fault.CodeOK {
		t.Fatal(code)
	}
	whole := len(fixture.Guide) + len(fixture.Grammar) + len(fixture.Trouble)
	if len(out) > whole*3 {
		t.Errorf("overview cost %d bytes for %d bytes of documents", len(out), whole)
	}
	for _, phrase := range []string{"Dock reads documentation", "go install", "This document states"} {
		if strings.Contains(out, phrase) {
			t.Errorf("overview leaked content: %q", phrase)
		}
	}
}

// --- folders an overview would otherwise not mention ----------------------

// The complaint this answers: a folder made a moment ago does not appear in an
// overview, because an overview is a table per document and a new folder has
// none. Nothing distinguishes it from a folder that failed to be created, which
// is exactly what somebody who just made one is checking.
func TestOverviewNamesFoldersWithNoDocuments(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "guide.md"), []byte("# §1 Guide\n\nprose\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Word Of Alex", "pics"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "pics", "a.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, errs, code := run(t, "overview", dir)
	if code != fault.CodeOK {
		t.Fatalf("code = %d, stderr = %s", code, errs)
	}
	if !strings.Contains(out, "Word Of Alex") {
		t.Errorf("a folder with nothing in it is missing from the overview:\n%s", out)
	}
	if !strings.Contains(out, "empty") {
		t.Errorf("the overview does not say why it has nothing to show:\n%s", out)
	}
	if !strings.Contains(out, "nothing dock reads") {
		t.Errorf("a folder of files dock does not read is not told apart from an "+
			"empty one, and they are different things to do about:\n%s", out)
	}
	// The documents are still the point, and still first.
	if !strings.Contains(out, "guide.md") {
		t.Errorf("the documents went missing:\n%s", out)
	}
	if strings.Index(out, "guide.md") > strings.Index(out, "Word Of Alex") {
		t.Errorf("the folders are drawn above the documents:\n%s", out)
	}
}

// A tree of folders and no documents is a tree somebody is part-way through.
// Saying "not found" over a list of what is there would be the screen arguing
// with itself.
func TestATreeOfFoldersIsNotAMissingTree(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "Docs"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, errs, code := run(t, "overview", dir)
	if code != fault.CodeOK {
		t.Fatalf("code = %d, stderr = %s\n%s", code, errs, out)
	}
	if !strings.Contains(out, "Docs") {
		t.Errorf("the folder that is there is not in the listing:\n%s", out)
	}
}

// Nothing at all is still not found: there is no folder to point at, so the old
// answer is the right one.
func TestAnEmptyTreeIsStillNotFoundUnderJSON(t *testing.T) {
	dir := t.TempDir()
	out, _, code := run(t, "overview", dir, "--json")
	if code != fault.CodeOK {
		t.Fatalf("--json is data, not a failure: code = %d", code)
	}
	// The folders stay out of the JSON: it is an array of documents and cq reads
	// it as one, so a new shape would break a reader built before it.
	if strings.Contains(out, "folder") {
		t.Errorf("the folder list leaked into --json, whose shape is a contract:\n%s", out)
	}
}
