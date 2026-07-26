package model

import "testing"

// The workspace root is a legitimate target: it is the directory the agent works
// in, and `ls` names it. It reached the matcher through cleanGlob, which refuses
// `.` as a *pattern* — correctly, since a pattern naming the root selects nothing
// — so every clause failed on it and an agent holding `read(**)` could not list
// its own workspace.
//
// What makes the fix safe is that it grants nothing new below the root: `**`
// already covers everything inside, so covering the directory entry as well adds
// no reachable file. A clause scoped to one directory still does not cover the
// one above it, which is the case worth being sure of.

func TestTheWholeWorkspaceCoversTheWorkspaceItself(t *testing.T) {
	for _, spelling := range []string{".", "", "./"} {
		p, err := ParsePattern("read(**)")
		if err != nil {
			t.Fatal(err)
		}
		if !p.Matches(spelling) {
			t.Errorf("read(**) does not cover the workspace root spelled %q, "+
				"so the agent cannot list the directory it works in", spelling)
		}
	}
}

func TestWriteToTheWholeWorkspaceCoversItToo(t *testing.T) {
	p, err := ParsePattern("write(**)")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Matches(".") {
		t.Error("write(**) does not cover the workspace root")
	}
}

// The one that must not regress. An agent given one directory may not list the
// workspace above it — otherwise this fix would be a quiet widening of every
// scoped permission in every fleet.
func TestADirectoryClauseDoesNotCoverTheWorkspaceAboveIt(t *testing.T) {
	for _, clause := range []string{"read(Docs/**)", "read(Docs/)", "read(Anno/**)", "write(Docs/**)"} {
		p, err := ParsePattern(clause)
		if err != nil {
			t.Fatal(err)
		}
		if p.Matches(".") {
			t.Errorf("%s covers the workspace root, which widens every scoped permission", clause)
		}
	}
}

// An exception still refuses, at the root as anywhere else.
func TestAnExceptionStillRefusesTheRoot(t *testing.T) {
	p, err := ParsePattern("read(** except **)")
	if err != nil {
		t.Skipf("this fleet does not spell exceptions that way: %v", err)
	}
	if p.Matches(".") {
		t.Error("an exception covering everything did not refuse the root")
	}
}

// And nothing below the root changed.
func TestOrdinaryPathsAreUnaffected(t *testing.T) {
	p, err := ParsePattern("read(Docs/**)")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Matches("Docs/x.md") {
		t.Error("read(Docs/**) stopped matching a file inside Docs")
	}
	if p.Matches("Orc/x.go") {
		t.Error("read(Docs/**) started matching outside Docs")
	}
}
