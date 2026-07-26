package doc_test

import (
	"errors"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/dock/internal/doc"
	"orc/dock/internal/scan"
)

func build(t *testing.T, text string) doc.Doc {
	t.Helper()
	d, err := doc.Build("guide.md", scan.Scan(text))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return d
}

// guide is the worked example from the plan, and the golden case for spans.
const guide = `# §1 Guide

Intro prose.

## §1.1 Install

Run ` + "`go install ./cmd/dock`" + `.

## §1.2 Sections

A section is a heading carrying a number.

### §1.2.1 Numbering

The number's depth matches the heading's depth.

## §1.3 Troubleshooting

Start with Install.
`

func TestTheWorkedExample(t *testing.T) {
	d := build(t, guide)
	if d.Len() != 5 {
		t.Fatalf("got %d sections, want 5", d.Len())
	}
	for _, tc := range []struct {
		number string
		name   string
		depth  int
		head   int
		own    string
		tree   string
	}{
		{"1", "Guide", 1, 1, "<2:4>", "<2:19>"},
		{"1.1", "Install", 2, 5, "<6:8>", "<6:8>"},
		{"1.2", "Sections", 2, 9, "<10:12>", "<10:16>"},
		{"1.2.1", "Numbering", 3, 13, "<14:16>", "<14:16>"},
		{"1.3", "Troubleshooting", 2, 17, "<18:19>", "<18:19>"},
	} {
		t.Run(tc.number, func(t *testing.T) {
			s, ok := d.ByNumber(tc.number)
			if !ok {
				t.Fatalf("§%s not found", tc.number)
			}
			if s.Name() != tc.name {
				t.Errorf("name = %q, want %q", s.Name(), tc.name)
			}
			if s.Depth() != tc.depth {
				t.Errorf("depth = %d, want %d", s.Depth(), tc.depth)
			}
			if s.Head() != tc.head {
				t.Errorf("head = %d, want %d", s.Head(), tc.head)
			}
			if got := s.Own().String(); got != tc.own {
				t.Errorf("own = %s, want %s", got, tc.own)
			}
			if got := s.Tree().String(); got != tc.tree {
				t.Errorf("tree = %s, want %s", got, tc.tree)
			}
		})
	}
}

// TestOwnStopsAtTheFirstSubsection is the whole reason read defaults to the own
// span: asking for §1 in a chapter must not return the chapter.
func TestOwnStopsAtTheFirstSubsection(t *testing.T) {
	d := build(t, guide)
	top, _ := d.ByNumber("1")
	if got, want := top.Own().Len(), 3; got != want {
		t.Errorf("own span is %d lines, want %d — it should stop at §1.1", got, want)
	}
	if top.Tree().Len() <= top.Own().Len() {
		t.Error("the tree span should be the larger of the two")
	}
}

// TestTheHeadingIsNeverInASpan. The heading is structure, not content: write
// must be unable to destroy it or renumber the document.
func TestTheHeadingIsNeverInASpan(t *testing.T) {
	d := build(t, guide)
	for _, s := range d.Sections() {
		if !s.Own().Empty() && s.Own().Start() <= s.Head() {
			t.Errorf("§%s own span starts at its heading", s.Number())
		}
		if !s.Tree().Empty() && s.Tree().Start() <= s.Head() {
			t.Errorf("§%s tree span starts at its heading", s.Number())
		}
		for _, k := range d.Children(s) {
			if !s.Own().Empty() && s.Own().End() >= k.Head() {
				t.Errorf("§%s own span reaches its child's heading", s.Number())
			}
		}
	}
}

func TestUnmarkedHeadingsAreProse(t *testing.T) {
	d := build(t, "# §1 Real\n\ntext\n\n## Not A Section\n\nmore\n")
	if d.Len() != 1 {
		t.Fatalf("got %d sections, want 1", d.Len())
	}
	s, _ := d.ByNumber("1")
	// The unmarked heading and everything under it stays inside §1.
	if got := s.Own().End(); got != 7 {
		t.Errorf("own span ends at %d, want 7 — an unmarked heading is prose", got)
	}
}

