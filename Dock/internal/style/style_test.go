package style_test

import (
	"strings"
	"testing"

	"orc/dock/internal/style"
)

// TestWidthIsColumnsNotBytes. A § is three bytes and one column, and every
// table in Dock aligns on this: a wrong answer here shears a column.
func TestWidthIsColumnsNotBytes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"§", 1},
		{"§1.2.1", 6},
		{"→3 ←0", 5},
		{"日本語", 6},
		{"日本語のセクション", 18},
		{"é", 1},      // e + combining acute
		{"áb́c", 3},   // combining marks occupy no column
		{"\x00abc", 3}, // control characters are not drawn
		{"…", 1},
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := style.Width(tc.in); got != tc.want {
				t.Errorf("Width(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestPadAlwaysMeasuresItsWidth is the invariant the whole table rests on.
func TestPadAlwaysMeasuresItsWidth(t *testing.T) {
	for _, in := range []string{
		"", "a", "abc", "§1.2.1", "日本語", "日本語のセクション名です",
		strings.Repeat("x", 40), "éé", "→3 ←0",
	} {
		for _, w := range []int{0, 1, 2, 3, 6, 10, 20} {
			if got := style.Width(style.Pad(in, w)); got != w {
				t.Errorf("Width(Pad(%q, %d)) = %d, want %d", in, w, got, w)
			}
			if got := style.Width(style.PadLeft(in, w)); got != w {
				t.Errorf("Width(PadLeft(%q, %d)) = %d, want %d", in, w, got, w)
			}
		}
	}
}

func TestTruncate(t *testing.T) {
	for _, tc := range []struct {
		in    string
		width int
		want  string
	}{
		{"abcdef", 10, "abcdef"},
		{"abcdef", 6, "abcdef"},
		{"abcdef", 5, "abcd…"},
		{"abcdef", 1, "…"},
		{"abcdef", 0, ""},
		{"abcdef", -1, ""},
		{"日本語", 3, "日…"},
		{"§1.2.1", 4, "§1.…"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got := style.Truncate(tc.in, tc.width)
			if got != tc.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tc.in, tc.width, got, tc.want)
			}
			if w := style.Width(got); tc.width > 0 && w > tc.width {
				t.Errorf("Truncate(%q, %d) is %d columns wide", tc.in, tc.width, w)
			}
		})
	}
}

// TestAWideRuneIsNeverCutInHalf: truncating between the two columns of a wide
// rune would leave a cell that measures right and draws wrong.
func TestAWideRuneIsNeverCutInHalf(t *testing.T) {
	for w := 1; w <= 8; w++ {
		got := style.Truncate("日本語日", w)
		if style.Width(got) > w {
			t.Errorf("Truncate(width %d) = %q, %d columns", w, got, style.Width(got))
		}
	}
}

// TestThePlainPaletteNeverColours. The zero value is plain, so every code path
// that has not asked for colour gets none — including every test comparing
// exact bytes.
func TestThePlainPaletteNeverColours(t *testing.T) {
	var zero style.Palette
	for _, p := range []style.Palette{zero, style.Plain()} {
		if p.Enabled() {
			t.Error("a plain palette reports itself enabled")
		}
		for ink := style.Ink(0); ink.Valid(); ink++ {
			if got := p.Paint("text", ink); got != "text" {
				t.Errorf("plain palette painted ink %d: %q", ink, got)
			}
		}
	}
}

func TestPaintIsAlwaysSafe(t *testing.T) {
	p := style.Coloured()
	if !p.Enabled() {
		t.Fatal("the coloured palette is not enabled")
	}
	// Empty text, the None ink, and an undefined ink all pass through, so Paint
	// never lengthens output that would not benefit.
	if got := p.Paint("", style.Number); got != "" {
		t.Errorf("painted empty text: %q", got)
	}
	if got := p.Paint("x", style.None); got != "x" {
		t.Errorf("painted the None ink: %q", got)
	}
	if got := p.Paint("x", style.Ink(999)); got != "x" {
		t.Errorf("painted an undefined ink: %q", got)
	}
	if got := p.Paint("x", style.Number); got == "x" {
		t.Error("a defined ink was not painted")
	}
}

// TestColourNeverChangesWidth is why measuring and painting are separate: the
// layout pass must never see an escape sequence.
func TestColourNeverChangesWidth(t *testing.T) {
	p := style.Coloured()
	for _, in := range []string{"a", "§1.2", "日本語", "→3 ←0"} {
		painted := p.Paint(in, style.Number)
		if painted == in {
			t.Fatalf("%q was not painted", in)
		}
		if len(painted) <= len(in) {
			t.Errorf("painting %q did not add sequences", in)
		}
	}
}

func TestEveryInkIsMapped(t *testing.T) {
	p := style.Coloured()
	for ink := style.Ink(1); ink.Valid(); ink++ {
		if got := p.Paint("x", ink); got == "x" {
			t.Errorf("ink %d has no colour", ink)
		}
	}
}
