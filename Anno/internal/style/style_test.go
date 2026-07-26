package style_test

import (
	"strings"
	"testing"

	"orc/anno/internal/style"
	"orc/theme"
)

func TestZeroPaletteIsPlain(t *testing.T) {
	var p style.Palette
	if p.Enabled() {
		t.Errorf("the zero palette should be plain")
	}
	for ink := style.None; ink.Valid(); ink++ {
		if got := p.Paint("text", ink); got != "text" {
			t.Errorf("Paint(%v) = %q, want the text untouched", ink, got)
		}
	}
}

func TestPaintWrapsOnlyWhenItHelps(t *testing.T) {
	on := style.Coloured()

	if got := on.Paint("x", style.Name); !strings.Contains(got, "x") || !strings.HasPrefix(got, "\x1b[") {
		t.Errorf("Paint = %q, want a wrapped x", got)
	}
	if got := on.Paint("x", style.None); got != "x" {
		t.Errorf("the None ink should not wrap: %q", got)
	}
	if got := on.Paint("", style.Name); got != "" {
		t.Errorf("empty text should not wrap: %q", got)
	}
	// An ink outside the table is left alone rather than indexing past it.
	if got := on.Paint("x", style.Ink(99)); got != "x" {
		t.Errorf("an undefined ink should not wrap: %q", got)
	}
	if !on.Enabled() {
		t.Errorf("Coloured() should be enabled")
	}
	if style.Plain().Enabled() {
		t.Errorf("Plain() should not be enabled")
	}
}

func TestPaintedTextKeepsItsPrintedWidth(t *testing.T) {
	on := style.Coloured()
	for ink := style.None; ink.Valid(); ink++ {
		painted := on.Paint("abc", ink)
		if got := len(strip(painted)); got != 3 {
			t.Errorf("%v painted %q strips to %d columns, want 3", ink, painted, got)
		}
	}
}

// TestEveryInkIsDefined guards the table: an ink added without a role would
// paint nothing, which looks like a palette that is simply off.
func TestEveryInkIsDefined(t *testing.T) {
	on := style.Coloured()
	for ink := style.Name; ink.Valid(); ink++ {
		if got := on.Paint("x", ink); got == "x" {
			t.Errorf("%v paints nothing; the ink table has a hole", ink)
		}
	}
}

// TestInksAreCatppuccin pins the colours the scheme resolves to, so a mis-wired
// ink is caught here rather than noticed on screen.
func TestInksAreCatppuccin(t *testing.T) {
	on := style.Coloured()
	for _, tc := range []struct {
		ink style.Ink
		rgb string
	}{
		{style.Name, "202;211;245"},    // text
		{style.Quiet, "128;135;162"},   // overlay1
		{style.Meta, "245;169;127"},    // peach
		{style.Span, "125;196;228"},    // sapphire
		{style.Section, "198;160;246"}, // mauve
		{style.Symbol, "139;213;202"},  // teal
		{style.Part, "166;218;149"},    // green
		{style.Good, "166;218;149"},    // green
		{style.Alarm, "237;135;150"},   // red
	} {
		if got := on.Paint("x", tc.ink); !strings.Contains(got, tc.rgb) {
			t.Errorf("%v = %q, want the colour %s", tc.ink, got, tc.rgb)
		}
	}
}

// TestKindsAreDistinguishable: an index colours the three annotation kinds so
// they can be told apart at a glance, which fails if two share a colour.
func TestKindsAreDistinguishable(t *testing.T) {
	on := style.Coloured()
	seen := map[string]style.Ink{}
	for _, ink := range []style.Ink{style.Section, style.Symbol, style.Part} {
		got := on.Paint("x", ink)
		if other, dup := seen[got]; dup {
			t.Errorf("%v and %v paint identically", other, ink)
		}
		seen[got] = ink
	}
}

// TestPaletteFollowsTheSharedScheme: the flavour is not Anno's to choose.
func TestPaletteFollowsTheSharedScheme(t *testing.T) {
	for _, f := range []theme.Flavour{theme.Latte, theme.Frappe, theme.Macchiato, theme.Mocha} {
		p := style.New(theme.New(f, theme.TrueColour))
		if !p.Enabled() {
			t.Errorf("%v should give an enabled palette", f)
		}
		if got := p.Scheme().Flavour(); got != f {
			t.Errorf("Scheme() = %v, want %v", got, f)
		}
	}
	if style.New(theme.Palette{}).Enabled() {
		t.Error("a zero scheme should give a plain palette")
	}
}

// strip removes SGR escape sequences, so a test can compare printed columns.
func strip(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\x1b' {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++ // the 'm' itself
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
