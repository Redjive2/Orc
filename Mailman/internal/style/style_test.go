package style_test

import (
	"errors"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/mailman/internal/style"
	"orc/theme"
)

// TestPaletteWrapsTheSharedScheme: Mailman decides what is a heading; the
// shared theme decides what a heading looks like. This checks the wiring, and
// leaves the colour policy to be tested where it lives, in orc/theme.
func TestPaletteWrapsTheSharedScheme(t *testing.T) {
	if style.Plain().Enabled() {
		t.Error("the plain palette should be disabled")
	}
	if !style.Coloured().Enabled() {
		t.Error("the coloured palette should be enabled")
	}
	if got := style.Coloured().Scheme().Flavour(); got != theme.Default {
		t.Errorf("Coloured() uses %v, want the default %v", got, theme.Default)
	}
	// A palette built from a disabled scheme is disabled, whichever way the
	// caller arrived at it.
	if style.New(theme.Palette{}).Enabled() {
		t.Error("wrapping a zero scheme should give a plain palette")
	}
	if style.New(theme.New(theme.Plain, theme.TrueColour)).Enabled() {
		t.Error("wrapping the plain flavour should give a plain palette")
	}
}

// TestRolesAreCatppuccin pins the actual colours one level up from the theme's
// own tests, so a mis-wired role — Subject pointing at Danger, say — is caught
// here rather than noticed on screen.
func TestRolesAreCatppuccin(t *testing.T) {
	p := style.Coloured()
	for _, tc := range []struct {
		name string
		role func(string) string
		hex  string
	}{
		{"Title", p.Title, "198;160;246"},   // mauve
		{"Frame", p.Frame, "91;96;120"},     // surface2
		{"Muted", p.Muted, "128;135;162"},   // overlay1
		{"Unread", p.Unread, "238;212;159"}, // yellow
		{"User", p.User, "166;218;149"},     // green
		{"Subject", p.Subject, "138;173;244"},
		{"Convo", p.Convo, "198;160;246"},
		{"ID", p.ID, "125;196;228"},
		{"Good", p.Good, "166;218;149"},
		{"Bad", p.Bad, "237;135;150"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.role("x"); !strings.Contains(got, tc.hex) {
				t.Errorf("%s = %q, want the colour %s", tc.name, got, tc.hex)
			}
		})
	}
}

// TestPlainAndColouredAreExhaustive checks every role in the palette, in both
// modes, so a newly added role cannot quietly do nothing.
func TestPlainAndColouredAreExhaustive(t *testing.T) {
	roles := map[string]func(style.Palette, string) string{
		"Title":   style.Palette.Title,
		"Header":  style.Palette.Header,
		"Frame":   style.Palette.Frame,
		"Muted":   style.Palette.Muted,
		"Unread":  style.Palette.Unread,
		"User":    style.Palette.User,
		"Subject": style.Palette.Subject,
		"Convo":   style.Palette.Convo,
		"ID":      style.Palette.ID,
		"Good":    style.Palette.Good,
		"Bad":     style.Palette.Bad,
		"Note":    style.Palette.Note,
	}

	plain, coloured := style.Plain(), style.Coloured()
	for name, role := range roles {
		t.Run(name, func(t *testing.T) {
			if got := role(plain, "text"); got != "text" {
				t.Errorf("plain %s = %q, want the text unchanged", name, got)
			}
			got := role(coloured, "text")
			if !strings.Contains(got, "text") {
				t.Errorf("coloured %s = %q, lost the text", name, got)
			}
			if !strings.HasPrefix(got, "\x1b[") {
				t.Errorf("coloured %s = %q, should begin with an escape", name, got)
			}
			if !strings.HasSuffix(got, "\x1b[0m") {
				t.Errorf("coloured %s = %q, should reset at the end", name, got)
			}
			// An empty string must stay empty: a colour wrapper around nothing
			// would still occupy a cell in a table that measured it as zero.
			if got := role(coloured, ""); got != "" {
				t.Errorf("coloured %s(\"\") = %q, want empty", name, got)
			}
		})
	}
}

func TestWidth(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want int
	}{
		{"empty", "", 0},
		{"ascii", "hello", 5},
		{"accented", "café", 4},
		{"combining mark", "é", 1},
		{"cjk", "日本語", 6},
		{"hangul", "한국", 4},
		{"fullwidth", "ＡＢ", 4},
		{"mixed", "re: 日本", 8},
		{"emoji", "🚀", 2},
		{"zero width joiner", "a‍b", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := style.Width(tc.text); got != tc.want {
				t.Errorf("Width(%q) = %d, want %d", tc.text, got, tc.want)
			}
		})
	}
}

