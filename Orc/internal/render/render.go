// Package render draws Orc's output: the fleet, the card, and the two lists.
//
// Rendering is two passes, as in every other tool here: a layout pass measures
// every cell, then a draw pass emits fixed-width rows. Both are pure functions of
// their input, and every width is clamped, so a degenerate fleet — one identity,
// no roles, a one-character name — still produces a well-formed screen.
//
// Orc's screens are not the weighted-column tables Macmuffin and Mailman draw,
// which is why this package is smaller than theirs rather than a copy of one: the
// fleet is a *tree* with aligned columns, and the card is a box of labelled
// sections. A shared table renderer in orc/common is the obvious refactor once
// three tools want the same one; four tools each having their own is the current
// state of the tree, and following it beats inventing a shared abstraction from a
// single new caller.
//
// Colour is a layer and never information. Every colour is redundant with a glyph
// or a word, which is what lets the golden tests run with colour off and still
// pin the whole layout.
package render

import (
	"strings"

	"orc/common/fault"
	"orc/orc/internal/style"
	"orc/theme"
)

// Box-drawing pieces. Kept in one table so the whole frame can be changed at
// once, and so no drawing code contains a literal line character.
const (
	horizontal = "─"
	vertical   = "│"

	cardTopLeft     = "┌"
	cardTopRight    = "┐"
	cardBottomLeft  = "└"
	cardBottomRight = "┘"
	cardTeeRight    = "├"
	cardTeeLeft     = "┤"

	// The fleet screen's rules, which are ruled rather than boxed: a tree with a
	// left border would have two vertical lines competing at every depth, and the
	// indent is what carries the structure.
	//
	// They are ASCII rather than box drawing, matching the shape `anno index`
	// prints. A rule made of ─ next to a row that starts with | reads as two
	// different frames that failed to meet.
	ruleEdge = "|"
	ruleDash = "-"
	ruleTick = ":"

	// The tree's own indent. Two spaces and a stem, so depth reads at a glance
	// without the row losing its left alignment.
	treeStem = "|  "
)

// Width bounds. The default is what a terminal that does not say otherwise is
// assumed to be; the minimum is what the narrowest useful screen needs.
const (
	DefaultWidth = 100
	MinWidth     = 48
	MaxWidth     = 400
)

// Glyphs. Every one of them is beside a word that says the same thing, so a
// terminal without them loses nothing that matters.
const (
	GlyphLive   = "●"
	GlyphIdle   = "○"
	GlyphDead   = "✗"
	GlyphNone   = "—"
	GlyphCapped = "‡"
)

// Clamp bounds a requested width.
func Clamp(width int) int {
	switch {
	case width <= 0:
		return DefaultWidth
	case width < MinWidth:
		return MinWidth
	case width > MaxWidth:
		return MaxWidth
	default:
		return width
	}
}

// Field is one labelled value in a card section.
type Field struct {
	Label string
	Value string
	// Note is dimmer text after the value: where a permission came from, why a
	// number was capped. It is the column that turns a card from a list of facts
	// into an explanation.
	Note string
	// Paint styles the value. Nil leaves it plain.
	Paint func(style.Palette, string) string
}

// Section is a titled group of fields in a card. An empty title runs the fields
// on without a divider, which is what the first section of every card does.
type Section struct {
	Title  string
	Fields []Field
	// Empty is drawn when the section has no fields. A section that would be
	// blank says why instead: "no permissions" is information, and a gap is not.
	Empty string
}

// Card is a box-drawn screen of labelled sections.
type Card struct {
	Title    string
	Note     string
	Sections []Section
	// Footer is a line under the last section, inside the box: a caveat, a next
	// step, a warning about what is not enforced.
	Footer string
}

// DrawCard renders a card.
func DrawCard(c Card, p style.Palette, width int) (string, error) {
	if err := fault.Check(strings.TrimSpace(c.Title) != "", "render.DrawCard", "card has no title"); err != nil {
		return "", err
	}
	width = Clamp(width)
	inner := width - 4 // the two borders and a space either side

	label := 0
	for _, s := range c.Sections {
		for _, f := range s.Fields {
			if w := theme.Width(f.Label); w > label {
				label = w
			}
		}
	}
	if label > inner/3 {
		label = inner / 3
	}

	var b strings.Builder
	if err := cardTop(&b, c, p, inner); err != nil {
		return "", err
	}
	for _, s := range c.Sections {
		if s.Title != "" {
			if err := cardDivider(&b, s.Title, p, inner); err != nil {
				return "", err
			}
		}
		if len(s.Fields) == 0 {
			if err := cardLine(&b, p, inner, p.Muted(or(s.Empty, "nothing"))); err != nil {
				return "", err
			}
			continue
		}
		for _, f := range s.Fields {
			if err := cardField(&b, f, p, inner, label); err != nil {
				return "", err
			}
		}
	}
	if c.Footer != "" {
		if err := cardDivider(&b, "", p, inner); err != nil {
			return "", err
		}
		for _, line := range wrap(c.Footer, inner) {
			if err := cardLine(&b, p, inner, p.Muted(line)); err != nil {
				return "", err
			}
		}
	}

	b.WriteString(p.Frame(cardBottomLeft + strings.Repeat(horizontal, inner+2) + cardBottomRight))
	b.WriteString("\n")
	return b.String(), nil
}

