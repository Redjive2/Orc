// Package render draws Mailman's output.
//
// Rendering is two passes, as in Anno: a layout pass measures every cell, then
// a draw pass emits fixed-width rows. Both are pure functions of their input,
// and every width is clamped to a sane minimum so a degenerate mailbox — no
// mail, an empty subject, a one-character name — still produces a well-formed
// table.
//
// Colour is a layer and never information. Every colour is redundant with a
// glyph or a word, so a pipe through grep and a NO_COLOR terminal lose nothing
// but the pleasure. That is also what lets the golden tests run with colour off
// and still pin the whole layout.
package render

import (
	"strings"

	"orc/common/fault"
	"orc/mailman/internal/style"
)

// Box-drawing pieces. Kept in one table so the whole frame can be changed at
// once, and so no drawing code contains a literal line character.
const (
	topLeft     = "┌"
	topRight    = "┐"
	bottomLeft  = "└"
	bottomRight = "┘"
	horizontal  = "─"
	vertical    = "│"
	teeDown     = "┬"
	teeUp       = "┴"
	teeRight    = "├"
	teeLeft     = "┤"
	cross       = "┼"

	cardTopLeft     = "╭"
	cardTopRight    = "╮"
	cardBottomLeft  = "╰"
	cardBottomRight = "╯"

	threadStem   = "│"
	threadBranch = "├─"
	threadLast   = "╰─"
)

// Width bounds. The default is what a terminal that does not say otherwise is
// assumed to be; the minimum is what the narrowest useful table needs.
const (
	DefaultWidth = 100
	MinWidth     = 40
	MaxWidth     = 400
)

// Align is a column's alignment.
type Align byte

const (
	// Left aligns text.
	Left Align = 'l'
	// Right aligns numbers, so their digits line up.
	Right Align = 'r'
	// Centre aligns headings.
	Centre Align = 'c'
)

// Column describes one column of a table.
type Column struct {
	// Header is the label drawn above the column.
	Header string
	// Align is how cells sit in the column.
	Align Align
	// Weight is how eagerly the column gives up space when the table is too
	// wide. Zero never shrinks; higher shrinks sooner.
	Weight int
	// Min is the narrowest the column may become.
	Min int
}

// Cell is one drawn value: its text, and how that text should be coloured.
type Cell struct {
	Text  string
	Paint func(style.Palette, string) string
}

// Plain makes an uncoloured cell.
func Plain(text string) Cell { return Cell{Text: text} }

// Painted makes a cell with a role.
func Painted(text string, paint func(style.Palette, string) string) Cell {
	return Cell{Text: text, Paint: paint}
}

func (c Cell) render(p style.Palette, width int, align Align) (string, error) {
	// Sanitised first: a subject is attacker-controlled text, and an escape
	// sequence smuggled into one would repaint every row below it.
	text := style.Sanitise(c.Text)
	padded, err := style.Pad(text, width, byte(align))
	if err != nil {
		return "", err
	}
	if c.Paint == nil || !p.Enabled() {
		return padded, nil
	}

	// Colour is applied to the text and not to the padding, so the escape codes
	// cannot change how wide the cell measures.
	trimmed := strings.TrimRight(padded, " ")
	gap := len(padded) - len(trimmed)
	lead := ""
	if align == Right || align == Centre {
		lead = padded[:len(padded)-len(strings.TrimLeft(padded, " "))]
		trimmed = strings.TrimLeft(trimmed, " ")
	}
	return lead + c.Paint(p, trimmed) + strings.Repeat(" ", gap), nil
}

// Table is a box-drawn table with a title bar.
type Table struct {
	// Title is drawn in the top bar, on the left.
	Title string
	// Note is drawn in the top bar, on the right: a count, a summary.
	Note string
	// Columns describe the layout.
	Columns []Column
	// Rows are the cells, one slice per row, each as long as Columns.
	Rows [][]Cell
	// Empty is drawn instead of any rows when there are none.
	Empty string
}

