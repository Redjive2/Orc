// Package render draws Dock's output.
//
// It is a pure function of its input: a document's sections, its link counts,
// and a palette. Nothing here reads a file or resolves a target, which is what
// lets the whole layer be pinned by golden tests that are readable in a diff.
//
// Layout is two passes, as in Anno's render. The first measures every cell, the
// second draws fixed-width rows. Measuring never sees an escape sequence, so a
// coloured table is aligned identically to a plain one.
//
// The number carries the depth, so there is no indent gutter — the name is
// indented instead, which keeps the numbers in one scannable column. That is
// frugality applied to the drawing: nothing is printed that the reader can
// already see.
package render

import (
	"fmt"
	"strings"

	"orc/common/fault"
	"orc/dock/internal/doc"
	"orc/dock/internal/scan"
	"orc/dock/internal/style"
)

// Unknown is the inbound count of a section whose backlinks were not computed.
// It renders as "?" rather than as zero: a count nobody measured must not read
// as a count of none.
const Unknown = -1

// Minimum column widths, so a degenerate document — no sections, no names, no
// links — still produces a well-formed frame rather than a collapsed one.
const (
	minNumber = 4
	minName   = 8
	minCount  = 7
	minRange  = 7

	// maxFileCell bounds how far a long path may stretch the table. Beyond it
	// the path is elided from the front, because a path's tail says what the
	// file is and its head says only where it started. Without this, reading a
	// document by absolute path draws a table wider than any terminal.
	maxFileCell = 48
)

// Counts are one section's edges.
type Counts struct {
	// Out is how many links the section declares.
	Out int
	// In is how many links point at it, or Unknown.
	In int
}

// Row is one line of the index.
type Row struct {
	// Number is the section number without its §, or "" for the file row.
	Number string
	// Name is the section's name, or the file's path on the file row.
	Name string
	// Depth is 0 for the file row and the section's depth otherwise.
	Depth int
	// Counts are the section's edges.
	Counts Counts
	// Span is the range the row reports, already narrowed to content.
	Span doc.Range
}

// Index is a document's table, ready to draw.
type Index struct {
	rows []Row
}

// Rows returns a copy of the rows, file row first.
func (ix Index) Rows() []Row { return append([]Row(nil), ix.rows...) }

// Len returns how many rows the index has, the file row included.
func (ix Index) Len() int { return len(ix.rows) }

// BuildIndex assembles a document's table.
//
// Every section reports its tree span — the whole of what it covers — because
// the index answers "what is in this document and how big is each part", which
// is the question an agent asks before deciding what to read. The narrower own
// span is what read returns, and the two are different questions.
//
// counts may be nil, in which case every section reports no outbound links and
// an unknown inbound count.
func BuildIndex(d doc.Doc, lines []scan.Line, counts map[string]Counts) (Index, error) {
	if len(lines) != d.Lines() {
		return Index{}, fault.Internal{
			Where:  "render.BuildIndex",
			Detail: fmt.Sprintf("given %d lines for a %d line document", len(lines), d.Lines()),
		}
	}

	// The file row reports a raw span, never a content range: a document's own
	// extent is what it is, blank lines included. Anno's root row does the same.
	file := Row{
		Name: d.Path(),
		Span: doc.NewRange(1, d.Lines()),
	}

	ix := Index{rows: make([]Row, 0, d.Len()+1)}
	total := Counts{In: Unknown}
	known := true

	rows := make([]Row, 0, d.Len())
	for _, s := range d.Sections() {
		c := Counts{In: Unknown}
		if counts != nil {
			if got, ok := counts[s.Number()]; ok {
				c = got
			} else {
				c = Counts{Out: 0, In: Unknown}
			}
		}
		total.Out += c.Out
		if c.In == Unknown {
			known = false
		} else if known {
			if total.In == Unknown {
				total.In = 0
			}
			total.In += c.In
		}
		rows = append(rows, Row{
			Number: s.Number(),
			Name:   s.Name(),
			Depth:  s.Depth(),
			Counts: c,
			Span:   d.Content(lines, s.Tree()),
		})
	}
	if !known {
		total.In = Unknown
	}
	file.Counts = total

	ix.rows = append(ix.rows, file)
	ix.rows = append(ix.rows, rows...)
	return ix, nil
}

