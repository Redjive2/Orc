package theme_test

import (
	"fmt"
	"strings"
	"testing"

	"orc/theme"
)

func TestParseFlavour(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want theme.Flavour
	}{
		{"macchiato", theme.Macchiato},
		{"MACCHIATO", theme.Macchiato},
		{"  Macchiato  ", theme.Macchiato},
		{"mocha", theme.Mocha},
		{"frappe", theme.Frappe},
		{"frappé", theme.Frappe},
		{"latte", theme.Latte},
		{"none", theme.Plain},
		{"off", theme.Plain},
		{"plain", theme.Plain},
	} {
		got, err := theme.ParseFlavour(tc.raw)
		if err != nil {
			t.Errorf("ParseFlavour(%q): %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseFlavour(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}

	for _, raw := range []string{"", "dracula", "macchiatto", "solarized", "1"} {
		if _, err := theme.ParseFlavour(raw); err == nil {
			t.Errorf("ParseFlavour(%q) should have failed", raw)
		} else if !strings.Contains(err.Error(), "macchiato") {
			t.Errorf("the error for %q should list the options: %v", raw, err)
		}
	}
}

// TestFlavourNamesRoundTrip: the name a flavour prints must be one that parses
// back, or a tool could report a setting nobody can reproduce.
func TestFlavourNamesRoundTrip(t *testing.T) {
	for _, f := range []theme.Flavour{theme.Plain, theme.Latte, theme.Frappe, theme.Macchiato, theme.Mocha} {
		back, err := theme.ParseFlavour(f.String())
		if err != nil {
			t.Errorf("%v prints as %q, which does not parse: %v", f, f, err)
			continue
		}
		if back != f {
			t.Errorf("%v prints as %q, which parses as %v", f, f, back)
		}
	}
	for _, name := range theme.Flavours() {
		if _, err := theme.ParseFlavour(name); err != nil {
			t.Errorf("the advertised name %q does not parse: %v", name, err)
		}
	}
}

// TestMacchiatoIsTheDefaultAndIsTheRealPalette pins the colours themselves.
// They are transcribed values, and a transcription error is exactly the sort of
// thing no other test would notice.
func TestMacchiatoIsTheRealPalette(t *testing.T) {
	if theme.Default != theme.Macchiato {
		t.Errorf("Default = %v, want macchiato", theme.Default)
	}

	p := theme.New(theme.Macchiato, theme.TrueColour)
	for _, tc := range []struct {
		role theme.Role
		hex  string
	}{
		{theme.Text, "#cad3f5"},
		{theme.Heading, "#cad3f5"},
		{theme.Title, "#c6a0f6"},   // mauve
		{theme.Muted, "#8087a2"},   // overlay1
		{theme.Subtle, "#6e738d"},  // overlay0
		{theme.Frame, "#5b6078"},   // surface2
		{theme.Primary, "#8aadf4"}, // blue
		{theme.Secondary, "#c6a0f6"},
		{theme.Tertiary, "#8bd5ca"}, // teal
		{theme.Accent, "#f5a97f"},   // peach
		{theme.Info, "#7dc4e4"},     // sapphire
		{theme.Success, "#a6da95"},  // green
		{theme.Warning, "#eed49f"},  // yellow
		{theme.Danger, "#ed8796"},   // red
	} {
		got, ok := p.Colour(tc.role)
		if !ok {
			t.Errorf("%v has no colour", tc.role)
			continue
		}
		if got.Hex() != tc.hex {
			t.Errorf("%v = %s, want %s", tc.role, got.Hex(), tc.hex)
		}
	}
}

// TestEveryFlavourDefinesEveryRole guards the table itself: a role added
// without a colour would render as black, which on a dark terminal is invisible
// rather than obviously wrong.
func TestEveryFlavourDefinesEveryRole(t *testing.T) {
	for _, f := range []theme.Flavour{theme.Latte, theme.Frappe, theme.Macchiato, theme.Mocha} {
		p := theme.New(f, theme.TrueColour)
		for r := theme.Text; r.Valid(); r++ {
			c, ok := p.Colour(r)
			if !ok {
				t.Errorf("%v/%v has no colour", f, r)
				continue
			}
			if c == (theme.Colour{}) {
				t.Errorf("%v/%v is pure black, which means the table has a hole", f, r)
			}
		}
	}
}

// TestRolesAreDistinguishable: two roles a reader has to tell apart must not
// resolve to the same colour, or the distinction is decorative.
func TestRolesAreDistinguishable(t *testing.T) {
	groups := [][]theme.Role{
		{theme.Primary, theme.Secondary, theme.Tertiary},
		{theme.Success, theme.Warning, theme.Danger},
		{theme.Muted, theme.Subtle, theme.Frame},
	}
	for _, f := range []theme.Flavour{theme.Latte, theme.Frappe, theme.Macchiato, theme.Mocha} {
		p := theme.New(f, theme.TrueColour)
		for _, group := range groups {
			seen := map[string]theme.Role{}
			for _, r := range group {
				c, _ := p.Colour(r)
				if other, dup := seen[c.Hex()]; dup {
					t.Errorf("%v: %v and %v are both %s", f, other, r, c.Hex())
				}
				seen[c.Hex()] = r
			}
		}
	}
}

func TestPaint(t *testing.T) {
	p := theme.New(theme.Macchiato, theme.TrueColour)

	got := p.Paint("hello", theme.Title)
	if !strings.Contains(got, "hello") {
		t.Fatalf("Paint lost the text: %q", got)
	}
	// Title is mauve and bold.
	if want := "\x1b[1;38;2;198;160;246mhello\x1b[0m"; got != want {
		t.Errorf("Paint = %q, want %q", got, want)
	}

	// Strong adds weight to a role that does not carry it.
	plainRole := p.Paint("x", theme.Success)
	strongRole := p.Strong("x", theme.Success)
	if strings.Contains(plainRole, "[1;") {
		t.Errorf("Success should not be bold by default: %q", plainRole)
	}
	if !strings.Contains(strongRole, "1;") {
		t.Errorf("Strong should add weight: %q", strongRole)
	}
}

// TestPaintIsSafeOnEveryInput: Paint is called from drawing code that cannot
// usefully handle an error, so it must never produce one.
func TestPaintIsSafeOnEveryInput(t *testing.T) {
	for _, p := range []theme.Palette{
		{},
		theme.New(theme.Macchiato, theme.TrueColour),
		theme.New(theme.Macchiato, theme.Ansi256),
		theme.New(theme.Plain, theme.TrueColour),
		theme.New(theme.Macchiato, theme.NoColour),
	} {
		for _, r := range []theme.Role{theme.Text, theme.Danger, theme.Role(-1), theme.Role(999)} {
			for _, s := range []string{"", "x", "日本語", "\x1b[31m"} {
				got := p.Paint(s, r)
				if !strings.Contains(got, s) {
					t.Errorf("Paint(%q, %v) = %q, lost the text", s, r, got)
				}
				// Empty text never gains a sequence: an empty cell that carried
				// escape codes would measure as wider than it draws.
				if s == "" && got != "" {
					t.Errorf("Paint(\"\", %v) = %q, want empty", r, got)
				}
			}
		}
	}
}

// TestDisabledPalettesAreByteIdentical is what lets every golden test in every
// tool run without opting out of anything.
func TestDisabledPalettesAreByteIdentical(t *testing.T) {
	for _, p := range []theme.Palette{
		{},
		theme.New(theme.Plain, theme.TrueColour),
		theme.New(theme.Macchiato, theme.NoColour),
		theme.New(theme.Flavour(99), theme.TrueColour),
	} {
		if p.Enabled() {
			t.Errorf("%v/%v should be disabled", p.Flavour(), p.Depth())
		}
		for r := theme.Text; r.Valid(); r++ {
			if got := p.Paint("sample", r); got != "sample" {
				t.Errorf("a disabled palette painted %v: %q", r, got)
			}
			if got := p.Strong("sample", r); got != "sample" {
				t.Errorf("a disabled palette emphasised %v: %q", r, got)
			}
		}
	}
}

// TestAnsi256FallbackStaysInRange: the approximation must always land on a real
// index, or the terminal prints the digits instead of colouring anything.
func TestAnsi256FallbackStaysInRange(t *testing.T) {
	p := theme.New(theme.Macchiato, theme.Ansi256)
	for _, f := range []theme.Flavour{theme.Latte, theme.Frappe, theme.Macchiato, theme.Mocha} {
		p = theme.New(f, theme.Ansi256)
		for r := theme.Text; r.Valid(); r++ {
			got := p.Paint("x", r)
			if !strings.Contains(got, "38;5;") {
				t.Errorf("%v/%v did not use a 256-colour sequence: %q", f, r, got)
			}
			var idx int
			if _, err := fmt.Sscanf(got[strings.Index(got, "38;5;"):], "38;5;%dm", &idx); err != nil {
				t.Errorf("%v/%v produced an unreadable sequence %q: %v", f, r, got, err)
				continue
			}
			if idx < 16 || idx > 255 {
				t.Errorf("%v/%v mapped to index %d, outside 16..255", f, r, idx)
			}
		}
	}
}

// TestApproximationIsClose keeps the 256-colour fallback honest: it should be
// recognisably the same colour, not merely a legal index.
func TestApproximationIsClose(t *testing.T) {
	exact := theme.New(theme.Macchiato, theme.TrueColour)
	for r := theme.Text; r.Valid(); r++ {
		want, _ := exact.Colour(r)
		// The cube's coarsest spacing is 40 per channel, so a good match is
		// within half of that in each direction.
		if got := nearestCubeDistance(want); got > 3*20*20 {
			t.Errorf("%v (%s) is %d from the nearest representable colour", r, want.Hex(), got)
		}
	}
}

// nearestCubeDistance measures how far a colour is from what a 256-colour
// terminal can actually show.
func nearestCubeDistance(c theme.Colour) int {
	levels := []uint8{0, 95, 135, 175, 215, 255}
	best := 1 << 30
	for _, r := range levels {
		for _, g := range levels {
			for _, b := range levels {
				if d := sq(c.R, r) + sq(c.G, g) + sq(c.B, b); d < best {
					best = d
				}
			}
		}
	}
	for step := range 24 {
		v := uint8(8 + 10*step)
		if d := sq(c.R, v) + sq(c.G, v) + sq(c.B, v); d < best {
			best = d
		}
	}
	return best
}

func sq(a, b uint8) int { d := int(a) - int(b); return d * d }

func TestDepthAndFlavourStrings(t *testing.T) {
	for _, tc := range []struct {
		d    theme.Depth
		want string
	}{
		{theme.NoColour, "none"},
		{theme.Ansi256, "256"},
		{theme.TrueColour, "truecolor"},
	} {
		if got := tc.d.String(); got != tc.want {
			t.Errorf("Depth(%d).String() = %q, want %q", tc.d, got, tc.want)
		}
	}
	if got := theme.Role(999).String(); !strings.Contains(got, "999") {
		t.Errorf("an undefined role should say so, got %q", got)
	}
	if theme.Role(999).Valid() {
		t.Error("Role(999) should not be valid")
	}
}

func TestColourHex(t *testing.T) {
	if got := (theme.Colour{R: 0x0a, G: 0xbc, B: 0xde}).Hex(); got != "#0abcde" {
		t.Errorf("Hex() = %q", got)
	}
}
