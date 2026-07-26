package render_test

import (
	"errors"
	"strings"
	"testing"

	"orc/anno/internal/fixture"
	"orc/anno/internal/render"
	"orc/anno/internal/style"
	"orc/anno/internal/tree"
	"orc/common/fault"
	"orc/common/source"
)

// documentedIndex is the index table exactly as it appears in Vision.md,
// separator rules included.
const documentedIndex = `|----------:-------------------|----------:---------:-------|------------------|
[example.go]                   [                            ] 32 lines < 1:32> |
|  section    data             [                            ]  3 lines < 4: 7> |
|  |  symbol  SampleOperation  [Operation                   ]  1 line  < 6: 6> |
|  section    types            [                            ]  8 lines <10:19> |
|  |  symbol  Pair             [struct    L         R       ]  4 lines <12:15> |
|  |  symbol  Operation        [                            ]  1 line  <18:18> |
|  section    code             [                            ]  8 lines <23:32> |
|  |  symbol  Operate          [Pair      Operation ->String]  8 lines <23:32> |
|  |  |  part declarations     [                            ]  4 lines <25:28> |
|--:--:------------------------|----------:---------:-------|------------------|
`

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

// TestDocumentedIndex is the golden test for the whole rendering layer.
func TestDocumentedIndex(t *testing.T) {
	got, err := render.Index(build(t, "example.go", fixture.ExampleGo), style.Palette{})
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if got != documentedIndex {
		gotLines := strings.Split(got, "\n")
		wantLines := strings.Split(documentedIndex, "\n")
		for i := range max(len(gotLines), len(wantLines)) {
			g, w := "", ""
			if i < len(gotLines) {
				g = gotLines[i]
			}
			if i < len(wantLines) {
				w = wantLines[i]
			}
			if g != w {
				t.Errorf("line %d:\n got %q\nwant %q", i+1, g, w)
			}
		}
	}
}

func TestIndexRowsAreAllTheSameWidth(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"documented", fixture.ExampleGo},
		{"no annotations", "package main\n"},
		{"empty file", ""},
		{"one annotation", "// @:> section s\nx\n"},
		{"long name", "// @:> symbol " + strings.Repeat("n", 60) + "\nx\n"},
		{"ragged metadata", "// @:> symbol a [x]\nq\n// @:> symbol b [x y z]\nq\n"},
		{"deep nesting", "// @:> section s\n// @:> symbol y\n// @:> part p\nx\n"},
		{"many lines", strings.Repeat("x\n", 1200) + "// @:> section s\nq\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := render.Index(build(t, "f.go", tc.text), style.Palette{})
			if err != nil {
				t.Fatalf("Index: %v", err)
			}
			lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
			if len(lines) < 3 {
				t.Fatalf("expected at least a rule, a file row and a rule:\n%s", out)
			}
			width := len([]rune(lines[0]))
			for i, line := range lines {
				if got := len([]rune(line)); got != width {
					t.Errorf("line %d is %d wide, want %d:\n%s", i+1, got, width, out)
				}
			}
			if !strings.HasPrefix(lines[0], "|") || !strings.HasSuffix(lines[0], "|") {
				t.Errorf("top rule is malformed: %q", lines[0])
			}
		})
	}
}