func TestDocumentWithNoSections(t *testing.T) {
	d := build(t, "# Title\n\njust prose\n")
	if d.Len() != 0 {
		t.Errorf("got %d sections, want none", d.Len())
	}
	if d.Lines() != 3 {
		t.Errorf("lines = %d, want 3", d.Lines())
	}
}

func TestStructuralFaults(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string // substring of the reported reason
	}{
		{"depth too deep", "# §1 A\n## §1.1.1 B\n", "components under"},
		{"depth too shallow", "# §1 A\n### §1.1 B\n", "components under"},
		{"no parent", "### §1.1.1 B\n", "no open parent"},
		{"gap", "# §1 A\n## §1.1 B\n## §1.3 C\n", "out of sequence"},
		{"repeat", "# §1 A\n## §1.1 B\n## §1.1 C\n", "out of sequence"},
		{"out of order", "# §1 A\n## §1.2 B\n", "out of sequence"},
		{"top level gap", "# §2 A\n", "out of sequence"},
		{"duplicate name", "# §1 A\n## §1.1 Install\n## §1.2 install\n", "already declared"},
		{"no number", "# § Name\n", "needs a number"},
		{"no name", "# §1\n", "has no name"},
		{"zero", "# §0 A\n", "numbers sections from 1"},
		{"leading zero", "# §01 A\n", "leading zero"},
		{"empty component", "# §1..1 A\n", "empty component"},
		{"not a number", "# §1.x A\n", "not a number"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := doc.Build("guide.md", scan.Scan(tc.in))
			if err == nil {
				t.Fatal("expected a fault")
			}
			if !errors.Is(err, fault.ErrParse) {
				t.Errorf("error is not a parse fault: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("reason %q does not mention %q", err.Error(), tc.want)
			}
		})
	}
}

// TestEveryFaultIsReported: a document with four mistakes is fixed in one round
// trip, not four.
func TestEveryFaultIsReported(t *testing.T) {
	_, err := doc.Build("guide.md", scan.Scan("# §1 A\n## §1.2 B\n## §1.5 C\n## §1.9 D\n"))
	if err == nil {
		t.Fatal("expected faults")
	}
	if got := strings.Count(err.Error(), "out of sequence"); got != 3 {
		t.Errorf("reported %d sequence faults, want 3:\n%v", got, err)
	}
}

func TestFaultsCarryTheirLine(t *testing.T) {
	_, err := doc.Build("guide.md", scan.Scan("# §1 A\n\n\n## §1.3 B\n"))
	if err == nil {
		t.Fatal("expected a fault")
	}
	if !strings.Contains(err.Error(), "guide.md:4") {
		t.Errorf("fault does not carry its position: %v", err)
	}
}

func TestByName(t *testing.T) {
	d := build(t, guide)
	for _, name := range []string{"Install", "install", "  INSTALL  ", "iNsTaLl"} {
		if _, ok := d.ByName(name); !ok {
			t.Errorf("ByName(%q) missed", name)
		}
	}
	if _, ok := d.ByName("nothing here"); ok {
		t.Error("ByName found a section that does not exist")
	}
}

func TestByNumberAcceptsTheSigil(t *testing.T) {
	d := build(t, guide)
	for _, n := range []string{"1.2.1", "§1.2.1", " §1.2.1 "} {
		if _, ok := d.ByNumber(n); !ok {
			t.Errorf("ByNumber(%q) missed", n)
		}
	}
}

func TestChildren(t *testing.T) {
	d := build(t, guide)
	top, _ := d.ByNumber("1")
	kids := d.Children(top)
	if len(kids) != 3 {
		t.Fatalf("§1 has %d children, want 3", len(kids))
	}
	for i, want := range []string{"1.1", "1.2", "1.3"} {
		if kids[i].Number() != want {
			t.Errorf("child %d is §%s, want §%s", i, kids[i].Number(), want)
		}
	}
	if got := len(d.Children(kids[1])); got != 1 {
		t.Errorf("§1.2 has %d children, want 1", got)
	}
}

