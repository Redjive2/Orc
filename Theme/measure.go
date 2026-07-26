package theme

// Terminal measurement.
//
// This lives beside the colour scheme because it is the same kind of knowledge:
// how a terminal will lay text out. Every table in every Orc tool aligns on
// Width, so a wrong answer here shears a column — and three tools each carrying
// their own copy is three chances for one of them to drift.

import (
	"strings"
	"unicode"

	"orc/common/fault"
)

// Width returns how many terminal cells s occupies.
//
// Three classes of rune are not one cell wide: combining marks and other
// zero-width formatting take none, East Asian wide and fullwidth forms take
// two, and control characters are refused outright rather than guessed at,
// since a stray escape sequence in a subject line would corrupt every row below
// it.
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
// ranges that terminals render double-width. The table is deliberately coarse:
// being right about CJK, Hangul, and common emoji covers what an agent will
// actually put in a subject line, and a wrong guess in an exotic block costs a
// misaligned column, not a failure.
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
// Control characters are the hazard: a tab breaks alignment, a newline breaks
// the row, and an escape sequence smuggled through a subject line could repaint
// the whole table. Each is replaced with a visible substitute rather than
// dropped, so the caller can see that something was there.
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
//
// Cells are never wrapped: a row that becomes two rows destroys the alignment
// that makes a table scannable, and the full value is always one `open` away.
func Truncate(s string, max int) (string, error) {
	if err := fault.Check(max >= 0, "Truncate", "negative width %d", max); err != nil {
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
	if err := fault.Check(Width(out) <= max, "Truncate",
		"truncated %q to %d cells, limit was %d", out, Width(out), max); err != nil {
		return "", err
	}
	return out, nil
}

// Pad extends s to exactly width cells. align is 'l' for left, 'r' for right,
// and 'c' for centred; anything else is an internal error rather than a silent
// default, because a mis-typed alignment should not quietly produce a table
// that looks almost right.
func Pad(s string, width int, align byte) (string, error) {
	if err := fault.Check(width >= 0, "Pad", "negative width %d", width); err != nil {
		return "", err
	}
	s, err := Truncate(s, width)
	if err != nil {
		return "", err
	}
	gap := width - Width(s)
	if err := fault.Check(gap >= 0, "Pad", "%q is %d cells, wider than %d", s, Width(s), width); err != nil {
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
		return "", fault.Internal{Where: "Pad", Detail: "alignment must be 'l', 'r', or 'c', got " + string(align)}
	}
}
