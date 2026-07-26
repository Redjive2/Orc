package render

import (
	"strings"

	"orc/common/fault"
	"orc/orc/internal/style"
	"orc/theme"
)

// Align is a column's alignment.
type Align byte

// The alignments.
const (
	// Left aligns text.
	Left Align = 'l'
	// Right aligns numbers, so their digits line up.
	Right Align = 'r'
)

// Column describes one column of a ruled table.
type Column struct {
	Header string
	Align  Align
	// Min is the narrowest the column may become. A column narrower than its
	// header is unreadable, so the header's own width is the floor whatever this
	// says.
	Min int
	// Grow marks the column that gives up width when the table does not fit, and
	// absorbs leftover width when Stretch is set on the table.
	Grow bool
}

// Cell is one drawn value: its text, and how it should be coloured.
type Cell struct {
	Text  string
	Paint func(style.Palette, string) string
}

// Text makes an uncoloured cell.
func Text(s string) Cell { return Cell{Text: s} }

// Painted makes a cell with a role.
func Painted(s string, paint func(style.Palette, string) string) Cell {
	return Cell{Text: s, Paint: paint}
}

// Table is a ruled table: a title line, a header row, and rows of cells.
//
// It is ruled rather than boxed — a top and bottom rule with tick marks, no left
// or right border. The fleet screen is a tree, and a tree inside a box has two
// vertical lines competing at every depth; the indent is what carries the
// structure, so the frame stays out of its way.
type Table struct {
	Title   string
	Note    string
	Columns []Column
	Rows    [][]Cell
	Empty   string
	// Footer holds one footnote per entry, each wrapped and drawn on its own line.
	Footer []string
	// Stretch fills the terminal, widening the growing column. It is off by
	// default because a tree of five agents stretched across a wide terminal puts
	// the numbers a screen away from the names they belong to — content width is
	// the right width for a fleet, and a full-width table is the right one for a
	// list somebody scans.
	Stretch bool
}

