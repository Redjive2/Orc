package render_test

import (
	"regexp"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/dock/internal/doc"
	"orc/dock/internal/fixture"
	"orc/dock/internal/link"
	"orc/dock/internal/render"
	"orc/dock/internal/scan"
	"orc/dock/internal/style"
)

// guideIndex builds the fixture's index with the counts the whole corpus
// implies, which is what the golden constant is measured against.
func guideIndex(t *testing.T) (render.Index, []scan.Line) {
	t.Helper()
	r := scan.Scan(fixture.Guide)
	d, err := doc.Build("guide.md", r)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	edges, err := link.Edges(d, r)
	if err != nil {
		t.Fatalf("Edges: %v", err)
	}

	counts := map[string]render.Counts{}
	for _, s := range d.Sections() {
		counts[s.Number()] = render.Counts{}
	}
	for _, e := range edges {
		if e.From() == link.Root {
			continue
		}
		c := counts[e.From()]
		c.Out++
		counts[e.From()] = c
	}
	// Inbound counts come from the rest of the corpus: guide§1.1 is cited by
	// Trouble and by Guide itself; guide§1.2.1 by Grammar.
	for n, in := range map[string]int{"1.1": 2, "1.2.1": 1} {
		c := counts[n]
		c.In = in
		counts[n] = c
	}

	ix, err := render.BuildIndex(d, r.Lines(), counts)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	return ix, r.Lines()
}

// TestTheGoldenIndex is the golden test for the whole rendering layer.
func TestTheGoldenIndex(t *testing.T) {
	ix, _ := guideIndex(t)
	got := render.Table(ix, style.Plain())
	if got != fixture.GuideIndex {
		t.Errorf("index does not match the golden:\n--- got ---\n%s\n--- want ---\n%s", got, fixture.GuideIndex)
	}
}

var escapes = regexp.MustCompile("\x1b\\[[0-9;]*m")

// TestColourDoesNotMoveAnything. Escape sequences occupy no columns, so a
// coloured table must strip back to the plain one byte for byte. If this fails,
// the layout pass has seen a colour code.
func TestColourDoesNotMoveAnything(t *testing.T) {
	ix, _ := guideIndex(t)
	plain := render.Table(ix, style.Plain())
	coloured := render.Table(ix, style.Coloured())

	if coloured == plain {
		t.Fatal("the coloured palette emitted no sequences")
	}
	if stripped := escapes.ReplaceAllString(coloured, ""); stripped != plain {
		t.Errorf("colour changed the layout:\n--- stripped ---\n%s\n--- plain ---\n%s", stripped, plain)
	}
}

// TestEveryRowIsTheSameWidth: a table whose rows disagree is a sheared table,
// and the usual cause is measuring bytes where columns were meant.
func TestEveryRowIsTheSameWidth(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
	}{
		{"the fixture", fixture.Guide},
		{"no sections", "just prose\n"},
		{"one section", "# §1 A\n"},
		{"empty document", ""},
		{"long name", "# §1 " + strings.Repeat("very long name ", 8) + "\n"},
		{"cjk name", "# §1 日本語のセクション名\n"},
		{"combining marks", "# §1 e\u0301e\u0301e\u0301\n"},
		{"deep", "# §1 A\n## §1.1 B\n### §1.1.1 C\n#### §1.1.1.1 D\n##### §1.1.1.1.1 E\n###### §1.1.1.1.1.1 F\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := scan.Scan(tc.text)
			d, err := doc.Build("d.md", r)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			ix, err := render.BuildIndex(d, r.Lines(), nil)
			if err != nil {
				t.Fatalf("BuildIndex: %v", err)
			}
			table := render.Table(ix, style.Plain())
			if table == "" {
				t.Fatal("no table drawn")
			}
			var width int
			for i, line := range strings.Split(strings.TrimRight(table, "\n"), "\n") {
				w := style.Width(line)
				if i == 0 {
					width = w
					continue
				}
				if w != width {
					t.Errorf("row %d is %d columns, want %d:\n%s", i, w, width, table)
				}
			}
		})
	}
}

// TestUnknownInboundIsNotZero. A count nobody measured must not read as a count
// of none, or an agent will conclude a section is unreferenced when nothing
// looked.
func TestUnknownInboundIsNotZero(t *testing.T) {
	r := scan.Scan("# §1 A\n\ntext\n")
	d, _ := doc.Build("d.md", r)
	ix, err := render.BuildIndex(d, r.Lines(), nil)
	if err != nil {
		t.Fatal(err)
	}
	table := render.Table(ix, style.Plain())
	if !strings.Contains(table, "←?") {
		t.Errorf("an unmeasured inbound count did not render as ?:\n%s", table)
	}
	if strings.Contains(table, "←0") {
		t.Errorf("an unmeasured inbound count rendered as zero:\n%s", table)
	}
}

// TestTheFileRowSpansBothColumns: a long filename must not push every section
// number across the table.
func TestTheFileRowSpansBothColumns(t *testing.T) {
	r := scan.Scan("# §1 A\n")
	d, _ := doc.Build(strings.Repeat("deep/", 12)+"document.md", r)
	ix, _ := render.BuildIndex(d, r.Lines(), nil)
	table := render.Table(ix, style.Plain())
	var row string
	for _, line := range strings.Split(table, "\n") {
		if strings.HasPrefix(line, "| §1") {
			row = line
		}
	}
	if row == "" {
		t.Fatalf("no section row:\n%s", table)
	}
	// The name follows the number at the width of the widest number, not at the
	// width of the path. The slack the path needs is taken by the name column,
	// which is where a long value belongs.
	if got := strings.Index(row, "A"); got > 10 {
		t.Errorf("the name starts at column %d, so the filename stretched the number column:\n%s", got, table)
	}
}

func TestBuildIndexRefusesMismatchedLines(t *testing.T) {
	r := scan.Scan("# §1 A\n\ntext\n")
	d, _ := doc.Build("d.md", r)
	_, err := render.BuildIndex(d, r.Lines()[:1], nil)
	if err == nil {
		t.Fatal("expected a fault")
	}
	if got := fault.Code(err); got != fault.CodeInternal {
		t.Errorf("code = %d, want internal — a caller mismatch is a bug, not bad input", got)
	}
}

func TestRowsAreCopies(t *testing.T) {
	ix, _ := guideIndex(t)
	rows := ix.Rows()
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	rows[0] = render.Row{}
	if ix.Rows()[0].Name == "" {
		t.Error("Rows returned the live slice")
	}
}

// TestTheIndexNeverPrintsContent is frugality as a test: the index answers what
// is in a document, never what it says.
func TestTheIndexNeverPrintsContent(t *testing.T) {
	ix, _ := guideIndex(t)
	table := render.Table(ix, style.Plain())
	for _, phrase := range []string{
		"Dock reads documentation", "go install", "the grammar",
		"https://example.com", "Anno does the same job",
	} {
		if strings.Contains(table, phrase) {
			t.Errorf("the index leaked content: %q", phrase)
		}
	}
}
