package link_test

import (
	"errors"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/dock/internal/doc"
	"orc/dock/internal/link"
	"orc/dock/internal/scan"
	"orc/dock/internal/target"
)

func build(t *testing.T, text string) (doc.Doc, scan.Result) {
	t.Helper()
	r := scan.Scan(text)
	d, err := doc.Build("guide.md", r)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return d, r
}

func edges(t *testing.T, text string) []link.Edge {
	t.Helper()
	d, r := build(t, text)
	e, err := link.Edges(d, r)
	if err != nil {
		t.Fatalf("Edges: %v", err)
	}
	return e
}

const corpus = `A preamble link to [the grammar](./g.md§2.1).

# §1 Guide

Intro, citing [Install](§1.1).

## §1.1 Install

Run it. See [anno](../Anno/example.go@code:Operate) and
[the site](https://example.com) and [a plain doc](./other.md).

## §1.2 Sections

Nothing here links anywhere.
`

func TestAttribution(t *testing.T) {
	got := edges(t, corpus)
	want := []struct {
		from  string
		to    string
		label string
	}{
		{link.Root, "./g.md§2.1", "the grammar"},
		{"1", "§1.1", "Install"},
		{"1.1", "../Anno/example.go@code:Operate", "anno"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d edges, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].From() != w.from {
			t.Errorf("edge %d from %q, want %q", i, got[i].From(), w.from)
		}
		if got[i].To().String() != w.to {
			t.Errorf("edge %d to %q, want %q", i, got[i].To().String(), w.to)
		}
		if got[i].Label() != w.label {
			t.Errorf("edge %d label %q, want %q", i, got[i].Label(), w.label)
		}
	}
}

// TestOrdinaryLinksAreNotEdges: a URL and a plain path are markdown, not
// addresses, and a graph that filled with them would be unreadable.
func TestOrdinaryLinksAreNotEdges(t *testing.T) {
	got := edges(t, "# §1 A\n\n[a](https://x.example) [b](./other.md) [c](#anchor)\n")
	if len(got) != 0 {
		t.Errorf("got %d edges, want none: %v", len(got), got)
	}
}

// TestLinksInCodeAreNotEdges is scan's guarantee arriving intact: Dock's own
// documentation is full of example links inside fences.
func TestLinksInCodeAreNotEdges(t *testing.T) {
	got := edges(t, "# §1 A\n\n```\n[x](§1)\n```\n\n`[y](§1)`\n\n<!-- [z](§1) -->\n")
	if len(got) != 0 {
		t.Errorf("got %d edges, want none: %v", len(got), got)
	}
}

// TestAHeadingsLinkBelongsToItsOwnSection: attributing it to the predecessor
// would credit a citation to the wrong part of the document.
func TestAHeadingsLinkBelongsToItsOwnSection(t *testing.T) {
	got := edges(t, "# §1 A\n\ntext\n\n## §1.1 See [B](§1)\n\ntext\n")
	if len(got) != 1 {
		t.Fatalf("got %d edges, want 1", len(got))
	}
	if got[0].From() != "1.1" {
		t.Errorf("from = %q, want 1.1 — the section the heading opens", got[0].From())
	}
}

func TestLinkBeforeAnySectionIsRooted(t *testing.T) {
	got := edges(t, "[a](§1)\n\n# §1 A\n")
	if len(got) != 1 {
		t.Fatalf("got %d edges, want 1", len(got))
	}
	if got[0].From() != link.Root {
		t.Errorf("from = %q, want the root", got[0].From())
	}
}

func TestSameFileResolution(t *testing.T) {
	d, r := build(t, corpus)
	e, err := link.Edges(d, r)
	if err != nil {
		t.Fatal(err)
	}
	var resolved int
	for _, edge := range e {
		if !edge.SameFile() {
			continue
		}
		s, ok := link.Resolve(d, edge.To())
		if !ok {
			t.Errorf("same-file edge %v did not resolve", edge)
			continue
		}
		if s.Number() != "1.1" || s.Name() != "Install" {
			t.Errorf("resolved to §%s %q, want §1.1 Install", s.Number(), s.Name())
		}
		resolved++
	}
	if resolved != 1 {
		t.Errorf("resolved %d same-file edges, want 1", resolved)
	}
}

func TestResolveByName(t *testing.T) {
	d, _ := build(t, "# §1 Guide\n\n## §1.1 Install\n\ntext\n")
	for _, dest := range []string{"§'Install'", "§'install'", "§'  INSTALL '", "§1.1"} {
		out, ok, err := target.Parse(dest)
		if err != nil || !ok {
			t.Fatalf("Parse(%q): ok=%v err=%v", dest, ok, err)
		}
		s, ok := link.Resolve(d, out[0])
		if !ok {
			t.Errorf("Resolve(%q) missed", dest)
			continue
		}
		if s.Number() != "1.1" {
			t.Errorf("Resolve(%q) = §%s, want §1.1", dest, s.Number())
		}
	}
}

func TestResolveRefusesCrossFile(t *testing.T) {
	d, _ := build(t, "# §1 A\n")
	out, _, err := target.Parse("other.md§1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := link.Resolve(d, out[0]); ok {
		t.Error("Resolve answered a cross-file target, which it cannot know")
	}
}

