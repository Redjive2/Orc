package render

import (
	"errors"
	"strings"
	"testing"

	"orc/anno/internal/style"
	"orc/common/fault"
)

// The layout guards exist so a miscomputed column can never produce a ragged
// table; they are reached by handing the drawing pass a layout that disagrees
// with its rows.

func TestMeasureRejectsNoRows(t *testing.T) {
	if _, err := measure(nil); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("error = %v, want an internal fault", err)
	}
}

func TestMeasureRejectsNegativeDepth(t *testing.T) {
	_, err := measure([]row{{depth: -1, name: "x"}})
	if !errors.Is(err, fault.ErrInternal) {
		t.Fatalf("error = %v, want an internal fault", err)
	}
	if !strings.Contains(err.Error(), "negative depth") {
		t.Errorf("message %q should name the defect", err)
	}
}

func TestDrawRejectsALayoutTooNarrowForItsRow(t *testing.T) {
	r := row{depth: 1, kind: "section", name: "wide-name", lines: 1, start: 1, end: 1}
	good, err := measure([]row{r})
	if err != nil {
		t.Fatal(err)
	}

	// A name column that stops short of the row's own name.
	narrow := good
	narrow.nameEnd = 1
	if _, err := draw(r, narrow, style.Palette{}); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("a too-narrow name column should be refused, got %v", err)
	}

	// A kind column that stops short of the row's own kind word.
	shallow := good
	shallow.nameCol = 0
	if _, err := draw(r, shallow, style.Palette{}); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("a too-narrow kind column should be refused, got %v", err)
	}
}

func TestDrawRejectsATooNarrowMetadataSlot(t *testing.T) {
	r := row{depth: 1, kind: "section", name: "n", meta: []string{"metadata"}, lines: 1, start: 1, end: 1}
	l, err := measure([]row{r})
	if err != nil {
		t.Fatal(err)
	}
	l.metaCols = []int{1}
	if _, err := draw(r, l, style.Palette{}); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("a too-narrow metadata slot should be refused, got %v", err)
	}
}

func TestDrawRejectsAMetadataWidthMismatch(t *testing.T) {
	r := row{depth: 1, kind: "section", name: "n", meta: []string{"a"}, lines: 1, start: 1, end: 1}
	l, err := measure([]row{r})
	if err != nil {
		t.Fatal(err)
	}
	l.metaSpan = 99 // the rows say otherwise
	_, err = draw(r, l, style.Palette{})
	if !errors.Is(err, fault.ErrInternal) {
		t.Fatalf("error = %v, want an internal fault", err)
	}
	if !strings.Contains(err.Error(), "metadata row") {
		t.Errorf("message %q should name the mismatch", err)
	}
}

func TestPadRefusesNegativeWidths(t *testing.T) {
	var b strings.Builder
	if err := pad(&b, -1); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("error = %v, want an internal fault", err)
	}
	if err := pad(&b, 3); err != nil || b.String() != "   " {
		t.Errorf("pad(3) wrote %q, %v", b.String(), err)
	}
}

func TestDigits(t *testing.T) {
	for _, tc := range []struct{ n, want int }{
		{0, 1}, {1, 1}, {9, 1}, {10, 2}, {999, 3}, {1000, 4}, {-1, 2}, {-42, 3},
	} {
		if got := digits(tc.n); got != tc.want {
			t.Errorf("digits(%d) = %d, want %d", tc.n, got, tc.want)
		}
	}
}

func TestIndexPropagatesLayoutFailures(t *testing.T) {
	// A negative depth cannot come out of flatten, so this drives Index's own
	// error path with a row set that measure will reject.
	if _, err := measure([]row{{depth: -2}}); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("error = %v, want an internal fault", err)
	}
}

func TestRuleWidthMatchesRowWidth(t *testing.T) {
	rows := []row{
		{depth: 0, name: "[f.go]", lines: 3, start: 1, end: 3},
		{depth: 1, kind: "section", name: "s", meta: []string{"a", "bb"}, lines: 2, start: 2, end: 3},
	}
	l, err := measure(rows)
	if err != nil {
		t.Fatal(err)
	}
	want := len([]rune(rule(l, true)))
	if got := len([]rune(rule(l, false))); got != want {
		t.Errorf("bottom rule is %d wide, top rule is %d", got, want)
	}
	for _, r := range rows {
		line, err := draw(r, l, style.Palette{})
		if err != nil {
			t.Fatal(err)
		}
		if got := len([]rune(line)); got != want {
			t.Errorf("row %q is %d wide, rules are %d", line, got, want)
		}
	}
}

func TestInkForKnowsEachKind(t *testing.T) {
	for _, tc := range []struct {
		kind string
		want style.Ink
	}{
		{"section", style.Section},
		{"symbol", style.Symbol},
		{"part", style.Part},
		{"", style.None},
		{"something else", style.None},
	} {
		if got := inkFor(tc.kind); got != tc.want {
			t.Errorf("inkFor(%q) = %v, want %v", tc.kind, got, tc.want)
		}
	}
}
