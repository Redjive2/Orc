// Package style is the only place in Orcprobe that knows an escape sequence
// exists.
//
// Two jobs, as in Mailman: it decides what is a heading, and it measures text
// the way a terminal will lay it out. The colours themselves come from
// orc/theme, which every Orc tool shares, so a probe's tables look like the
// tools they are showing.
//
// The house rule for colour: it is a layer, never information. Every colour is
// redundant with a glyph or a word, so piping through grep or setting NO_COLOR
// loses nothing but the pleasure. One rule is Orcprobe's own: a guard that is
// not in force is drawn in the danger role *and* says "absent" in words, since
// that is the one line in this tool nobody may misread.
package style

import (
	"strings"
	"unicode"

	"orc/orcprobe/internal/fault"
	"orc/theme"
)

// Palette renders text in a role. The zero Palette is plain, which makes
// "colour off" the trivially safe path and lets golden tests compare bytes.
type Palette struct {
	inner theme.Palette
}

// New wraps a resolved scheme.
func New(p theme.Palette) Palette { return Palette{inner: p} }

// Plain returns a palette that never colours anything.
func Plain() Palette { return Palette{} }

// Coloured returns a palette in the default flavour, so a test can assert the
// escape sequences appear without owning a terminal.
func Coloured() Palette {
	return Palette{inner: theme.New(theme.Default, theme.TrueColour)}
}

// Enabled reports whether this palette emits escape sequences.
func (p Palette) Enabled() bool { return p.inner.Enabled() }

// Scheme returns the underlying palette, for a caller that needs to name the
// flavour in force.
func (p Palette) Scheme() theme.Palette { return p.inner }

// The roles Orcprobe draws with. Each maps to a role in the shared scheme
// rather than to a colour, so what "a problem" looks like is decided once for
// every tool rather than here.

// Title styles a table's heading.
func (p Palette) Title(s string) string { return p.inner.Paint(s, theme.Title) }

// Header styles a column label.
func (p Palette) Header(s string) string { return p.inner.Paint(s, theme.Heading) }

// Frame styles the box-drawing characters, which should recede.
func (p Palette) Frame(s string) string { return p.inner.Paint(s, theme.Frame) }

// Muted styles secondary text: sizes, counts, absent values.
func (p Palette) Muted(s string) string { return p.inner.Paint(s, theme.Muted) }

// Probe styles a probe name.
func (p Palette) Probe(s string) string { return p.inner.Paint(s, theme.Primary) }

// User styles an identity.
func (p Palette) User(s string) string { return p.inner.Paint(s, theme.Success) }

// ID styles an identifier.
func (p Palette) ID(s string) string { return p.inner.Paint(s, theme.Info) }

// Subject styles a mail subject, matching what Mailman paints one.
func (p Palette) Subject(s string) string { return p.inner.Paint(s, theme.Primary) }

// Path styles a filesystem path.
func (p Palette) Path(s string) string { return p.inner.Paint(s, theme.Secondary) }

// Good styles a guard in force, or a step that succeeded.
func (p Palette) Good(s string) string { return p.inner.Paint(s, theme.Success) }

// Warn styles something the operator should notice but that is not a failure —
// a guard that is deferred, a probe taken from a world that has since moved.
func (p Palette) Warn(s string) string { return p.inner.Strong(s, theme.Warning) }

// Bad styles a guard that is absent, or a refusal.
func (p Palette) Bad(s string) string { return p.inner.Strong(s, theme.Danger) }

// Note styles an aside.
func (p Palette) Note(s string) string { return p.inner.Paint(s, theme.Subtle) }

// The help screen's roles. They map to the same theme roles Macmuffin's do, so
// the two tools' help pages look like pages of one manual rather than two.

// Tool styles the tool's own name.
func (p Palette) Tool(s string) string { return p.inner.Strong(s, theme.Title) }

// Command styles a command word.
func (p Palette) Command(s string) string { return p.inner.Strong(s, theme.Primary) }

// Flag styles an option.
func (p Palette) Flag(s string) string { return p.inner.Paint(s, theme.Tertiary) }

// Value styles a placeholder — <name>, <label>, a query.
func (p Palette) Value(s string) string { return p.inner.Paint(s, theme.Secondary) }

// Setting styles an environment variable.
func (p Palette) Setting(s string) string { return p.inner.Paint(s, theme.Info) }

// Width returns how many terminal cells s occupies.
//
// Three classes of rune are not one cell wide: combining marks and other
// zero-width formatting take none, East Asian wide and fullwidth forms take
// two, and control characters are replaced before they get here.
func Width(s string) int {
	n := 0
	for _, r := range s {
		n += runeWidth(r)
	}
	return n
}

func runeWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case unicode.Is(unicode.Mn, r), unicode.Is(unicode.Me, r), unicode.Is(unicode.Cf, r):
		return 0
	case isWide(r):
		return 2
	default:
		return 1
	}
}