func TestSanitiseRemovesControlCharacters(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{"clean", "hello", "hello"},
		{"tab", "a\tb", "a    b"},
		{"newline", "a\nb", "a�b"},
		{"carriage return", "a\rb", "a�b"},
		{"escape", "a\x1b[31mb", "a�[31mb"},
		{"del", "a\x7fb", "a�b"},
		{"c1 control", "ab", "a�b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := style.Sanitise(tc.text); got != tc.want {
				t.Errorf("Sanitise(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

// TestSanitiseDefeatsAnEscapeInjection is the security-shaped case: a subject
// line is attacker-controlled text that lands in a drawn table.
func TestSanitiseDefeatsAnEscapeInjection(t *testing.T) {
	hostile := "innocent\x1b[2J\x1b[H TOTALLY FINE"
	got := style.Sanitise(hostile)
	if strings.Contains(got, "\x1b") {
		t.Errorf("Sanitise left an escape in %q", got)
	}
	if style.Width(got) != len([]rune(got)) {
		t.Error("sanitised text should be all single-width")
	}
}

func TestTruncate(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		max  int
		want string
	}{
		{"fits", "hello", 10, "hello"},
		{"exact", "hello", 5, "hello"},
		{"cut", "hello", 4, "hel…"},
		{"one cell", "hello", 1, "…"},
		{"zero cells", "hello", 0, ""},
		{"empty", "", 3, ""},
		{"wide runes cut cleanly", "日本語", 4, "日…"},
		{"wide rune would straddle", "日本語", 3, "日…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := style.Truncate(tc.text, tc.max)
			if err != nil {
				t.Fatalf("Truncate: %v", err)
			}
			if got != tc.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tc.text, tc.max, got, tc.want)
			}
		})
	}
}

// TestTruncateNeverExceedsItsLimit is the property the table layout relies on.
// A single overrun shears every row below it.
func TestTruncateNeverExceedsItsLimit(t *testing.T) {
	texts := []string{
		"", "a", "hello world", "日本語のテキスト", "🚀🚀🚀", "café", "áb́",
		"ＡＢＣＤＥ", "mixed 日本 text 🚀 here",
	}
	for _, text := range texts {
		for max := range 20 {
			got, err := style.Truncate(text, max)
			if err != nil {
				t.Fatalf("Truncate(%q, %d): %v", text, max, err)
			}
			if w := style.Width(got); w > max {
				t.Errorf("Truncate(%q, %d) = %q, which is %d cells", text, max, got, w)
			}
		}
	}
}

func TestTruncateRejectsANegativeWidth(t *testing.T) {
	if _, err := style.Truncate("x", -1); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Truncate(_, -1) = %v, want an internal fault", err)
	}
}

func TestPad(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		width int
		align byte
		want  string
	}{
		{"left", "ab", 5, 'l', "ab   "},
		{"right", "ab", 5, 'r', "   ab"},
		{"centre", "ab", 6, 'c', "  ab  "},
		{"centre odd gap", "ab", 5, 'c', " ab  "},
		{"exact", "abc", 3, 'l', "abc"},
		{"empty", "", 3, 'l', "   "},
		{"zero width", "abc", 0, 'l', ""},
		{"wide text", "日本", 6, 'l', "日本  "},
		{"overlong is truncated", "abcdef", 4, 'l', "abc…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := style.Pad(tc.text, tc.width, tc.align)
			if err != nil {
				t.Fatalf("Pad: %v", err)
			}
			if got != tc.want {
				t.Errorf("Pad(%q, %d, %q) = %q, want %q", tc.text, tc.width, tc.align, got, tc.want)
			}
			if w := style.Width(got); w != tc.width {
				t.Errorf("Pad(%q, %d, %q) is %d cells, want exactly %d", tc.text, tc.width, tc.align, w, tc.width)
			}
		})
	}
}

// TestPadRejectsAnUnknownAlignment: a mis-typed alignment must be a reported
// bug, not a silent default that produces an almost-right table.
func TestPadRejectsAnUnknownAlignment(t *testing.T) {
	if _, err := style.Pad("x", 3, 'q'); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Pad with alignment 'q' = %v, want an internal fault", err)
	}
	if _, err := style.Pad("x", -1, 'l'); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Pad with a negative width = %v, want an internal fault", err)
	}
}

// TestPadIsExactForEveryWidth is the invariant that makes columns line up: the
// result is always precisely the requested number of cells, for any input.
func TestPadIsExactForEveryWidth(t *testing.T) {
	texts := []string{"", "a", "hello", "日本語", "🚀 go", "café"}
	for _, text := range texts {
		for width := range 12 {
			for _, align := range []byte{'l', 'r', 'c'} {
				got, err := style.Pad(text, width, align)
				if err != nil {
					t.Fatalf("Pad(%q, %d, %q): %v", text, width, align, err)
				}
				if w := style.Width(got); w != width {
					t.Errorf("Pad(%q, %d, %q) = %q, which is %d cells", text, width, align, got, w)
				}
			}
		}
	}
}

func FuzzWidthAndTruncate(f *testing.F) {
	for _, seed := range []string{"", "a", "日本語", "🚀", "é", "\x1b[31m", "ＡＢ"} {
		f.Add(seed, 5)
	}
	f.Fuzz(func(t *testing.T, text string, max int) {
		// Bound the width so the fuzzer spends its time on the text rather than
		// on allocating enormous padding.
		if max < 0 || max > 64 {
			max = max & 63
		}

		clean := style.Sanitise(text)
		if strings.ContainsAny(clean, "\x1b\n\r\t\x00") {
			t.Fatalf("Sanitise left a control character in %q", clean)
		}

		got, err := style.Truncate(clean, max)
		if err != nil {
			t.Fatalf("Truncate(%q, %d): %v", clean, max, err)
		}
		if w := style.Width(got); w > max {
			t.Fatalf("Truncate(%q, %d) = %q, which is %d cells", clean, max, got, w)
		}

		padded, err := style.Pad(clean, max, 'l')
		if err != nil {
			t.Fatalf("Pad(%q, %d): %v", clean, max, err)
		}
		if w := style.Width(padded); w != max {
			t.Fatalf("Pad(%q, %d) = %q, which is %d cells", clean, max, padded, w)
		}
	})
}
