package style_test

import (
	"strings"
	"testing"

	"orc/cq/internal/style"
	"orc/theme"
)

func TestZeroPaletteIsPlain(t *testing.T) {
	var p style.Palette
	if p.Enabled() {
		t.Errorf("the zero palette should be plain")
	}
	for ink := style.None; ink.Valid(); ink++ {
		if got := p.Paint("text", ink); got != "text" {
			t.Errorf("Paint(%d) = %q, want the text untouched", ink, got)
		}
	}
}

func TestPaintWrapsOnlyWhenItHelps(t *testing.T) {
	on := style.Coloured()

	if got := on.Paint("x", style.Command); !strings.HasPrefix(got, "\x1b[") {
		t.Errorf("Paint = %q, want a wrapped x", got)
	}
	if got := on.Paint("x", style.None); got != "x" {
		t.Errorf("the None ink should not wrap: %q", got)
	}
	if got := on.Paint("", style.Command); got != "" {
		t.Errorf("empty text should not wrap: %q", got)
	}
	// An ink outside the table is left alone rather than indexing past it.
	if got := on.Paint("x", style.Ink(99)); got != "x" {
		t.Errorf("an undefined ink should not wrap: %q", got)
	}
	if !on.Enabled() || style.Plain().Enabled() {
		t.Errorf("Coloured() should be enabled and Plain() should not")
	}
}

// TestEveryInkIsDefined guards the table: an ink added without a role would
// paint nothing, which looks like a palette that is simply off.
func TestEveryInkIsDefined(t *testing.T) {
	on := style.Coloured()
	for ink := style.Tool; ink.Valid(); ink++ {
		if got := on.Paint("x", ink); got == "x" {
			t.Errorf("ink %d paints nothing; the table has a hole", ink)
		}
	}
}

// TestPaintedTextKeepsItsPrintedWidth is what lets help align its columns from
// the plain text while painting each cell.
func TestPaintedTextKeepsItsPrintedWidth(t *testing.T) {
	on := style.Coloured()
	for ink := style.None; ink.Valid(); ink++ {
		if got := len(strip(on.Paint("abc", ink))); got != 3 {
			t.Errorf("ink %d strips to %d columns, want 3", ink, got)
		}
	}
}

// TestRolesAreDistinguishable: help colours commands, flags and values
// differently so a line can be read at a glance, which fails if two share one.
func TestRolesAreDistinguishable(t *testing.T) {
	on := style.Coloured()
	seen := map[string]style.Ink{}
	for _, ink := range []style.Ink{
		style.Command, style.Flag, style.Value, style.Setting,
		style.Good, style.Warn, style.Alarm,
	} {
		got := on.Paint("x", ink)
		if other, dup := seen[got]; dup {
			t.Errorf("inks %d and %d paint identically", other, ink)
		}
		seen[got] = ink
	}
}

// TestPaletteFollowsTheSharedScheme: the flavour is not cq's to choose.
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

func strip(s string) string {
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
