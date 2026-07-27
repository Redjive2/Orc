// Package render draws an annotation tree as the index table.
//
// Rendering is two passes: a layout pass measures every column, then a draw
// pass emits fixed-width rows. Both are pure functions of the tree, and every
// width is clamped to a sane minimum so a degenerate tree — no annotations, no
// metadata, a zero-line file — still produces a well-formed table.
package render

import (
	"fmt"
	"strings"

	"orc/anno/internal/marker"
	"orc/anno/internal/style"
	"orc/anno/internal/tree"
	"orc/common/fault"
)

// inkFor is the attribute each annotation kind's word is drawn in. Kinds keep
// the same colour everywhere they appear, so depth is readable at a glance
// without counting indent bars.
func inkFor(kind string) style.Ink {
	switch kind {
	case marker.Section.String():
		return style.Section
	case marker.Symbol.String():
		return style.Symbol
	case marker.Part.String():
		return style.Part
	default:
		return style.None
	}
}

// indent is the width of one tree level, drawn as "|  ".
const indent = 3

// row is one line of the table, flattened from the tree.
type row struct {
	depth int    // 0 for the file row
	kind  string // empty for the file row
	name  string // bracketed for the file row
	meta  []string
	lines int
	start int
	end   int
}

// layout holds the measurements shared by every row.
type layout struct {
	nameCol  int   // column at which names begin
	nameEnd  int   // column at which the metadata bracket begins
	metaCols []int // width of each metadata slot
	metaSpan int   // total width inside the brackets
	countW   int   // width of the line-count number
	rangeW   int   // width of each range bound
	minName  int   // shallowest row's name start, marked on the top rule
	maxDepth int   // deepest row, whose indents are marked on the bottom rule
}

// Index renders a whole tree, including the file row.
func Index(t tree.Tree, p style.Palette) (string, error) { return IndexAs(t, "", p) }

