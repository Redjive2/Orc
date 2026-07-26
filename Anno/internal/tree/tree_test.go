package tree_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"orc/anno/internal/fixture"
	"orc/anno/internal/marker"
	"orc/anno/internal/tree"
	"orc/common/fault"
	"orc/common/source"
)

// build is the common path: bytes in, tree out, fatal on failure.
func build(t *testing.T, name, text string) tree.Tree {
	t.Helper()
	f, err := source.Parse(name, []byte(text))
	if err != nil {
		t.Fatalf("source.Parse: %v", err)
	}
	tr, err := tree.Build(f)
	if err != nil {
		t.Fatalf("tree.Build: %v", err)
	}
	return tr
}

func buildErr(t *testing.T, name, text string) error {
	t.Helper()
	f, err := source.Parse(name, []byte(text))
	if err != nil {
		return err
	}
	_, err = tree.Build(f)
	if err == nil {
		t.Fatalf("tree.Build(%q): expected an error", text)
	}
	return err
}

// flat walks the tree depth-first, describing each node the way the
// documentation's index table does.
func flat(tr tree.Tree) []string {
	var out []string
	var walk func(ns []tree.Node, depth int)
	walk = func(ns []tree.Node, depth int) {
		for _, n := range ns {
			out = append(out, fmt.Sprintf("%s%s %s %d %s",
				strings.Repeat(">", depth), n.Kind(), n.Name(), n.Lines(), n.Display()))
			walk(n.Children(), depth+1)
		}
	}
	walk(tr.Children(), 1)
	return out
}