// DrawTable renders a table.
func DrawTable(t Table, p style.Palette, width int) (string, error) {
	const where = "render.DrawTable"
	if err := fault.Check(len(t.Columns) > 0, where, "table %q has no columns", t.Title); err != nil {
		return "", err
	}
	for i, row := range t.Rows {
		if err := fault.Check(len(row) == len(t.Columns), where,
			"row %d of %q has %d cells but there are %d columns", i+1, t.Title, len(row), len(t.Columns)); err != nil {
			return "", err
		}
	}

	width = Clamp(width)
	widths := measure(t, width)

	var b strings.Builder
	b.WriteString(p.Frame(ruleOf(widths)))
	b.WriteString("\n")

	// The title line sits under the rule rather than in it: a fleet's title is a
	// sentence with counts in it, and a sentence threaded through a rule is a
	// sentence nobody can read.
	if t.Title != "" {
		line := p.Frame("[") + p.Title(theme.Sanitise(t.Title)) + p.Frame("]")
		if t.Note != "" {
			line += "  " + p.Muted(theme.Sanitise(t.Note))
		}
		b.WriteString(line)
		b.WriteString("\n")
	}

	if err := headerRow(&b, t, p, widths); err != nil {
		return "", err
	}

	if len(t.Rows) == 0 {
		b.WriteString(p.Frame(ruleEdge) + " " + p.Muted(or(t.Empty, "nothing here")))
		b.WriteString("\n")
	}
	for _, row := range t.Rows {
		if err := bodyRow(&b, row, t, p, widths); err != nil {
			return "", err
		}
	}

	b.WriteString(p.Frame(ruleOf(widths)))
	b.WriteString("\n")
	// Each footnote is wrapped on its own, so two of them never run into one
	// sentence — which is how "the boss chain caps it" and "no sessions" first read
	// as a single claim about sessions being capped.
	for _, note := range t.Footer {
		for _, line := range wrap(note, width) {
			b.WriteString(p.Muted(line))
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}

// measure decides the column widths: the widest cell in each, floored by the
// header, then the growing column absorbing whatever is left.
func measure(t Table, width int) []int {
	widths := make([]int, len(t.Columns))
	for i, c := range t.Columns {
		widths[i] = maxInt(theme.Width(c.Header), c.Min)
	}
	for _, row := range t.Rows {
		for i, cell := range row {
			if w := theme.Width(theme.Sanitise(cell.Text)); w > widths[i] {
				widths[i] = w
			}
		}
	}

	// Two leading characters for the rule edge, then two spaces between columns.
	used := 2
	for _, w := range widths {
		used += w + 2
	}
	if used >= width {
		// Too wide. The growing column gives up first, because that is what it is
		// for; then any other left-aligned column, because a truncated role name
		// still lines up. Right-aligned columns are never touched: they hold
		// numbers, and an authority level with a digit missing is a different
		// authority level.
		//
		// The second pass exists because the first is not enough on its own. A
		// table whose long content sits in a column that does not grow — a role
		// name on a narrow terminal — would otherwise draw a rule wider than the
		// terminal and wrap into nonsense, which is worse than a shortened word.
		over := used - width
		for _, pass := range [...]bool{true, false} {
			for i, c := range t.Columns {
				if over <= 0 {
					break
				}
				if c.Grow != pass || (!pass && c.Align == Right) {
					continue
				}
				floor := maxInt(theme.Width(c.Header), c.Min)
				if widths[i]-over < floor {
					over -= widths[i] - floor
					widths[i] = floor
					continue
				}
				widths[i] -= over
				over = 0
			}
		}
		return widths
	}

	if t.Stretch {
		for i, c := range t.Columns {
			if c.Grow {
				widths[i] += width - used
				break
			}
		}
	}
	return widths
}

// ruleOf draws the top and bottom rules, ticked at the column boundaries so the
// eye can find them without a vertical line down every row.
func ruleOf(widths []int) string {
	var b strings.Builder
	b.WriteString(ruleEdge)
	for _, w := range widths {
		b.WriteString(strings.Repeat(ruleDash, w+1))
		b.WriteString(ruleTick)
	}
	return strings.TrimSuffix(b.String(), ruleTick) + ruleEdge
}

func headerRow(b *strings.Builder, t Table, p style.Palette, widths []int) error {
	cells := make([]Cell, len(t.Columns))
	for i, c := range t.Columns {
		cells[i] = Cell{Text: c.Header, Paint: func(p style.Palette, s string) string { return p.Header(s) }}
	}
	return bodyRow(b, cells, t, p, widths)
}

func bodyRow(b *strings.Builder, row []Cell, t Table, p style.Palette, widths []int) error {
	b.WriteString(p.Frame(ruleEdge))
	for i, cell := range row {
		text := theme.Sanitise(cell.Text)
		if theme.Width(text) > widths[i] {
			trimmed, err := theme.Truncate(text, widths[i])
			if err != nil {
				return err
			}
			text = trimmed
		}
		padded, err := theme.Pad(text, widths[i], byte(t.Columns[i].Align))
		if err != nil {
			return err
		}

		// Colour is applied to the text and not to the padding, so escape codes
		// cannot change how wide the cell measures.
		if cell.Paint != nil && p.Enabled() {
			lead := padded[:len(padded)-len(strings.TrimLeft(padded, " "))]
			trail := strings.Repeat(" ", len(padded)-len(strings.TrimRight(padded, " ")))
			padded = lead + cell.Paint(p, strings.TrimSpace(padded)) + trail
		}
		b.WriteString(" " + padded + " ")
	}
	b.WriteString("\n")
	return nil
}

// Indent returns the tree stem for a depth, which is what makes the fleet screen
// a tree rather than a list.
func Indent(depth int) string {
	if depth <= 0 {
		return ""
	}
	return strings.Repeat(treeStem, depth)
}
