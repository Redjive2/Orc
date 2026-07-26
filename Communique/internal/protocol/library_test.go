package protocol_test

import (
	"errors"
	"strings"
	"testing"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
)

func sound() protocol.File {
	return protocol.File{Path: "Docs/Dock/Vision.md", Lines: 83, Bytes: 2400, Text: "hello\n"}
}

// TestAPathMayNotClimbOutOfTheTree is the rule worth having.
//
// The interface turns these into links and the server looks files up by them, so
// a path that climbs out is either a bug or an attempt, and neither should reach
// a handler that then has to be careful about it.
func TestAPathMayNotClimbOutOfTheTree(t *testing.T) {
	for _, path := range []string{
		"../etc/passwd",
		"Docs/../../etc/passwd",
		"/etc/passwd",
		"/",
		"",
	} {
		f := sound()
		f.Path = path
		if err := f.Validate(); !errors.Is(err, fault.ErrParse) {
			t.Errorf("path %q was accepted: %v", path, err)
		}
	}
}

// TestAWindowsShapedPathIsRefusedByTheServer.
//
// The server may be on Linux while the agent is on Windows. There, `/` is the
// only separator the server understands and `\\` is one the agent obeys — so a
// path holding a backslash passes a check that splits on `/` and then means
// something else entirely when it lands. The agent's containment check catches
// it as well, but a refusal a sync later is a worse refusal than one at the
// door.
func TestAWindowsShapedPathIsRefusedByTheServer(t *testing.T) {
	for _, path := range []string{
		`Docs\..\..\Windows\System32\drivers\etc\hosts`,
		`Docs\Vision.md`,
		`C:\Windows\System32\hosts`,
		"C:/Windows/System32/hosts",
		"c:relative",
	} {
		f := sound()
		f.Path = path
		if err := f.Validate(); !errors.Is(err, fault.ErrParse) {
			t.Errorf("path %q was accepted: %v", path, err)
		}
	}
}

// A path that merely contains dots is fine: `..foo` and `a..b` are names.
func TestOrdinaryPathsAreAccepted(t *testing.T) {
	for _, path := range []string{
		"Docs/Dock/Vision.md",
		"a/b/c.go",
		"..hidden.md",
		"weird..name.go",
		"./relative.go",
	} {
		f := sound()
		f.Path = path
		if err := f.Validate(); err != nil {
			t.Errorf("path %q was refused: %v", path, err)
		}
	}
}

// TestAFileSaysEitherWhatItHoldsOrWhyItDoesNot: both at once is a contradiction,
// and the interface would show an empty file with a reason beside it.
func TestAFileSaysEitherWhatItHoldsOrWhyItDoesNot(t *testing.T) {
	f := sound()
	f.Skipped = "it is too large"
	if err := f.Validate(); !errors.Is(err, fault.ErrParse) {
		t.Errorf("a file with both text and a reason was accepted: %v", err)
	}

	// Either alone is fine — a skipped file has no text, and a carried one has
	// no reason.
	f.Text = ""
	if err := f.Validate(); err != nil {
		t.Errorf("a skipped file was refused: %v", err)
	}
}

// TestAnnotationNestingIsBounded stops a damaged or hostile snapshot from
// exhausting the stack while being validated. The structure is recursive and
// arrives over a wire, so the bound is what makes validating it safe.
func TestAnnotationNestingIsBounded(t *testing.T) {
	deep := protocol.Annotation{Kind: "part", Name: "leaf", Start: 1, End: 1}
	for range protocol.MaxAnnotationDepth + 2 {
		deep = protocol.Annotation{
			Kind: "section", Name: "wrap", Start: 1, End: 1,
			Children: []protocol.Annotation{deep},
		}
	}
	if err := deep.Validate(); !errors.Is(err, fault.ErrParse) {
		t.Errorf("nesting past the limit was accepted: %v", err)
	}

	// Anno's own three ranks are nowhere near the bound, so a real file passes.
	real := protocol.Annotation{
		Kind: "section", Name: "types", Start: 21, End: 30,
		Children: []protocol.Annotation{{
			Kind: "symbol", Name: "Pair", Start: 23, End: 26,
			Children: []protocol.Annotation{{Kind: "part", Name: "fields", Start: 24, End: 25}},
		}},
	}
	if err := real.Validate(); err != nil {
		t.Errorf("a real annotation tree was refused: %v", err)
	}
}

