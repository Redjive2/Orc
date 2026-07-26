package link_test

import (
	"strings"
	"testing"

	"orc/dock/internal/doc"
	"orc/dock/internal/link"
	"orc/dock/internal/scan"
)

// corpusOf builds a graph from a map of path to document text, which is the
// whole of what a graph needs: no filesystem, no walking.
func corpusOf(t *testing.T, docs map[string]string) link.Graph {
	t.Helper()
	var set []link.Document
	for path, text := range docs {
		r := scan.Scan(text)
		d, err := doc.Build(path, r)
		if err != nil {
			t.Fatalf("Build(%s): %v", path, err)
		}
		e, err := link.Edges(d, r)
		if err != nil {
			t.Fatalf("Edges(%s): %v", path, err)
		}
		set = append(set, link.Document{Path: path, Doc: d, Edges: e})
	}
	return link.Build(set)
}

func TestBacklinks(t *testing.T) {
	g := corpusOf(t, map[string]string{
		"guide.md": "# §1 Guide\n\nSee [g](./grammar.md§2) and [i](§1.1).\n\n" +
			"## §1.1 Install\n\ntext\n",
		"grammar.md": "# §1 Preface\n\ntext\n\n# §2 Grammar\n\nBack to [i](./guide.md§1.1).\n",
		"trouble.md": "# §1 Symptoms\n\nCheck [i](./guide.md§1.1).\n",
	})

	install := link.Node{Path: "guide.md", Number: "1.1"}
	in := g.In(install)
	if len(in) != 3 {
		t.Fatalf("§1.1 has %d backlinks, want 3: %v", len(in), in)
	}
	// A backlink names where it came from, which is the question it answers.
	want := map[string]bool{"guide.md§1": true, "grammar.md§2": true, "trouble.md§1": true}
	for _, a := range in {
		if !want[a.From.String()] {
			t.Errorf("unexpected backlink from %s", a.From)
		}
		delete(want, a.From.String())
	}
	if len(want) != 0 {
		t.Errorf("missing backlinks from %v", want)
	}

	out, inCount := g.Counts(install)
	if out != 0 || inCount != 3 {
		t.Errorf("counts = (%d, %d), want (0, 3)", out, inCount)
	}
}

// TestPathsAreNormalised: ./a.md and a.md are one node, or a backlink count
// depends on how someone happened to write a link.
func TestPathsAreNormalised(t *testing.T) {
	g := corpusOf(t, map[string]string{
		"guide.md": "# §1 A\n\ntext\n",
		"a.md":     "# §1 A2\n\n[x](./guide.md§1)\n",
		"b.md":     "# §1 B\n\n[x](guide.md§1)\n",
		"c.md":     "# §1 C\n\n[x](./sub/../guide.md§1)\n",
	})
	if got := len(g.In(link.Node{Path: "guide.md", Number: "1"})); got != 3 {
		t.Errorf("guide§1 has %d backlinks, want 3 — paths did not normalise", got)
	}
}

func TestRelativePathsResolveFromTheLinkingDocument(t *testing.T) {
	g := corpusOf(t, map[string]string{
		"docs/guide.md":      "# §1 Guide\n\n[up](../top.md§1) and [down](./deep/x.md§1)\n",
		"top.md":             "# §1 Top\n\ntext\n",
		"docs/deep/x.md":     "# §1 Deep\n\ntext\n",
		"docs/deep/other.md": "# §1 Other\n\n[sideways](../guide.md§1)\n",
	})
	for _, tc := range []struct{ path, number string }{
		{"top.md", "1"}, {"docs/deep/x.md", "1"},
	} {
		n := link.Node{Path: tc.path, Number: tc.number}
		if got := len(g.In(n)); got != 1 {
			t.Errorf("%s has %d backlinks, want 1", n, got)
		}
	}
	if got := len(g.In(link.Node{Path: "docs/guide.md", Number: "1"})); got != 1 {
		t.Errorf("docs/guide.md§1 has %d backlinks, want 1", got)
	}
}