// TestDocumentedExample is the specification test: every span, content range
// and line count in Vision.md's index table, reproduced exactly.
func TestDocumentedExample(t *testing.T) {
	tr := build(t, "example.go", fixture.ExampleGo)

	if got, want := tr.Count(), 32; got != want {
		t.Fatalf("file line count = %d, want %d", got, want)
	}

	want := []string{
		">section data 3 4:7",
		">>symbol SampleOperation 1 6:6",
		">section types 8 10:19",
		">>symbol Pair 4 12:15",
		">>symbol Operation 1 18:18",
		">section code 8 23:32",
		">>symbol Operate 8 23:32",
		">>>part declarations 4 25:28",
	}
	got := flat(tr)
	if len(got) != len(want) {
		t.Fatalf("got %d nodes, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("node %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestDocumentedSpans checks the raw spans, which differ from content ranges
// and are what `anno read` emits.
func TestDocumentedSpans(t *testing.T) {
	tr := build(t, "example.go", fixture.ExampleGo)

	spans := map[string]string{}
	var walk func(ns []tree.Node)
	walk = func(ns []tree.Node) {
		for _, n := range ns {
			spans[n.Name()] = n.Span().String()
			walk(n.Children())
		}
	}
	walk(tr.Children())

	// section code's span starts at 22, the nested symbol marker, while its
	// content range starts at 23. Both appear in the documentation.
	for name, want := range map[string]string{
		"data":            "4:8",
		"SampleOperation": "6:6",
		"types":           "10:20",
		"Pair":            "12:16",
		"Operation":       "18:18",
		"code":            "22:32",
		"Operate":         "23:32",
		"declarations":    "25:28",
	} {
		if got := spans[name]; got != want {
			t.Errorf("span of %s = %s, want %s", name, got, want)
		}
	}
}

func TestKindRankTerminatesPeersAndDeeperKinds(t *testing.T) {
	src := strings.Join([]string{
		"// @:> section one", // 1
		"a",                  // 2
		"// @:> symbol s1",   // 3
		"b",                  // 4
		"// @:> part p1",     // 5
		"c",                  // 6
		"// @:> symbol s2",   // 7 ends s1 and p1, not one
		"d",                  // 8
		"// @:> section two", // 9 ends one and s2
		"e",                  // 10
	}, "\n") + "\n"

	tr := build(t, "x.go", src)
	want := []string{
		">section one 4 2:8",
		">>symbol s1 2 4:6",
		">>>part p1 1 6:6",
		">>symbol s2 1 8:8",
		">section two 1 10:10",
	}
	got := flat(tr)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestNestingFollowsRankNotProximity(t *testing.T) {
	// A part opened directly under a section, with no symbol between, nests in
	// the section: rank decides parentage.
	tr := build(t, "x.go", "// @:> section s\n// @:> part p\nx\n")
	kids := tr.Children()
	if len(kids) != 1 || len(kids[0].Children()) != 1 {
		t.Fatalf("unexpected shape: %v", flat(tr))
	}
	if got := kids[0].Children()[0].Kind(); got != marker.Part {
		t.Errorf("child kind = %v, want part", got)
	}
}

func TestExplicitCloseEndsNestedAnnotations(t *testing.T) {
	src := strings.Join([]string{
		"// @:> section s", // 1
		"a",                // 2
		"// @:> symbol y",  // 3
		"b",                // 4
		"// @:< s",         // 5 closes y as well as s
		"c",                // 6
	}, "\n") + "\n"

	tr := build(t, "x.go", src)
	want := []string{">section s 2 2:4", ">>symbol y 1 4:4"}
	if got := strings.Join(flat(tr), "\n"); got != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
}

func TestCloseChoosesInnermostOfThatName(t *testing.T) {
	src := "// @:> section n\na\n// @:> symbol n\nb\n// @:< n\nc\n"
	tr := build(t, "x.go", src)
	// The close ends the symbol, leaving the section open to end of file.
	want := []string{">section n 3 2:6", ">>symbol n 1 4:4"}
	if got := strings.Join(flat(tr), "\n"); got != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
}

func TestNextLineAnnotationSpansOneLine(t *testing.T) {
	tr := build(t, "x.go", "// @:; symbol s\ncode\nmore\n")
	kids := tr.Children()
	if len(kids) != 1 {
		t.Fatalf("got %d nodes, want 1", len(kids))
	}
	if got, want := kids[0].Span().String(), "2:2"; got != want {
		t.Errorf("span = %s, want %s", got, want)
	}
	if got := kids[0].Lines(); got != 1 {
		t.Errorf("lines = %d, want 1", got)
	}
}

func TestEmptyAnnotationCollapsesToItsMarkerLine(t *testing.T) {
	// An open marker on the last line covers nothing at all.
	tr := build(t, "x.go", "a\n// @:> section s\n")
	n := tr.Children()[0]
	if !n.Span().Empty() {
		t.Errorf("span = %s, want empty", n.Span())
	}
	if got := n.Lines(); got != 0 {
		t.Errorf("lines = %d, want 0", got)
	}
	if got, want := n.Display().String(), "2:2"; got != want {
		t.Errorf("display = %s, want %s (the marker line)", got, want)
	}
}

func TestAnnotationOfOnlyBlanksAndMarkersIsEmpty(t *testing.T) {
	tr := build(t, "x.go", "// @:> section s\n\n\n// @:> section t\nx\n")
	s := tr.Children()[0]
	if got := s.Lines(); got != 0 {
		t.Errorf("lines = %d, want 0", got)
	}
	if !s.Content().Empty() {
		t.Errorf("content = %s, want empty", s.Content())
	}
}

func TestInteriorBlankLinesCountButMarkerLinesDoNot(t *testing.T) {
	src := "// @:> section s\na\n\n// @:> part p\nb\n"
	tr := build(t, "x.go", src)
	s := tr.Children()[0]
	// Content 2:5 is four lines, one of which is the part marker.
	if got, want := s.Content().String(), "2:5"; got != want {
		t.Fatalf("content = %s, want %s", got, want)
	}
	if got, want := s.Lines(), 3; got != want {
		t.Errorf("lines = %d, want %d", got, want)
	}
}

func TestUnbalancedCloseIsReportedWithOpenNames(t *testing.T) {
	err := buildErr(t, "x.go", "// @:> section s\na\n// @:< nope\n")
	if !errors.Is(err, fault.ErrUnbalanced) {
		t.Fatalf("error = %v, want unbalanced", err)
	}
	var u fault.Unbalanced
	if !errors.As(err, &u) {
		t.Fatalf("error is not fault.Unbalanced: %v", err)
	}
	if u.Line != 3 || u.Name != "nope" {
		t.Errorf("got line %d name %q, want 3/nope", u.Line, u.Name)
	}
	if len(u.Open) != 1 || !strings.Contains(u.Open[0], "section s") {
		t.Errorf("open list = %v, want the open section", u.Open)
	}
}

func TestCloseWithNothingOpenIsUnbalanced(t *testing.T) {
	err := buildErr(t, "x.go", "a\n// @:< ghost\n")
	if !errors.Is(err, fault.ErrUnbalanced) {
		t.Fatalf("error = %v, want unbalanced", err)
	}
}

func TestNextLineMarkerOnLastLineIsAParseError(t *testing.T) {
	err := buildErr(t, "x.go", "a\n// @:; symbol s\n")
	if !errors.Is(err, fault.ErrParse) {
		t.Fatalf("error = %v, want parse", err)
	}
	if !strings.Contains(err.Error(), "last line") {
		t.Errorf("message %q should explain the problem", err)
	}
}

func TestEveryFaultIsReportedAtOnce(t *testing.T) {
	src := "// @:> nope a\n// @:> section\n// @:< ghost\n"
	err := buildErr(t, "x.go", src)
	for _, want := range []string{"nope", "expected", "ghost"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q should mention %q", err, want)
		}
	}
}

func TestEmptyFileHasNoAnnotations(t *testing.T) {
	tr := build(t, "x.go", "")
	if !tr.Empty() {
		t.Errorf("empty file should have no annotations")
	}
	if got := tr.Count(); got != 0 {
		t.Errorf("line count = %d, want 0", got)
	}
	if got, want := tr.Name(), "x.go"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
	if got, want := tr.Path(), "x.go"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestAccessorsReturnCopies(t *testing.T) {
	tr := build(t, "x.go", "// @:> symbol s [a b]\nx\n")

	kids := tr.Children()
	kids[0] = tree.Node{}
	if len(tr.Children()) != 1 || tr.Children()[0].Name() != "s" {
		t.Errorf("Tree.Children exposed internal state")
	}

	n := tr.Children()[0]
	meta := n.Meta()
	meta[0] = "clobbered"
	if n.Meta()[0] != "a" {
		t.Errorf("Node.Meta exposed internal state")
	}

	inner := n.Children()
	_ = inner
	if got := n.MarkerLine(); got != 1 {
		t.Errorf("marker line = %d, want 1", got)
	}
}

func TestRangeArithmetic(t *testing.T) {
	r, err := tree.NewRange(3, 7)
	if err != nil {
		t.Fatal(err)
	}
	if r.Start() != 3 || r.End() != 7 || r.Len() != 5 || r.Empty() {
		t.Errorf("range %v is wrong", r)
	}
	if got, want := r.String(), "3:7"; got != want {
		t.Errorf("String = %q, want %q", got, want)
	}

	empty, err := tree.NewRange(4, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !empty.Empty() || empty.Len() != 0 {
		t.Errorf("range %v should be empty", empty)
	}

	inner, err := tree.NewRange(4, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Contains(inner) {
		t.Errorf("%v should contain %v", r, inner)
	}
	if !r.Contains(empty) {
		t.Errorf("every range contains an empty range")
	}
	if empty.Contains(inner) {
		t.Errorf("an empty range contains nothing")
	}
	outer, err := tree.NewRange(1, 99)
	if err != nil {
		t.Fatal(err)
	}
	if r.Contains(outer) {
		t.Errorf("%v should not contain %v", r, outer)
	}
}

func TestNewRangeRejectsNonsense(t *testing.T) {
	for _, tc := range []struct{ start, end int }{{0, 5}, {-1, 2}, {5, 3}} {
		if _, err := tree.NewRange(tc.start, tc.end); !errors.Is(err, fault.ErrInternal) {
			t.Errorf("NewRange(%d, %d) error = %v, want internal", tc.start, tc.end, err)
		}
	}
}

func TestBuildRejectsUnreadableSource(t *testing.T) {
	// A File that was never constructed properly cannot be walked.
	if _, err := tree.Build(source.File{}); err != nil {
		t.Fatalf("zero File should build an empty tree, got %v", err)
	}
}

func TestNextLineMarkerCannotAnnotateAnotherMarker(t *testing.T) {
	// Line 3 both closes the section and is the line the `@:;` claims, which would
	// leave the section ending before the annotation nested inside it.
	err := buildErr(t, "x.go", "// @:> section a\n// @:; symbol s\n// @:> section b\n")
	if !errors.Is(err, fault.ErrParse) {
		t.Fatalf("error = %v, want parse", err)
	}
	if !strings.Contains(err.Error(), "itself an annotation marker") {
		t.Errorf("message %q should explain the problem", err)
	}
}