// Table draws the index.
func Table(ix Index, pal style.Palette) string {
	if ix.Len() == 0 {
		return ""
	}

	// Pass one: measure.
	w := widths{number: minNumber, name: minName, count: minCount, span: minRange}
	cells := make([]cellRow, 0, ix.Len())
	fileWidth := 0
	for _, r := range ix.rows {
		c := measure(r)
		if c.file {
			// The file row spans the number and name columns, as Anno's root
			// row does. Measuring its path against the number column alone
			// would push every section number across the table to make room
			// for a long filename.
			fileWidth = style.Width(c.number)
		} else {
			w.number = max(w.number, style.Width(c.number))
			w.name = max(w.name, style.Width(c.name))
		}
		w.links = max(w.links, style.Width(c.links))
		w.count = max(w.count, style.Width(c.count))
		w.span = max(w.span, style.Width(c.span))
		cells = append(cells, c)
	}
	if span := w.number + 1 + w.name; fileWidth > span {
		w.name += fileWidth - span
	}

	// Pass two: draw.
	var b strings.Builder
	rule := w.rule()
	b.WriteString(pal.Paint(rule, style.Frame))
	b.WriteByte('\n')
	for i, c := range cells {
		b.WriteString(w.draw(c, i == 0, pal))
		b.WriteByte('\n')
	}
	b.WriteString(pal.Paint(rule, style.Frame))
	b.WriteByte('\n')
	return b.String()
}

type cellRow struct {
	number string
	name   string
	links  string
	count  string
	span   string
	file   bool
	depth  int
}

func measure(r Row) cellRow {
	c := cellRow{depth: r.Depth}
	if r.Number == "" {
		c.file = true
		c.number = "[" + style.TruncateLeft(r.Name, maxFileCell-2) + "]"
		c.name = ""
	} else {
		c.number = doc.Sigil + r.Number
		c.name = strings.Repeat("  ", max(0, r.Depth-1)) + r.Name
	}
	c.links = fmt.Sprintf("→%s ←%s", count(r.Counts.Out), count(r.Counts.In))
	c.count = plural(r.Span.Len())
	c.span = r.Span.String()
	if r.Span.Empty() {
		c.span = "<empty>"
	}
	return c
}

func count(n int) string {
	if n == Unknown {
		return "?"
	}
	return fmt.Sprint(n)
}

func plural(n int) string {
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
}

type widths struct {
	number int
	name   int
	links  int
	count  int
	span   int
}

func (w widths) rule() string {
	return "|" + strings.Repeat("-", w.number+w.name+3) +
		"|" + strings.Repeat("-", w.links+2) +
		"|" + strings.Repeat("-", w.count+w.span+3) + "|"
}

func (w widths) draw(c cellRow, file bool, pal style.Palette) string {
	var b strings.Builder

	b.WriteString(pal.Paint("|", style.Frame))
	b.WriteByte(' ')
	if file {
		// One cell across both columns, plus the separator they would have had.
		b.WriteString(pal.Paint(style.Pad(c.number, w.number+1+w.name), style.Name))
	} else {
		b.WriteString(pal.Paint(style.Pad(c.number, w.number), style.Number))
		b.WriteByte(' ')
		b.WriteString(pal.Paint(style.Pad(c.name, w.name), style.Name))
	}
	b.WriteByte(' ')

	b.WriteString(pal.Paint("|", style.Frame))
	b.WriteByte(' ')
	b.WriteString(paintLinks(style.Pad(c.links, w.links), pal))
	b.WriteByte(' ')

	b.WriteString(pal.Paint("|", style.Frame))
	b.WriteByte(' ')
	b.WriteString(pal.Paint(style.PadLeft(c.count, w.count), style.Quiet))
	b.WriteByte(' ')
	b.WriteString(pal.Paint(style.PadLeft(c.span, w.span), style.Span))
	b.WriteByte(' ')
	b.WriteString(pal.Paint("|", style.Frame))

	return b.String()
}

// paintLinks colours the two directions separately, so they stay tellable apart
// at a glance, and leaves the padding uncoloured so the width is unchanged.
func paintLinks(s string, pal style.Palette) string {
	if !pal.Enabled() {
		return s
	}
	i := strings.Index(s, " ←")
	if i < 0 {
		return pal.Paint(s, style.Out)
	}
	return pal.Paint(s[:i], style.Out) + " " + pal.Paint(strings.TrimRight(s[i+1:], " "), style.In) +
		strings.Repeat(" ", len(s)-len(strings.TrimRight(s, " ")))
}