func TestDanglingIsReportedWithAReason(t *testing.T) {
	g := corpusOf(t, map[string]string{
		"guide.md": "# §1 A\n\n[gone](§9.9) [nowhere](./missing.md§1) [named](§'Nothing')\n",
	})
	bad := g.Dangling()
	if len(bad) != 3 {
		t.Fatalf("got %d dangling, want 3: %v", len(bad), bad)
	}
	for _, a := range bad {
		if a.Why == "" {
			t.Errorf("dangling arrow %v has no reason", a.Edge)
		}
		if a.Edge.Line() < 1 || a.Edge.Col() < 1 {
			t.Errorf("dangling arrow has no position: %d:%d", a.Edge.Line(), a.Edge.Col())
		}
	}
}

// TestAnnoTargetsAreUncheckedNotDangling: reporting one as broken would send
// someone to fix a document that is correct.
func TestAnnoTargetsAreUncheckedNotDangling(t *testing.T) {
	g := corpusOf(t, map[string]string{
		"guide.md": "# §1 A\n\n[code](../src/example.go@code:Operate)\n",
	})
	if got := len(g.Dangling()); got != 0 {
		t.Errorf("an anno target was called dangling: %v", g.Dangling())
	}
	faults := g.Faults()
	if len(faults) != 1 || faults[0].State != link.Unchecked {
		t.Fatalf("want one unchecked arrow, got %v", faults)
	}
	if faults[0].Why == "" {
		t.Error("an unchecked arrow does not say why")
	}
}

// TestACycleTerminates. A link cycle between documents is legitimate and
// common; nothing in the graph may loop on it.
func TestACycleTerminates(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		g := corpusOf(t, map[string]string{
			"a.md": "# §1 A\n\n[to b](./b.md§1)\n",
			"b.md": "# §1 B\n\n[to c](./c.md§1)\n",
			"c.md": "# §1 C\n\n[to a](./a.md§1)\n",
		})
		for _, p := range []string{"a.md", "b.md", "c.md"} {
			n := link.Node{Path: p, Number: "1"}
			if len(g.Out(n)) != 1 || len(g.In(n)) != 1 {
				t.Errorf("%s: out %d, in %d; want 1 and 1", p, len(g.Out(n)), len(g.In(n)))
			}
		}
		if len(g.Dangling()) != 0 {
			t.Errorf("a cycle was reported as broken: %v", g.Dangling())
		}
	}()
	<-done
}

// TestSelfLink is the degenerate cycle: a section citing itself.
func TestSelfLink(t *testing.T) {
	g := corpusOf(t, map[string]string{"a.md": "# §1 A\n\n[me](§1)\n"})
	n := link.Node{Path: "a.md", Number: "1"}
	if len(g.Out(n)) != 1 || len(g.In(n)) != 1 {
		t.Errorf("out %d, in %d; want 1 and 1", len(g.Out(n)), len(g.In(n)))
	}
}

func TestDocumentsAreSorted(t *testing.T) {
	g := corpusOf(t, map[string]string{
		"c.md": "# §1 C\n", "a.md": "# §1 A\n", "b.md": "# §1 B\n",
	})
	got := g.Documents()
	for i, want := range []string{"a.md", "b.md", "c.md"} {
		if got[i] != want {
			t.Errorf("document %d is %s, want %s", i, got[i], want)
		}
	}
}

func TestNodeStringIsATarget(t *testing.T) {
	n := link.Node{Path: "docs/guide.md", Number: "1.2.1"}
	if got := n.String(); got != "docs/guide.md§1.2.1" {
		t.Errorf("String() = %q", got)
	}
	if got := n.Rel("docs").String(); got != "guide.md§1.2.1" {
		t.Errorf("Rel() = %q", got)
	}
	// A node outside the base keeps its full path rather than growing ../..
	if got := n.Rel("elsewhere").String(); got != "docs/guide.md§1.2.1" {
		t.Errorf("Rel(outside) = %q", got)
	}
}

