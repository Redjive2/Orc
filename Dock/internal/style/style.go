// Package style adds colour to Dock's output, and measures text the way a
// terminal lays it out.
//
// A Palette is a value: the zero Palette is plain, so every code path that has
// not asked for colour gets none, and tests comparing exact bytes keep working
// without opting out of anything.
//
// Colour is applied only when writing, never while measuring. Escape sequences
// occupy no columns, so a table aligned in plain text stays aligned in colour —
// the layout pass never sees a code.
//
// The colours come from orc/theme, the scheme every Orc tool shares. Nothing
// here decides what a number or a span looks like; it only decides what *is* a
// number or a span. That indirection is what lets one setting restyle every
// tool at once, and what stops dock and anno disagreeing about what green means.
package style

import (
	"strings"
	"unicode"

	"orc/theme"
)

// Ink is the role a piece of Dock's output plays. It names what the text is,
// not what colour it should be.
type Ink int

const (
	// None leaves text untouched.
	None Ink = iota
	// Number is a section number: the address, and the thing a reader is
	// looking for so they can paste it back.
	Number
	// Name is a section's name.
	Name
	// Quiet is structure that should recede — rules, counts, indent.
	Quiet
	// Frame is box drawing.
	Frame
	// Span is a line range.
	Span
	// Out marks outbound links.
	Out
	// In marks inbound links.
	In
	// Label is a link's text, an aside the eye can skip.
	Label
	// Foreign marks a target another tool resolves — an Anno chain — which is
	// unusual in a document without being wrong.
	Foreign
	// Good reports a satisfied condition.
	Good
	// Alarm reports a dangling link or a fault.
	Alarm
	// Tool is dock's own name, where the help introduces it.
	Tool
	// Command is a command word in the help.
	Command
	// Flag is a flag in the help.
	Flag
	// Value is a placeholder in the help: <target>, <dir>.
	Value
	// Setting is an environment variable in the help.
	Setting
	inkCount
)

// Valid reports whether i is a defined ink.
func (i Ink) Valid() bool { return i >= None && i < inkCount }

// paints maps each ink to a role in the shared scheme, and whether it is drawn
// with extra weight. Out and In take two distinct hues because telling the two
// directions apart at a glance is the whole point of colouring an index.
var paints = [inkCount]struct {
	role   theme.Role
	strong bool
}{
	None:    {},
	Number:  {role: theme.Primary, strong: true},
	Name:    {role: theme.Heading},
	Quiet:   {role: theme.Muted},
	Frame:   {role: theme.Frame},
	Span:    {role: theme.Info},
	Out:     {role: theme.Secondary},
	In:      {role: theme.Tertiary},
	Label:   {role: theme.Subtle},
	Foreign: {role: theme.Accent},
	Good:    {role: theme.Success},
	Alarm:   {role: theme.Danger, strong: true},

	// The help's vocabulary takes the same roles muff gives it, so the two
	// tools' help pages look like pages of one manual rather than two.
	Tool:    {role: theme.Title, strong: true},
	Command: {role: theme.Primary, strong: true},
	Flag:    {role: theme.Tertiary},
	Value:   {role: theme.Secondary},
	Setting: {role: theme.Info},
}

// Palette renders text with a role, or plainly. The zero value is plain.
type Palette struct {
	inner theme.Palette
}

// New wraps a resolved scheme.
func New(p theme.Palette) Palette { return Palette{inner: p} }

// Plain returns a palette that never colours anything.
func Plain() Palette { return Palette{} }

// Coloured returns a palette in the default flavour, for tests that want to see
// the sequences without owning a terminal.
func Coloured() Palette {
	return Palette{inner: theme.New(theme.Default, theme.TrueColour)}
}

// Enabled reports whether the palette emits escape sequences.
func (p Palette) Enabled() bool { return p.inner.Enabled() }

// Scheme returns the underlying palette, for naming the flavour in force.
func (p Palette) Scheme() theme.Palette { return p.inner }