// Draw renders the table.
func Draw(t Table, p style.Palette, width int) (string, error) {
	if err := t.validate(); err != nil {
		return "", err
	}
	width = clampWidth(width)

	widths, err := measure(t, width)
	if err != nil {
		return "", err
	}
	widen(t, widths, width)

	// The table's own width follows from the columns, so the frame always
	// closes even when the terminal is wider than the content needs.
	inner := 0
	for _, w := range widths {
		inner += w + 2 // one space of padding either side
	}
	inner += len(widths) - 1 // the separators between columns

	var b strings.Builder
	if err := drawTitle(&b, t, p, inner); err != nil {
		return "", err
	}
	if err := drawHeader(&b, t, p, widths); err != nil {
		return "", err
	}

	if len(t.Rows) == 0 {
		if err := drawEmpty(&b, t, p, inner); err != nil {
			return "", err
		}
	} else {
		for _, row := range t.Rows {
			if err := drawRow(&b, t, p, widths, row); err != nil {
				return "", err
			}
		}
	}

	b.WriteString(p.Frame(rule(bottomLeft, teeUp, bottomRight, widths)))
	b.WriteString("\n")
	return b.String(), nil
}

func (t Table) validate() error {
	const where = "render.Table"
	if err := fault.Check(len(t.Columns) > 0, where, "table %q has no columns", t.Title); err != nil {
		return err
	}
	for i, row := range t.Rows {
		if err := fault.Check(len(row) == len(t.Columns), where,
			"row %d of %q has %d cells but there are %d columns", i+1, t.Title, len(row), len(t.Columns)); err != nil {
			return err
		}
	}
	for i, c := range t.Columns {
		switch c.Align {
		case Left, Right, Centre:
		default:
			return fault.Internal{Where: where, Detail: "column " + c.Header + " has an unknown alignment"}
		}
		if err := fault.Check(c.Min >= 0, where, "column %d has minimum width %d", i+1, c.Min); err != nil {
			return err
		}
	}
	return nil
}

func clampWidth(width int) int {
	switch {
	case width < MinWidth:
		return DefaultWidth
	case width > MaxWidth:
		return MaxWidth
	default:
		return width
	}
}

// measure computes each column's width.
//
// Columns start at their natural width — the widest cell, or the header — and
// are then shrunk, weightiest first, until the table fits. A column never goes
// below its own minimum, so a table that cannot fit stays readable and simply
// overruns rather than collapsing to a column of ellipses.
func measure(t Table, width int) ([]int, error) {
	widths := make([]int, len(t.Columns))
	for i, c := range t.Columns {
		widths[i] = maxInt(style.Width(c.Header), c.Min, 1)
	}
	for _, row := range t.Rows {
		for i, cell := range row {
			if w := style.Width(style.Sanitise(cell.Text)); w > widths[i] {
				widths[i] = w
			}
		}
	}

	// The frame costs two verticals, one separator per gap, and two spaces of
	// padding per column.
	overhead := 2 + (len(widths) - 1) + 2*len(widths)

	for {
		total := overhead
		for _, w := range widths {
			total += w
		}
		if total <= width {
			break
		}

		// Shrink the weightiest column that still has room. Ties go to the
		// widest, so a table narrows evenly rather than crushing one column.
		best, bestScore := -1, 0
		for i, c := range t.Columns {
			floor := maxInt(c.Min, 3)
			if c.Weight == 0 || widths[i] <= floor {
				continue
			}
			score := c.Weight*1000 + widths[i]
			if score > bestScore {
				best, bestScore = i, score
			}
		}
		if best < 0 {
			break // nothing left to give; the table overruns rather than breaking
		}
		widths[best]--
	}

	for i, w := range widths {
		if err := fault.Check(w >= 1, "render.measure", "column %d measured %d", i+1, w); err != nil {
			return nil, err
		}
	}
	return widths, nil
}

// widen grows the table so its title bar is not clipped.
//
// Column widths follow the cells, and a table of short cells can easily be
// narrower than its own heading — a one-column list of user names under the
// title "mailboxes" is the ordinary case. Truncating the title there would be
// absurd, so the deficit is given to the most flexible column instead, up to
// the terminal's width.
func widen(t Table, widths []int, width int) {
	need := style.Width(style.Sanitise(t.Title)) + style.Width(style.Sanitise(t.Note)) + 3
	if empty := style.Width(style.Sanitise(t.Empty)) + 2; len(t.Rows) == 0 && empty > need {
		need = empty
	}

	inner := (len(widths) - 1) + 2*len(widths)
	for _, w := range widths {
		inner += w
	}
	// Never past the terminal: a title too long for the screen is truncated,
	// which is the one case where clipping it is the lesser evil.
	if limit := width - 2; need > limit {
		need = limit
	}
	if inner >= need {
		return
	}

	// The weightiest column is the one that gave up space first when the table
	// was too wide, so it is the one that takes it back.
	target := 0
	for i, c := range t.Columns {
		if c.Weight > t.Columns[target].Weight {
			target = i
		}
	}
	widths[target] += need - inner
}