func TestGraphViewsAreCopies(t *testing.T) {
	g := corpusOf(t, map[string]string{"a.md": "# §1 A\n\n[me](§1)\n"})
	n := link.Node{Path: "a.md", Number: "1"}
	out := g.Out(n)
	out[0] = link.Arrow{}
	if len(g.Out(n)) != 1 || g.Out(n)[0].State != link.Resolved {
		t.Error("Out returned the live slice")
	}
}

func TestAnEmptyGraph(t *testing.T) {
	g := link.Build(nil)
	if len(g.Arrows()) != 0 || len(g.Dangling()) != 0 || len(g.Documents()) != 0 {
		t.Error("an empty corpus produced a non-empty graph")
	}
	if out, in := g.Counts(link.Node{Path: "x", Number: "1"}); out != 0 || in != 0 {
		t.Errorf("counts on an empty graph = (%d, %d)", out, in)
	}
}

// TestRecheckSettlesUncheckedArrows: the graph stays pure while still
// benefiting from a tool it cannot call itself.
func TestRecheckSettlesUncheckedArrows(t *testing.T) {
	g := corpusOf(t, map[string]string{
		"guide.md": "# §1 A\n\n[good](x.go:Operate) [bad](x.go:Missing) [doc](§1)\n",
	})
	if got := len(g.Faults()); got != 2 {
		t.Fatalf("got %d unchecked, want 2", got)
	}

	after := g.Recheck(func(a link.Arrow) (link.State, string) {
		if strings.Contains(a.Edge.To().Chain(), "Missing") {
			return link.Dangling, "no annotation matches"
		}
		return link.Resolved, ""
	})

	if got := len(after.Dangling()); got != 1 {
		t.Fatalf("got %d dangling after recheck, want 1: %v", got, after.Dangling())
	}
	if after.Dangling()[0].Why == "" {
		t.Error("the answer's reason was not carried through")
	}
	// The original is untouched: Recheck returns a new graph.
	if got := len(g.Dangling()); got != 0 {
		t.Errorf("Recheck mutated the graph it was called on: %v", g.Dangling())
	}
}

// TestRecheckLeavesUnknownAlone. "anno is missing" must never become "this link
// is broken".
func TestRecheckLeavesUnknownAlone(t *testing.T) {
	g := corpusOf(t, map[string]string{"a.md": "# §1 A\n\n[c](x.go:Op)\n"})
	after := g.Recheck(func(link.Arrow) (link.State, string) {
		return link.Unchecked, "anno is not on PATH"
	})
	if len(after.Dangling()) != 0 {
		t.Errorf("an unanswerable target was called broken: %v", after.Dangling())
	}
	if len(after.Faults()) != 1 || after.Faults()[0].State != link.Unchecked {
		t.Errorf("the arrow did not stay unchecked: %v", after.Faults())
	}
}

// TestRecheckDoesNotInventNodes: a code target resolves to something outside
// the doc graph, so nothing in the corpus gains a backlink from it.
func TestRecheckDoesNotInventNodes(t *testing.T) {
	g := corpusOf(t, map[string]string{"a.md": "# §1 A\n\n[c](x.go:Op)\n"})
	after := g.Recheck(func(link.Arrow) (link.State, string) { return link.Resolved, "" })
	for _, n := range []link.Node{{Path: "a.md", Number: "1"}, {}} {
		if got := len(after.In(n)); got != 0 {
			t.Errorf("%v gained %d backlinks from a code target", n, got)
		}
	}
}

func TestRecheckWithNoResolver(t *testing.T) {
	g := corpusOf(t, map[string]string{"a.md": "# §1 A\n\n[c](x.go:Op)\n"})
	if got := len(g.Recheck(nil).Faults()); got != 1 {
		t.Errorf("a nil resolver changed the graph")
	}
}