// Paint wraps text in a role. A disabled palette, the None ink, an undefined
// ink, or empty text all return the text untouched, so Paint is always safe to
// call and never lengthens output that would not benefit.
func (p Palette) Paint(text string, ink Ink) string {
	if !p.Enabled() || text == "" || ink == None || !ink.Valid() {
		return text
	}
	spec := paints[ink]
	if spec.strong {
		return p.inner.Strong(text, spec.role)
	}
	return p.inner.Paint(text, spec.role)
}

// Width measures how many terminal columns text occupies.
//
// This is not len, and it is not the rune count: a § is one column and three
// bytes, and a CJK name is two columns per rune. Every table aligns on this, so
// a wrong answer here shears a column — which is why measurement lives with the
// terminal knowledge rather than with the drawing code.
func Width(text string) int {
	n := 0
	for _, r := range text {
		switch {
		case r == '\t':
			n += 4 // a tab in a cell is a nuisance; count it as a fixed stop
		case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r):
			// A combining mark occupies no column of its own.
		case wide(r):
			n += 2
		case r < 0x20 || r == 0x7f:
			// A control character is not drawn.
		default:
			n++
		}
	}
	return n
}

// wide reports whether a rune occupies two columns. The ranges are the East
// Asian Wide and Fullwidth blocks, plus the emoji that behave like them.
func wide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115f, // Hangul Jamo
		r >= 0x2e80 && r <= 0x303e, // CJK radicals, Kangxi
		r >= 0x3041 && r <= 0x33ff, // Hiragana through CJK compatibility
		r >= 0x3400 && r <= 0x4dbf, // CJK extension A
		r >= 0x4e00 && r <= 0x9fff, // CJK unified ideographs
		r >= 0xa000 && r <= 0xa4cf, // Yi
		r >= 0xac00 && r <= 0xd7a3, // Hangul syllables
		r >= 0xf900 && r <= 0xfaff, // CJK compatibility ideographs
		r >= 0xfe30 && r <= 0xfe6f, // CJK compatibility forms
		r >= 0xff00 && r <= 0xff60, // Fullwidth forms
		r >= 0xffe0 && r <= 0xffe6,
		r >= 0x1f300 && r <= 0x1f64f, // emoji
		r >= 0x1f900 && r <= 0x1f9ff,
		r >= 0x20000 && r <= 0x3fffd: // CJK extension B and beyond
		return true
	}
	return false
}

// Truncate shortens text to at most width columns, marking the cut with an
// ellipsis. A row is always one line and columns always align, so an overlong
// cell is cut rather than wrapped — the full value is always available from
// read.
func Truncate(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if Width(text) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	out := make([]rune, 0, len(text))
	n := 0
	for _, r := range text {
		w := Width(string(r))
		if n+w > width-1 {
			break
		}
		out = append(out, r)
		n += w
	}
	return string(out) + "…"
}

// TruncateLeft shortens text to at most width columns by cutting the front,
// marking the cut with an ellipsis.
//
// It is for paths, where the tail carries the information: "…/guide/api.md"
// says what the file is and "/very/long/prefix/gu…" says only where it started.
func TruncateLeft(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if Width(text) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	runes := []rune(text)
	out := make([]rune, 0, len(runes))
	n := 0
	for i := len(runes) - 1; i >= 0; i-- {
		w := Width(string(runes[i]))
		if n+w > width-1 {
			break
		}
		out = append([]rune{runes[i]}, out...)
		n += w
	}
	return "…" + string(out)
}

// Pad extends text to exactly width columns, adding spaces on the right, and
// truncates it when it is too long. The result always measures width, which is
// the invariant the whole table rests on.
func Pad(text string, width int) string {
	text = Truncate(text, width)
	return text + strings.Repeat(" ", gap(text, width))
}

// PadLeft is Pad, right-aligned, for numbers.
func PadLeft(text string, width int) string {
	text = Truncate(text, width)
	return strings.Repeat(" ", gap(text, width)) + text
}

func gap(text string, width int) int {
	if n := width - Width(text); n > 0 {
		return n
	}
	return 0
}