// TestChildLinksSurviveGrowth guards the aliasing mistake this builder invites:
// holding a pointer into the section slice while appending to it.
func TestChildLinksSurviveGrowth(t *testing.T) {
	var b strings.Builder
	b.WriteString("# §1 Root\n")
	for i := 1; i <= 64; i++ {
		b.WriteString("## §1." + itoa(i) + " Child " + itoa(i) + "\n")
	}
	d := build(t, b.String())
	root, _ := d.ByNumber("1")
	if got := len(d.Children(root)); got != 64 {
		t.Errorf("root has %d children, want 64", got)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}

func TestSectionsInFencesAreInvisible(t *testing.T) {
	d := build(t, "# §1 Real\n\n```\n## §1.1 Fake\n```\n\ntext\n")
	if d.Len() != 1 {
		t.Errorf("got %d sections, want 1 — a fenced heading is not a section", d.Len())
	}
}

func TestContentTrimsBlankLines(t *testing.T) {
	text := "# §1 A\n\n\nbody\n\n\n"
	r := scan.Scan(text)
	d, err := doc.Build("guide.md", r)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := d.ByNumber("1")
	got := d.Content(r.Lines(), s.Own())
	if got.Start() != 4 || got.End() != 4 {
		t.Errorf("content = %s, want <4:4>", got)
	}
}

func TestDepthLimit(t *testing.T) {
	// Seven #s is not a heading at all, so the depth rule can never be
	// satisfied past six — the limit is markdown's, not Dock's.
	d := build(t, "# §1 A\n## §1.1 B\n### §1.1.1 C\n#### §1.1.1.1 D\n##### §1.1.1.1.1 E\n###### §1.1.1.1.1.1 F\n")
	if d.Len() != 6 {
		t.Errorf("got %d sections, want 6", d.Len())
	}
}

func TestSectionsAreCopies(t *testing.T) {
	d := build(t, guide)
	got := d.Sections()
	got[0] = doc.Section{}
	if s, _ := d.At(0); s.Number() != "1" {
		t.Error("mutating the returned slice changed the document")
	}
	s, _ := d.ByNumber("1")
	parts := s.Parts()
	parts[0] = 99
	if s.Number() != "1" {
		t.Error("mutating Parts changed the section")
	}
}

func FuzzBuild(f *testing.F) {
	for _, s := range []string{
		guide, "# §1 A\n", "## §1.1 B\n", "# §1 A\n## §1.1 B\n### §1.1.1 C\n",
		"# Title\n", "# §1..1 A\n", "```\n# §1 A\n```\n",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		r := scan.Scan(in)
		d, err := doc.Build("f.md", r)
		if err != nil {
			// A failure must be a parse fault, never an internal one: an
			// internal fault means the builder has a hole in it.
			if errors.Is(err, fault.ErrInternal) {
				t.Fatalf("Build reported an internal fault: %v", err)
			}
			return
		}

		lines := d.Lines()
		for i, s := range d.Sections() {
			if s.Depth() != s.Level() {
				t.Errorf("§%s: depth %d, level %d", s.Number(), s.Depth(), s.Level())
			}
			if s.Head() < 1 || s.Head() > lines {
				t.Errorf("§%s heads line %d of %d", s.Number(), s.Head(), lines)
			}
			for _, rg := range []doc.Range{s.Own(), s.Tree()} {
				if rg.Empty() {
					continue
				}
				if rg.Start() <= s.Head() {
					t.Errorf("§%s span %s includes its heading", s.Number(), rg)
				}
				if rg.End() > lines {
					t.Errorf("§%s span %s runs past the document", s.Number(), rg)
				}
			}
			// Own never outruns tree.
			if !s.Own().Empty() && !s.Tree().Empty() && s.Own().End() > s.Tree().End() {
				t.Errorf("§%s own %s outruns tree %s", s.Number(), s.Own(), s.Tree())
			}
			// Every accepted section is addressable by both of its handles.
			if _, ok := d.ByNumber(s.Number()); !ok {
				t.Errorf("§%s is not addressable by number", s.Number())
			}
			if _, ok := d.ByName(s.Name()); !ok {
				t.Errorf("§%s is not addressable by its name %q", s.Number(), s.Name())
			}
			if i > 0 {
				if prev, _ := d.At(i - 1); prev.Head() >= s.Head() {
					t.Errorf("§%s is out of document order", s.Number())
				}
			}
		}
	})
}