// rule draws a horizontal frame line with the given corners and junction.
func rule(left, mid, right string, widths []int) string {
	var b strings.Builder
	b.WriteString(left)
	for i, w := range widths {
		if i > 0 {
			b.WriteString(mid)
		}
		b.WriteString(strings.Repeat(horizontal, w+2))
	}
	b.WriteString(right)
	return b.String()
}

// span returns the total inner width, for a bar that has no column divisions.
func drawTitle(b *strings.Builder, t Table, p style.Palette, inner int) error {
	b.WriteString(p.Frame(topLeft + strings.Repeat(horizontal, inner) + topRight))
	b.WriteString("\n")

	title := style.Sanitise(t.Title)
	note := style.Sanitise(t.Note)

	// The title takes what it needs and the note takes the rest, so a long
	// title never pushes the note off the end of the bar.
	room := inner - 2
	if room < 1 {
		room = 1
	}
	noteWidth := minInt(style.Width(note), room/2)
	titleWidth := room - noteWidth
	if titleWidth < 1 {
		titleWidth, noteWidth = room, 0
	}

	left, err := style.Pad(title, titleWidth, 'l')
	if err != nil {
		return err
	}
	right, err := style.Pad(note, noteWidth, 'r')
	if err != nil {
		return err
	}

	b.WriteString(p.Frame(vertical))
	b.WriteString(" ")
	b.WriteString(paint(p, p.Title, left))
	b.WriteString(paint(p, p.Muted, right))
	b.WriteString(" ")
	b.WriteString(p.Frame(vertical))
	b.WriteString("\n")
	return nil
}

func drawHeader(b *strings.Builder, t Table, p style.Palette, widths []int) error {
	b.WriteString(p.Frame(rule(teeRight, teeDown, teeLeft, widths)))
	b.WriteString("\n")

	b.WriteString(p.Frame(vertical))
	for i, c := range t.Columns {
		if i > 0 {
			b.WriteString(p.Frame(vertical))
		}
		cell, err := Painted(c.Header, style.Palette.Header).render(p, widths[i], c.Align)
		if err != nil {
			return err
		}
		b.WriteString(" " + cell + " ")
	}
	b.WriteString(p.Frame(vertical))
	b.WriteString("\n")

	b.WriteString(p.Frame(rule(teeRight, cross, teeLeft, widths)))
	b.WriteString("\n")
	return nil
}

func drawRow(b *strings.Builder, t Table, p style.Palette, widths []int, row []Cell) error {
	b.WriteString(p.Frame(vertical))
	for i, cell := range row {
		if i > 0 {
			b.WriteString(p.Frame(vertical))
		}
		drawn, err := cell.render(p, widths[i], t.Columns[i].Align)
		if err != nil {
			return err
		}
		b.WriteString(" " + drawn + " ")
	}
	b.WriteString(p.Frame(vertical))
	b.WriteString("\n")
	return nil
}

func drawEmpty(b *strings.Builder, t Table, p style.Palette, inner int) error {
	text := t.Empty
	if text == "" {
		text = "nothing here"
	}
	padded, err := style.Pad(style.Sanitise(text), inner-2, 'c')
	if err != nil {
		return err
	}
	b.WriteString(p.Frame(vertical))
	b.WriteString(" ")
	b.WriteString(paint(p, p.Muted, padded))
	b.WriteString(" ")
	b.WriteString(p.Frame(vertical))
	b.WriteString("\n")
	return nil
}

// paint applies a role only when the text has something in it, so an empty cell
// never carries escape codes.
func paint(p style.Palette, role func(string) string, text string) string {
	if !p.Enabled() || strings.TrimSpace(text) == "" {
		return text
	}
	trimmed := strings.TrimRight(text, " ")
	gap := text[len(trimmed):]
	lead := trimmed[:len(trimmed)-len(strings.TrimLeft(trimmed, " "))]
	return lead + role(strings.TrimLeft(trimmed, " ")) + gap
}

func maxInt(vals ...int) int {
	best := vals[0]
	for _, v := range vals[1:] {
		if v > best {
			best = v
		}
	}
	return best
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