// cardTop draws the title bar: the title on the left, the note on the right.
func cardTop(b *strings.Builder, c Card, p style.Palette, inner int) error {
	title := theme.Sanitise(c.Title)
	note := theme.Sanitise(c.Note)

	// The title always survives; the note is what gives up space, because a note
	// is a summary of what the card already says and the title is what it is
	// about.
	if theme.Width(title)+theme.Width(note)+6 > inner {
		trimmed, err := theme.Truncate(note, maxInt(0, inner-theme.Width(title)-6))
		if err != nil {
			return err
		}
		note = trimmed
	}

	left := cardTopLeft + horizontal + " " + title + " "
	right := ""
	if note != "" {
		right = " " + note + " " + horizontal
	}
	fill := inner + 4 - theme.Width(left) - theme.Width(right) - 1
	if fill < 0 {
		fill = 0
	}

	b.WriteString(p.Frame(cardTopLeft+horizontal+" ") + p.Title(title) + p.Frame(" "+strings.Repeat(horizontal, fill)))
	if note != "" {
		b.WriteString(p.Muted(" "+note+" ") + p.Frame(horizontal))
	}
	b.WriteString(p.Frame(cardTopRight))
	b.WriteString("\n")
	return nil
}

// cardDivider draws a section rule with an optional label in it.
func cardDivider(b *strings.Builder, title string, p style.Palette, inner int) error {
	if title == "" {
		b.WriteString(p.Frame(cardTeeRight + strings.Repeat(horizontal, inner+2) + cardTeeLeft))
		b.WriteString("\n")
		return nil
	}
	label := theme.Sanitise(title)
	trimmed, err := theme.Truncate(label, maxInt(0, inner-4))
	if err != nil {
		return err
	}
	fill := inner + 2 - theme.Width(trimmed) - 3
	if fill < 0 {
		fill = 0
	}
	b.WriteString(p.Frame(cardTeeRight+horizontal+" ") + p.Header(trimmed) +
		p.Frame(" "+strings.Repeat(horizontal, fill)+cardTeeLeft))
	b.WriteString("\n")
	return nil
}

// cardLine draws one already-painted line inside the box.
//
// The padding is measured on the *unpainted* text, since escape sequences have no
// width — getting that wrong is what makes a coloured box close in the wrong
// column.
func cardLine(b *strings.Builder, p style.Palette, inner int, painted string) error {
	plain := theme.Sanitise(stripColour(painted))
	gap := inner - theme.Width(plain)
	if gap < 0 {
		gap = 0
	}
	b.WriteString(p.Frame(vertical) + " " + painted + strings.Repeat(" ", gap) + " " + p.Frame(vertical))
	b.WriteString("\n")
	return nil
}

// cardField draws one label/value/note row.
func cardField(b *strings.Builder, f Field, p style.Palette, inner, label int) error {
	name, err := theme.Pad(theme.Sanitise(f.Label), label, byte('l'))
	if err != nil {
		return err
	}

	value := theme.Sanitise(f.Value)
	note := theme.Sanitise(f.Note)
	room := inner - label - 2
	if room < 8 {
		room = 8
	}

	// The note is right-aligned against the box, and gives up space first: it is
	// an explanation of the value, and a truncated value would be a wrong fact
	// where a truncated explanation is only a shorter one.
	if theme.Width(value)+theme.Width(note)+2 > room {
		trimmed, err := theme.Truncate(note, maxInt(0, room-theme.Width(value)-2))
		if err != nil {
			return err
		}
		note = trimmed
	}
	if theme.Width(value) > room {
		trimmed, err := theme.Truncate(value, room)
		if err != nil {
			return err
		}
		value = trimmed
	}

	painted := value
	if f.Paint != nil {
		painted = f.Paint(p, value)
	}
	gap := room - theme.Width(value) - theme.Width(note)
	if gap < 0 {
		gap = 0
	}

	line := p.Header(name) + "  " + painted
	if note != "" {
		line += strings.Repeat(" ", gap) + p.Muted(note)
	}
	return cardLine(b, p, inner, line)
}

// wrap breaks text into lines of at most width, on spaces.
func wrap(text string, width int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	var out []string
	line := words[0]
	for _, w := range words[1:] {
		if theme.Width(line)+1+theme.Width(w) > width {
			out = append(out, line)
			line = w
			continue
		}
		line += " " + w
	}
	return append(out, line)
}

// stripColour removes escape sequences, so a painted string can be measured.
//
// Cells are painted before they are placed here, which is the opposite of how
// Macmuffin's table works — there, the renderer owns the painting and can measure
// the text before it colours it. A card's rows are assembled from several painted
// pieces, so the measurement has to happen after the fact, and this is what makes
// that safe.
func stripColour(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			// Skip to the end of the CSI sequence. An unterminated one runs to the
			// end of the string, which is the safe reading: it cannot be a width.
			j := i + 1
			for j < len(s) && s[j] != 'm' {
				j++
			}
			i = j + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func or(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