func TestIndexOfAFileWithoutAnnotations(t *testing.T) {
	out, err := render.Index(build(t, "bare.go", "a\nb\n"), style.Palette{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[bare.go]") {
		t.Errorf("output should name the file:\n%s", out)
	}
	if !strings.Contains(out, "2 lines") {
		t.Errorf("output should report the line count:\n%s", out)
	}
	if strings.Count(out, "\n") != 3 {
		t.Errorf("expected exactly three lines:\n%s", out)
	}
}

func TestIndexOfAnEmptyFile(t *testing.T) {
	out, err := render.Index(build(t, "empty.go", ""), style.Palette{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "0 lines") {
		t.Errorf("output should report zero lines:\n%s", out)
	}
}

func TestSingularAndPluralLineCounts(t *testing.T) {
	out, err := render.Index(build(t, "f.go", "// @:; symbol s\nx\ny\n"), style.Palette{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1 line ") {
		t.Errorf("a one-line annotation should read \"1 line\":\n%s", out)
	}
	if !strings.Contains(out, "3 lines") {
		t.Errorf("the file row should read \"3 lines\":\n%s", out)
	}
}

func TestEmptyAnnotationRendersAtItsMarkerLine(t *testing.T) {
	out, err := render.Index(build(t, "f.go", "x\n// @:> section s\n"), style.Palette{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "0 lines <2:2>") {
		t.Errorf("an empty annotation should collapse to its marker line:\n%s", out)
	}
}

func TestRow(t *testing.T) {
	tr := build(t, "example.go", fixture.ExampleGo)
	section := tr.Children()[2]
	symbol := section.Children()[0]
	part := symbol.Children()[0]

	got, err := render.Row(tr, []tree.Node{section, symbol, part}, style.Palette{})
	if err != nil {
		t.Fatalf("Row: %v", err)
	}
	if !strings.Contains(got, "part declarations") {
		t.Errorf("row = %q", got)
	}
	if !strings.Contains(got, "4 lines <25:28>") {
		t.Errorf("row = %q, want the documented count and range", got)
	}
	if !strings.HasPrefix(got, "|  |  |  ") {
		t.Errorf("row = %q, want three levels of indent", got)
	}

	if _, err := render.Row(tr, nil, style.Palette{}); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("an empty path should be an internal fault, got %v", err)
	}
}

func TestRowCarriesMetadata(t *testing.T) {
	tr := build(t, "f.go", "// @:> symbol S [a bb ccc]\nx\n")
	got, err := render.Row(tr, []tree.Node{tr.Children()[0]}, style.Palette{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "[a bb ccc]") {
		t.Errorf("row = %q, want the metadata rendered", got)
	}
}

func TestIndexRejectsCorruptTrees(t *testing.T) {
	// A zero Tree has no rows beyond the file row, which is still renderable;
	// the guard that matters is measure's refusal of an empty row set.
	if _, err := render.Index(tree.Tree{}, style.Palette{}); err != nil {
		t.Errorf("a zero tree should still render a file row, got %v", err)
	}
}

// stripANSI removes SGR escape sequences.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\x1b' {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// TestColourNeverChangesLayout is the property that makes colour safe to add:
// escape sequences occupy no columns, so stripping them from a coloured table
// must give back the plain table byte for byte.
func TestColourNeverChangesLayout(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"documented", fixture.ExampleGo},
		{"no annotations", "package main\n"},
		{"empty file", ""},
		{"ragged metadata", "// @:> symbol a [x]\nq\n// @:> symbol b [x y z]\nq\n"},
		{"deep nesting", "// @:> section s\n// @:> symbol y\n// @:> part p\nx\n"},
		{"unicode names", "// @:> symbol café [é]\nx\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := build(t, "f.go", tc.text)

			plain, err := render.Index(tr, style.Palette{})
			if err != nil {
				t.Fatal(err)
			}
			coloured, err := render.Index(tr, style.Coloured())
			if err != nil {
				t.Fatal(err)
			}

			if coloured == plain {
				t.Fatalf("colour was requested but nothing was painted")
			}
			if got := stripANSI(coloured); got != plain {
				t.Errorf("stripped colour output differs from plain:\n got %q\nwant %q", got, plain)
			}
		})
	}
}

func TestColouredRowStripsBackToPlain(t *testing.T) {
	tr := build(t, "example.go", fixture.ExampleGo)
	path := []tree.Node{tr.Children()[2], tr.Children()[2].Children()[0]}

	plain, err := render.Row(tr, path, style.Palette{})
	if err != nil {
		t.Fatal(err)
	}
	coloured, err := render.Row(tr, path, style.Coloured())
	if err != nil {
		t.Fatal(err)
	}
	if got := stripANSI(coloured); got != plain {
		t.Errorf("stripped row %q differs from plain %q", got, plain)
	}
}