func TestAnnotationFieldsAreChecked(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    protocol.Annotation
		want string
	}{
		{"unknown kind", protocol.Annotation{Kind: "chapter", Name: "x", Start: 1, End: 1}, "unknown kind"},
		{"no name", protocol.Annotation{Kind: "section", Start: 1, End: 1}, "name is empty"},
		{"line zero", protocol.Annotation{Kind: "section", Name: "x", Start: 0, End: 1}, "not a line number"},
		{"backwards", protocol.Annotation{Kind: "section", Name: "x", Start: 9, End: 2}, "ends before it begins"},
		{"broken child", protocol.Annotation{
			Kind: "section", Name: "x", Start: 1, End: 9,
			Children: []protocol.Annotation{{Kind: "nonsense", Name: "y", Start: 2, End: 3}},
		}, "unknown kind"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.a.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestASectionWithNoBodyIsAllowed: a heading nobody has written under yet is a
// real section, and Dock reports its span as 0:0. Refusing it failed the entire
// sync the first time a document was created from the browser — one empty
// section costing the whole mirror.
func TestASectionWithNoBodyIsAllowed(t *testing.T) {
	empty := protocol.Section{Number: "1", Name: "Notes", Depth: 1, Start: 0, End: 0, Lines: 0}
	if err := empty.Validate(); err != nil {
		t.Errorf("a section with no body was refused: %v", err)
	}
	// A half-empty span is still a mistake: it says the section starts nowhere
	// and ends somewhere.
	half := protocol.Section{Number: "1", Name: "Notes", Depth: 1, Start: 0, End: 4}
	if err := half.Validate(); err == nil {
		t.Error("a span that starts at line 0 and ends at 4 should be refused")
	}
}

func TestSectionFieldsAreChecked(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    protocol.Section
		want string
	}{
		{"no number", protocol.Section{Name: "Install", Depth: 1, Start: 1, End: 2}, "number is empty"},
		{"no depth", protocol.Section{Number: "1", Name: "Install", Start: 1, End: 2}, "not a heading level"},
		{"backwards", protocol.Section{Number: "1", Name: "x", Depth: 1, Start: 9, End: 2}, "ends before it begins"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.s.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestOnePathOneFile: a duplicate means the two ends disagree about what a path
// addresses, and the interface would show whichever it reached first.
func TestOnePathOneFile(t *testing.T) {
	lib := protocol.Library{Files: []protocol.File{sound(), sound()}}
	err := lib.Validate()
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("error = %v, want a complaint about the duplicate", err)
	}
}

func TestALibraryValidatesEveryFile(t *testing.T) {
	bad := sound()
	bad.Path = "../escape.md"
	lib := protocol.Library{Files: []protocol.File{sound(), bad}}
	if err := lib.Validate(); !errors.Is(err, fault.ErrParse) {
		t.Errorf("a bad file passed inside a good library: %v", err)
	}
}

func TestALibraryBoundsHowManyFilesItCarries(t *testing.T) {
	lib := protocol.Library{Files: make([]protocol.File, protocol.MaxLibraryFiles+1)}
	err := lib.Validate()
	if err == nil || !strings.Contains(err.Error(), "exceeds the limit") {
		t.Errorf("error = %v, want a complaint about the count", err)
	}
}

// A snapshot carrying a library validates it, so a damaged one is refused at the
// wire rather than served to a browser.
func TestASnapshotValidatesItsLibrary(t *testing.T) {
	s := snapshot()
	bad := sound()
	bad.Path = "/absolute.md"
	s.Library = &protocol.Library{Root: "Orc", Files: []protocol.File{bad}}

	if err := s.Validate(); !errors.Is(err, fault.ErrParse) {
		t.Errorf("a snapshot accepted a bad library: %v", err)
	}

	s.Library.Files[0].Path = "fine.md"
	if err := s.Validate(); err != nil {
		t.Errorf("a snapshot refused a sound library: %v", err)
	}
}

// A snapshot without one is the ordinary case: most machines mirror no
// repository, and absent must not read as empty.
func TestALibraryIsOptional(t *testing.T) {
	s := snapshot()
	s.Library = nil
	if err := s.Validate(); err != nil {
		t.Errorf("a snapshot with no library was refused: %v", err)
	}
}

// TestAWindowsCheckoutSurvivesTheWire is the one that decides whether cq runs on
// Windows at all.
//
// Every file there ends its lines with a carriage return, and one file failing
// validation fails the whole snapshot — so a rule that refused CR would not cost
// a file, it would cost the entire mirror on that machine.
func TestAWindowsCheckoutSurvivesTheWire(t *testing.T) {
	f := sound()
	f.Text = "package main\r\n\r\nfunc main() {}\r\n"
	if err := f.Validate(); err != nil {
		t.Fatalf("a CRLF file was refused: %v", err)
	}

	lib := protocol.Library{Root: "Orc", Files: []protocol.File{f}}
	if err := lib.Validate(); err != nil {
		t.Fatalf("a library of CRLF files was refused: %v", err)
	}

	// And an edit of one, which is the same text coming back the other way.
	act := protocol.Action{
		ID: protocol.ActionID(strings.Repeat("a", 32)), Machine: "studio",
		Op: protocol.OpWrite, Queued: at,
		Args: protocol.Args{Path: "main.go", Text: f.Text, Base: strings.Repeat("b", 64)},
	}
	if err := act.Validate(); err != nil {
		t.Fatalf("a write of CRLF text was refused: %v", err)
	}
}

// The characters that are still refused, and the one that never was: a NUL in a
// fixture is what found this rule in the first place.
func TestWhatIsNotText(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		ok   bool
	}{
		{"tab", "a\tb", true},
		{"newline", "a\nb", true},
		{"carriage return", "a\rb", true},
		{"CRLF", "a\r\nb", true},
		{"NUL", "a\x00b", false},
		{"bell", "a\x07b", false},
		{"escape", "a\x1bb", false},
		{"delete", "a\x7fb", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := sound()
			f.Text = tc.text
			err := f.Validate()
			if tc.ok && err != nil {
				t.Errorf("%q was refused: %v", tc.text, err)
			}
			if !tc.ok && err == nil {
				t.Errorf("%q was accepted", tc.text)
			}
		})
	}
}

// TestRemoveTreeCarriesWhatWasSeen.
//
// The manifest is the whole of what makes a recursive delete safe from a mirror
// minutes old, so the wire has to insist on its shape: paths inside the tree,
// bounded in number, and allowed to be empty — a directory holding only empty
// subdirectories shows no files, and that is a fact rather than an omission.
func TestRemoveTreeCarriesWhatWasSeen(t *testing.T) {
	sound := func(args protocol.Args) protocol.Action {
		return protocol.Action{
			ID: protocol.ActionID(strings.Repeat("a", 32)), Machine: "studio",
			Op: protocol.OpRemoveTree, Queued: at, Args: args,
		}
	}

	ok := sound(protocol.Args{Path: "Docs/Old", Paths: []string{"Docs/Old/a.md"}})
	if err := ok.Validate(); err != nil {
		t.Fatalf("a sound rmtree was refused: %v", err)
	}

	// A directory of empty directories has nothing to list.
	empty := sound(protocol.Args{Path: "Docs/Old"})
	if err := empty.Validate(); err != nil {
		t.Errorf("an empty manifest was refused: %v", err)
	}

	// A path that climbs out is refused here rather than at the agent, so a
	// server on one platform cannot pass one to an agent on another.
	out := sound(protocol.Args{Path: "Docs/Old", Paths: []string{"../etc/passwd"}})
	if err := out.Validate(); !errors.Is(err, fault.ErrParse) {
		t.Errorf("a path out of the tree was accepted: %v", err)
	}

	// And the directory itself is still required: a manifest with nothing to
	// remove it from is not a removal.
	none := sound(protocol.Args{Paths: []string{"Docs/Old/a.md"}})
	if err := none.Validate(); !errors.Is(err, fault.ErrParse) {
		t.Errorf("a rmtree with no path was accepted: %v", err)
	}
}

// Nothing else may carry a manifest: an operation whose meaning depends on which
// fields happen to be set is one the queue cannot report on honestly.
func TestOnlyTheVerbsThatTakePathsMayCarryThem(t *testing.T) {
	a := protocol.Action{
		ID: protocol.ActionID(strings.Repeat("a", 32)), Machine: "studio",
		Op: protocol.OpRemoveDir, Queued: at,
		Args: protocol.Args{Path: "Docs/Old", Paths: []string{"Docs/Old/a.md"}},
	}
	if err := a.Validate(); !errors.Is(err, fault.ErrParse) {
		t.Errorf("rmdir was allowed a manifest: %v", err)
	}
}