// wideRanges are the East Asian Wide and Fullwidth blocks, plus the emoji
// ranges terminals render double-width. Coarse on purpose: a wrong guess in an
// exotic block costs a misaligned column, not a failure.
var wideRanges = [...][2]rune{
	{0x1100, 0x115F},   // Hangul Jamo
	{0x2E80, 0x303E},   // CJK radicals, Kangxi, CJK symbols
	{0x3041, 0x33FF},   // Hiragana through CJK compatibility
	{0x3400, 0x4DBF},   // CJK extension A
	{0x4E00, 0x9FFF},   // CJK unified ideographs
	{0xA000, 0xA4CF},   // Yi
	{0xAC00, 0xD7A3},   // Hangul syllables
	{0xF900, 0xFAFF},   // CJK compatibility ideographs
	{0xFE10, 0xFE19},   // vertical forms
	{0xFE30, 0xFE6F},   // CJK compatibility forms
	{0xFF00, 0xFF60},   // fullwidth forms
	{0xFFE0, 0xFFE6},   // fullwidth signs
	{0x1F300, 0x1F64F}, // emoji: symbols and pictographs, emoticons
	{0x1F680, 0x1F6FF}, // emoji: transport and map
	{0x1F7E0, 0x1F7EB}, // emoji: geometric shapes extended
	{0x1F900, 0x1F9FF}, // emoji: supplemental
	{0x1FA70, 0x1FAFF}, // emoji: symbols and pictographs extended-A
	{0x20000, 0x3FFFD}, // CJK extensions B onward
}

func isWide(r rune) bool {
	for _, span := range wideRanges {
		if r >= span[0] && r <= span[1] {
			return true
		}
	}
	return false
}

// Sanitise makes text safe to place in a table cell.
//
// Orcprobe draws strings that came out of other tools' stores — task names,
// mailbox names, paths from a manifest — and a probe is exactly where hostile
// content would be sitting. An escape sequence smuggled through one of those
// could repaint the whole table, so each control character becomes a visible
// substitute rather than being dropped.
func Sanitise(s string) string {
	if !strings.ContainsFunc(s, isControl) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteString("    ")
		case isControl(r):
			b.WriteRune('�')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isControl(r rune) bool {
	return r < 0x20 || r == 0x7F || (r >= 0x80 && r <= 0x9F)
}

// Ellipsis marks a truncated cell.
const Ellipsis = "…"

// Truncate shortens s to at most max cells, marking the cut with an ellipsis.
// Cells are never wrapped: a row that becomes two rows destroys the alignment
// that makes a table scannable.
func Truncate(s string, max int) (string, error) {
	if err := fault.Check(max >= 0, "style.Truncate", "negative width %d", max); err != nil {
		return "", err
	}
	if max == 0 {
		return "", nil
	}
	if Width(s) <= max {
		return s, nil
	}
	if max == 1 {
		return Ellipsis, nil
	}

	var b strings.Builder
	used := 0
	for _, r := range s {
		w := runeWidth(r)
		if used+w > max-1 {
			break
		}
		b.WriteRune(r)
		used += w
	}
	out := b.String() + Ellipsis
	if err := fault.Check(Width(out) <= max, "style.Truncate",
		"truncated %q to %d cells, limit was %d", out, Width(out), max); err != nil {
		return "", err
	}
	return out, nil
}

// Elide shortens a path from the left, keeping the end that identifies it. A
// truncated path whose tail is missing names nothing.
func Elide(path string, max int) (string, error) {
	if Width(path) <= max || max < 2 {
		return Truncate(path, max)
	}
	runes := []rune(path)
	for i := range runes {
		tail := string(runes[i:])
		if Width(tail)+1 <= max {
			return Ellipsis + tail, nil
		}
	}
	return Truncate(path, max)
}

// Pad extends s to exactly width cells. align is 'l' for left, 'r' for right,
// and 'c' for centred; anything else is an internal error rather than a silent
// default.
func Pad(s string, width int, align byte) (string, error) {
	if err := fault.Check(width >= 0, "style.Pad", "negative width %d", width); err != nil {
		return "", err
	}
	s, err := Truncate(s, width)
	if err != nil {
		return "", err
	}
	gap := width - Width(s)
	if err := fault.Check(gap >= 0, "style.Pad", "%q is %d cells, wider than %d", s, Width(s), width); err != nil {
		return "", err
	}
	switch align {
	case 'l':
		return s + strings.Repeat(" ", gap), nil
	case 'r':
		return strings.Repeat(" ", gap) + s, nil
	case 'c':
		left := gap / 2
		return strings.Repeat(" ", left) + s + strings.Repeat(" ", gap-left), nil
	default:
		return "", fault.Internal{Where: "style.Pad", Detail: "alignment must be 'l', 'r', or 'c', got " + string(align)}
	}
}