// IndexAs is Index with the file row named explicitly.
//
// A tree knows its file by base name, which is right when somebody named that file:
// they typed the path, and repeating it back adds nothing. It is wrong in an
// overview of a whole tree, where four packages each hold a `cli.go` and four
// identical headers say nothing about which one is being read. There the caller
// knows the root the sweep started from and can name each file relative to it.
//
// An empty name means the tree's own, so the common case says nothing extra.
func IndexAs(t tree.Tree, name string, p style.Palette) (string, error) {
	rows := flatten(t)
	if name != "" && len(rows) > 0 {
		rows[0].name = "[" + name + "]"
	}
	l, err := measure(rows)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(p.Paint(rule(l, true), style.Quiet))
	b.WriteByte('\n')
	for _, r := range rows {
		line, err := draw(r, l, p)
		if err != nil {
			return "", err
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString(p.Paint(rule(l, false), style.Quiet))
	b.WriteByte('\n')
	return b.String(), nil
}

// Row renders a single node as one table line, sized to itself. It is used by
// `anno find`, which reports matches one at a time rather than as a table.
func Row(t tree.Tree, path []tree.Node, p style.Palette) (string, error) {
	if len(path) == 0 {
		return "", fault.Internal{Where: "render.Row", Detail: "empty node path"}
	}
	n := path[len(path)-1]
	d := n.Display()
	r := row{
		depth: len(path),
		kind:  n.Kind().String(),
		name:  n.Name(),
		meta:  n.Meta(),
		lines: n.Lines(),
		start: d.Start(),
		end:   d.End(),
	}
	l, err := measure([]row{r})
	if err != nil {
		return "", err
	}
	return draw(r, l, p)
}

// flatten walks the tree in file order, producing one row per node beneath the
// file row. It cannot fail: tree.Build validates every node's kind before a
// Tree exists, so there is nothing left here to check.
func flatten(t tree.Tree) []row {
	rows := []row{{
		depth: 0,
		name:  "[" + t.Name() + "]",
		lines: t.Count(),
		start: 1,
		end:   t.Count(),
	}}
	if t.Count() == 0 {
		rows[0].start, rows[0].end = 0, 0
	}

	var walk func(nodes []tree.Node, depth int)
	walk = func(nodes []tree.Node, depth int) {
		for _, n := range nodes {
			d := n.Display()
			rows = append(rows, row{
				depth: depth,
				kind:  n.Kind().String(),
				name:  n.Name(),
				meta:  n.Meta(),
				lines: n.Lines(),
				start: d.Start(),
				end:   d.End(),
			})
			walk(n.Children(), depth+1)
		}
	}
	walk(t.Children(), 1)
	return rows
}

// measure computes column widths across every row.
func measure(rows []row) (layout, error) {
	if len(rows) == 0 {
		return layout{}, fault.Internal{Where: "render.measure", Detail: "no rows to measure"}
	}

	l := layout{countW: 1, rangeW: 1}
	for _, r := range rows {
		if r.depth < 0 {
			return layout{}, fault.Internal{Where: "render.measure", Detail: fmt.Sprintf("row %q has negative depth", r.name)}
		}
		if r.depth > 0 {
			// The name column starts past the deepest indent plus its kind word.
			start := r.depth*indent + width(r.kind) + 1
			l.nameCol = maxInt(l.nameCol, start)
			if l.minName == 0 || start < l.minName {
				l.minName = start
			}
			l.maxDepth = maxInt(l.maxDepth, r.depth)
		}
		for len(l.metaCols) < len(r.meta) {
			l.metaCols = append(l.metaCols, 0)
		}
		for i, m := range r.meta {
			l.metaCols[i] = maxInt(l.metaCols[i], width(m))
		}
		l.countW = maxInt(l.countW, digits(r.lines))
		l.rangeW = maxInt(l.rangeW, digits(r.start), digits(r.end))
	}

	for _, r := range rows {
		// The file row is written flush left, so it needs no indent allowance.
		start := l.nameCol
		if r.depth == 0 {
			start = 0
		}
		l.nameEnd = maxInt(l.nameEnd, start+width(r.name)+2)
	}

	// Every slot but the last is followed by a single separating space.
	for i, w := range l.metaCols {
		l.metaSpan += w
		if i < len(l.metaCols)-1 {
			l.metaSpan++
		}
	}

	if err := fault.Check(l.nameEnd > l.nameCol, "render.measure", "name column ends at %d but starts at %d", l.nameEnd, l.nameCol); err != nil {
		return layout{}, err
	}
	return l, nil
}

// draw emits one row.
//
// Padding is always computed from the plain text; colour is added to each
// segment as it is written. That ordering is what keeps a coloured table
// aligned, since escape sequences occupy no columns.
func draw(r row, l layout, p style.Palette) (string, error) {
	var b strings.Builder

	// The file row carries its own brackets and sits flush left; annotation rows
	// are indented one "|  " per level, then their kind word, then the name at a
	// column shared by every row.
	start := 0
	if r.depth > 0 {
		b.WriteString(p.Paint(strings.Repeat("|  ", r.depth), style.Quiet))
		b.WriteString(p.Paint(r.kind, inkFor(r.kind)))
		if err := pad(&b, l.nameCol-(r.depth*indent+width(r.kind))); err != nil {
			return "", err
		}
		start = l.nameCol
	}
	b.WriteString(p.Paint(r.name, style.Name))
	if err := pad(&b, l.nameEnd-start-width(r.name)); err != nil {
		return "", err
	}

	b.WriteString(p.Paint("[", style.Quiet))
	used := 0
	for i, w := range l.metaCols {
		if i > 0 {
			b.WriteByte(' ')
			used++
		}
		var item string
		if i < len(r.meta) {
			item = r.meta[i]
		}
		b.WriteString(p.Paint(item, style.Meta))
		if err := pad(&b, w-width(item)); err != nil {
			return "", err
		}
		used += w
	}
	if err := fault.Check(used == l.metaSpan, "render.draw", "metadata row is %d wide, layout says %d", used, l.metaSpan); err != nil {
		return "", err
	}
	b.WriteString(p.Paint("]", style.Quiet))

	word := "lines"
	if r.lines == 1 {
		word = "line "
	}
	b.WriteByte(' ')
	b.WriteString(p.Paint(fmt.Sprintf("%*d %s", l.countW, r.lines, word), style.Quiet))
	b.WriteByte(' ')
	b.WriteString(p.Paint(fmt.Sprintf("<%*d:%*d>", l.rangeW, r.start, l.rangeW, r.end), style.Span))
	b.WriteByte(' ')
	b.WriteString(p.Paint("|", style.Quiet))

	return b.String(), nil
}

// rule draws a horizontal separator.
//
// The two rules differ, as they do in the documented example. The top rule
// marks where names begin for the shallowest row; the bottom rule marks each
// tree indent level. Both segments are drawn as dashes with colons punched in
// at measured offsets, so a rule can never disagree in width with the rows it
// brackets.
func rule(l layout, top bool) string {
	tree := []byte(strings.Repeat("-", maxInt(l.nameEnd-1, 0)))
	mark := func(col int) {
		if i := col - 1; i >= 0 && i < len(tree) {
			tree[i] = ':'
		}
	}
	if top {
		mark(l.minName)
	} else {
		for k := 1; k < l.maxDepth; k++ {
			mark(k * indent)
		}
	}

	meta := []byte(strings.Repeat("-", maxInt(l.metaSpan, 0)))
	offset := 0
	for i, w := range l.metaCols {
		if i > 0 {
			// A separating space sits at offset; the slot itself begins one
			// column later, and that first column carries the colon.
			offset++
			if offset < len(meta) {
				meta[offset] = ':'
			}
		}
		offset += w
	}

	var b strings.Builder
	b.WriteByte('|')
	b.Write(tree)
	b.WriteByte('|')
	b.Write(meta)
	b.WriteByte('|')
	b.WriteString(strings.Repeat("-", countWidth(l)))
	b.WriteByte('|')
	return b.String()
}

// countWidth is the width of the trailing count-and-range segment.
func countWidth(l layout) int {
	// " N lines <S:E> " plus the closing bar's own column.
	return 1 + l.countW + 1 + 5 + 1 + 1 + l.rangeW + 1 + l.rangeW + 1 + 1
}

func pad(b *strings.Builder, n int) error {
	if n < 0 {
		return fault.Internal{Where: "render.pad", Detail: fmt.Sprintf("negative padding %d", n)}
	}
	b.WriteString(strings.Repeat(" ", n))
	return nil
}

// width counts display columns. Anno's names come from source code, so counting
// runes is the right approximation; it is exact for every ASCII identifier.
func width(s string) int { return len([]rune(s)) }

func digits(n int) int {
	if n < 0 {
		return len(fmt.Sprint(n))
	}
	if n == 0 {
		return 1
	}
	d := 0
	for n > 0 {
		d++
		n /= 10
	}
	return d
}

func maxInt(vals ...int) int {
	m := vals[0]
	for _, v := range vals[1:] {
		if v > m {
			m = v
		}
	}
	return m
}