func TestDangling(t *testing.T) {
	text := "# §1 A\n\n" +
		"[good](§1) [bad number](§9) [bad name](§'Nowhere')\n" +
		"[elsewhere](other.md§1) [code](x.go:Sym)\n"
	d, r := build(t, text)
	e, err := link.Edges(d, r)
	if err != nil {
		t.Fatal(err)
	}
	bad := link.SameFileDangling(d, e)
	if len(bad) != 2 {
		t.Fatalf("got %d dangling, want 2: %v", len(bad), bad)
	}
	for _, b := range bad {
		if l := b.Label(); l != "bad number" && l != "bad name" {
			t.Errorf("reported %q as dangling", l)
		}
	}
}

// TestCrossFileAndAnnoAreNotJudged: reporting a link as broken because the tool
// that resolves it was not consulted would send someone to fix a correct file.
func TestCrossFileAndAnnoAreNotJudged(t *testing.T) {
	d, r := build(t, "# §1 A\n\n[a](nope.md§4) [b](nothing.go:Missing)\n")
	e, _ := link.Edges(d, r)
	if got := link.SameFileDangling(d, e); len(got) != 0 {
		t.Errorf("judged %d unresolvable-here edges: %v", len(got), got)
	}
}

func TestMalformedDestinationsAreReportedWithPosition(t *testing.T) {
	d, r := build(t, "# §1 A\n\ntext [bad](§1..2) more\n")
	_, err := link.Edges(d, r)
	if err == nil {
		t.Fatal("expected a fault")
	}
	if !errors.Is(err, fault.ErrParse) {
		t.Errorf("not a parse fault: %v", err)
	}
	if !strings.Contains(err.Error(), "guide.md:3:6") {
		t.Errorf("fault does not carry line and column: %v", err)
	}
}

func TestEveryMalformedDestinationIsReported(t *testing.T) {
	d, r := build(t, "# §1 A\n\n[a](§1..2)\n[b](§0)\n[c](§'x)\n")
	_, err := link.Edges(d, r)
	if err == nil {
		t.Fatal("expected faults")
	}
	if got := len(strings.Split(err.Error(), "\n")); got != 3 {
		t.Errorf("reported %d faults, want 3:\n%v", got, err)
	}
}

func TestEdgesCarryEveryReading(t *testing.T) {
	got := edges(t, "# §1 A\n\n[a](x.go@sec:sym)\n")
	if len(got) != 1 {
		t.Fatalf("got %d edges", len(got))
	}
	rs := got[0].Readings()
	if len(rs) < 2 {
		t.Fatalf("got %d readings, want the split to be undecided: %v", len(rs), rs)
	}
	if rs[0].Path() != "x.go@sec" {
		t.Errorf("first reading path = %q, want the longest", rs[0].Path())
	}
	// Mutating the copy must not reach the edge.
	rs[0] = target.Target{}
	if got[0].Readings()[0].Path() != "x.go@sec" {
		t.Error("Readings returned the live slice")
	}
}

func TestString(t *testing.T) {
	got := edges(t, "[a](§1)\n\n# §1 A\n\n[b](§1)\n")
	if s := got[0].String(); s != "(root) -> §1 [a]" {
		t.Errorf("root edge renders as %q", s)
	}
	if s := got[1].String(); s != "§1 -> §1 [b]" {
		t.Errorf("section edge renders as %q", s)
	}
}

// TestEveryRenderedTargetIsPasteable: a line Dock prints that looks like a
// target must be one.
func TestEveryRenderedTargetIsPasteable(t *testing.T) {
	for _, e := range edges(t, corpus) {
		s := e.To().String()
		out, ok, err := target.Parse(s)
		if err != nil || !ok {
			t.Errorf("rendered target %q does not parse back: ok=%v err=%v", s, ok, err)
			continue
		}
		if out[0].String() != s {
			t.Errorf("rendered target %q parses back to %q", s, out[0].String())
		}
	}
}

func FuzzEdges(f *testing.F) {
	for _, s := range []string{
		corpus, "# §1 A\n[a](§1)\n", "[a](§1)\n", "# §1 A\n[a](https://x)\n",
		"# §1 A\n[a](§1..2)\n", "```\n[a](§1)\n```\n",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		r := scan.Scan(in)
		d, err := doc.Build("f.md", r)
		if err != nil {
			return
		}
		e, err := link.Edges(d, r)
		if err != nil {
			if errors.Is(err, fault.ErrInternal) {
				t.Fatalf("internal fault: %v", err)
			}
			return
		}

		sections := d.Sections()
		for _, edge := range e {
			if len(edge.Readings()) == 0 {
				t.Fatal("an edge carries no readings")
			}
			if edge.Line() < 1 || edge.Line() > d.Lines() {
				t.Errorf("edge on line %d of %d", edge.Line(), d.Lines())
			}
			// The attributed section must actually precede the link.
			if edge.From() != link.Root {
				s, ok := d.ByNumber(edge.From())
				if !ok {
					t.Fatalf("edge attributed to §%s, which does not exist", edge.From())
				}
				if s.Head() > edge.Line() {
					t.Errorf("edge on line %d attributed to §%s, which heads line %d", edge.Line(), s.Number(), s.Head())
				}
			} else if len(sections) > 0 && sections[0].Head() <= edge.Line() {
				t.Errorf("edge on line %d rooted, but §%s heads line %d", edge.Line(), sections[0].Number(), sections[0].Head())
			}
		}
		// Dangling only ever reports same-file section edges.
		for _, b := range link.SameFileDangling(d, e) {
			if !b.SameFile() || b.To().Kind() != target.Section {
				t.Errorf("judged an edge it cannot check: %v", b)
			}
		}
	})
}
