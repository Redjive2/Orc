// Package tree turns a file's markers into an immutable annotation tree.
//
// Construction is the one genuinely stateful step in Anno — a stack machine
// over the marker stream — so it is confined to Build, which returns frozen
// values. Nothing exported from this package can be modified after it is
// returned, and every tree is checked against its own invariants before Build
// hands it back.
package tree

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"orc/anno/internal/marker"
	"orc/common/fault"
	"orc/common/source"
)

// Range is an inclusive, 1-indexed line range. A range with End < Start is
// empty, which is how an annotation that covers no lines is represented.
type Range struct {
	start int
	end   int
}

// NewRange builds a range, rejecting non-positive bounds. An empty range is
// spelled NewRange(n, n-1).
func NewRange(start, end int) (Range, error) {
	if start < 1 {
		return Range{}, fault.Internal{Where: "tree.NewRange", Detail: fmt.Sprintf("start %d is not positive", start)}
	}
	if end < start-1 {
		return Range{}, fault.Internal{Where: "tree.NewRange", Detail: fmt.Sprintf("end %d precedes start %d by more than one", end, start)}
	}
	return Range{start: start, end: end}, nil
}

// Start returns the first line of the range.
func (r Range) Start() int { return r.start }

// End returns the last line of the range.
func (r Range) End() int { return r.end }

// Empty reports whether the range covers no lines.
func (r Range) Empty() bool { return r.end < r.start }

// Len returns the number of lines the range spans.
func (r Range) Len() int {
	if r.Empty() {
		return 0
	}
	return r.end - r.start + 1
}

// Contains reports whether other lies entirely within r. An empty range is
// contained by every range.
func (r Range) Contains(other Range) bool {
	if other.Empty() {
		return true
	}
	if r.Empty() {
		return false
	}
	return other.start >= r.start && other.end <= r.end
}

// String renders the range as it appears in an index.
func (r Range) String() string { return fmt.Sprintf("%d:%d", r.start, r.end) }

// Node is one annotation. Nodes are immutable: every accessor returns a copy of
// anything a caller could otherwise modify.
type Node struct {
	kind       marker.Kind
	name       string
	meta       []string
	markerLine int
	span       Range
	content    Range
	lines      int
	children   []Node
}

// Kind returns the annotation's kind.
func (n Node) Kind() marker.Kind { return n.kind }

// Name returns the annotation's name.
func (n Node) Name() string { return n.name }

// Meta returns a copy of the annotation's metadata list.
func (n Node) Meta() []string { return slices.Clone(n.meta) }

// MarkerLine returns the line the opening marker sits on.
func (n Node) MarkerLine() int { return n.markerLine }

// Span returns the raw extent of the annotation: every line after its opening
// marker, up to but excluding its terminator. This is what `anno read` emits.
func (n Node) Span() Range { return n.span }

// Content returns the span trimmed of leading and trailing lines that are blank
// or are themselves markers. This is what `anno index` displays.
func (n Node) Content() Range { return n.content }

// Lines returns the number of content lines: the content range's length less
// the marker lines inside it. Interior blank lines count.
func (n Node) Lines() int { return n.lines }

// Children returns a copy of the node's direct children, in file order.
func (n Node) Children() []Node { return slices.Clone(n.children) }

// Display returns the range to show for this node, which is the content range
// except for empty annotations, where it collapses to the marker line so an
// index never prints an inverted range.
func (n Node) Display() Range {
	if n.content.Empty() {
		return Range{start: n.markerLine, end: n.markerLine}
	}
	return n.content
}

// Tree is a file's complete annotation structure.
type Tree struct {
	path     string
	name     string
	count    int
	children []Node
}

// Path returns the path of the file the tree describes.
func (t Tree) Path() string { return t.path }

// Name returns the base name of that file.
func (t Tree) Name() string { return t.name }

// Count returns the file's total line count.
func (t Tree) Count() int { return t.count }

// Children returns a copy of the top-level annotations.
func (t Tree) Children() []Node { return slices.Clone(t.children) }

// Empty reports whether the file carries no annotations at all.
func (t Tree) Empty() bool { return len(t.children) == 0 }

// builder is the mutable form used during construction. It never escapes Build.
type builder struct {
	kind       marker.Kind
	name       string
	meta       []string
	markerLine int
	spanStart  int
	spanEnd    int
	rank       int
	children   []*builder
}

// Build parses every line of f and assembles the annotation tree.
//
// All parse faults are collected rather than reported one at a time, so a caller
// fixing a file's annotations learns everything wrong with it in one pass.
func Build(f source.File) (Tree, error) {
	lines := f.Lines()

	markers := make([]marker.Marker, 0, len(lines))
	isMarker := make(map[int]bool, len(lines))
	var faults []error
	for i, text := range lines {
		m, ok, err := marker.Classify(f.Path(), i+1, text)
		if err != nil {
			faults = append(faults, err)
			continue
		}
		if ok {
			markers = append(markers, m)
			isMarker[m.Line()] = true
		}
	}

	root := &builder{rank: -1, spanStart: 1, spanEnd: len(lines)}
	stack := []*builder{root}

	// closeDown pops and closes every open node the predicate accepts,
	// terminating each at endLine.
	closeDown := func(accept func(*builder) bool, endLine int) {
		for len(stack) > 1 {
			top := stack[len(stack)-1]
			if !accept(top) {
				return
			}
			top.spanEnd = endLine
			stack = stack[:len(stack)-1]
		}
	}

	for _, m := range markers {
		switch m.Op() {
		case marker.Open, marker.Next:
			rank := m.Kind().Rank()
			closeDown(func(b *builder) bool { return b.rank >= rank }, m.Line()-1)

			if m.Op() == marker.Next {
				// The claimed line must exist and must hold real content. A
				// marker on that line would both be annotated as code and, if it
				// terminated the enclosing annotation, end that annotation before
				// the line its own child claims.
				if m.Line() >= len(lines) {
					faults = append(faults, fault.Parse{
						Path: f.Path(), Line: m.Line(), Col: m.Col(),
						Reason: "@:; annotates the next line, but this is the last line of the file",
					})
					continue
				}
				if isMarker[m.Line()+1] {
					faults = append(faults, fault.Parse{
						Path: f.Path(), Line: m.Line(), Col: m.Col(),
						Reason: "@:; annotates the next line, but that line is itself an annotation marker",
					})
					continue
				}
			}

			node := &builder{
				kind:       m.Kind(),
				name:       m.Name(),
				meta:       m.Meta(),
				markerLine: m.Line(),
				spanStart:  m.Line() + 1,
				rank:       rank,
			}
			parent := stack[len(stack)-1]
			parent.children = append(parent.children, node)

			if m.Op() == marker.Next {
				node.spanEnd = m.Line() + 1
			} else {
				node.spanEnd = 0 // still open
				stack = append(stack, node)
			}

		case marker.Close:
			at := -1
			for i := len(stack) - 1; i >= 1; i-- {
				if stack[i].name == m.Name() {
					at = i
					break
				}
			}
			if at < 0 {
				open := make([]string, 0, len(stack)-1)
				for _, b := range stack[1:] {
					open = append(open, fmt.Sprintf("%s %s", b.kind, b.name))
				}
				faults = append(faults, fault.Unbalanced{Path: f.Path(), Line: m.Line(), Name: m.Name(), Open: open})
				continue
			}
			depth := at
			closeDown(func(*builder) bool { return len(stack) > depth }, m.Line()-1)

		default:
			faults = append(faults, fault.Internal{Where: "tree.Build", Detail: fmt.Sprintf("unhandled marker op %v at line %d", m.Op(), m.Line())})
		}
	}

	closeDown(func(*builder) bool { return true }, len(lines))

	if len(faults) > 0 {
		return Tree{}, errors.Join(faults...)
	}

	children, err := freezeAll(root.children, lines, isMarker)
	if err != nil {
		return Tree{}, err
	}

	t := Tree{path: f.Path(), name: f.Name(), count: len(lines), children: children}
	if err := t.validate(); err != nil {
		return Tree{}, err
	}
	return t, nil
}

// freezeAll converts builders into immutable nodes, computing content ranges.
func freezeAll(bs []*builder, lines []string, isMarker map[int]bool) ([]Node, error) {
	out := make([]Node, 0, len(bs))
	for _, b := range bs {
		n, err := freeze(b, lines, isMarker)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func freeze(b *builder, lines []string, isMarker map[int]bool) (Node, error) {
	if b == nil {
		return Node{}, fault.Internal{Where: "tree.freeze", Detail: "nil builder"}
	}
	if b.spanEnd == 0 {
		return Node{}, fault.Internal{Where: "tree.freeze", Detail: fmt.Sprintf("annotation %q at line %d was never closed", b.name, b.markerLine)}
	}
	span, err := NewRange(b.spanStart, max(b.spanEnd, b.spanStart-1))
	if err != nil {
		return Node{}, err
	}

	content, count, err := measure(span, lines, isMarker)
	if err != nil {
		return Node{}, err
	}

	children, err := freezeAll(b.children, lines, isMarker)
	if err != nil {
		return Node{}, err
	}

	return Node{
		kind:       b.kind,
		name:       b.name,
		meta:       slices.Clone(b.meta),
		markerLine: b.markerLine,
		span:       span,
		content:    content,
		lines:      count,
		children:   children,
	}, nil
}

// measure trims a span to its content range and counts the content lines. A
// line is skippable at the edges when it is blank or is itself a marker; marker
// lines in the interior are excluded from the count but do not end the range.
func measure(span Range, lines []string, isMarker map[int]bool) (Range, int, error) {
	if span.Empty() {
		empty, err := NewRange(span.Start(), span.Start()-1)
		return empty, 0, err
	}
	if span.End() > len(lines) {
		return Range{}, 0, fault.Internal{Where: "tree.measure", Detail: fmt.Sprintf("span %s exceeds the file's %d lines", span, len(lines))}
	}

	skippable := func(n int) bool { return isMarker[n] || strings.TrimSpace(lines[n-1]) == "" }

	start, end := span.Start(), span.End()
	for start <= end && skippable(start) {
		start++
	}
	for end >= start && skippable(end) {
		end--
	}
	if start > end {
		empty, err := NewRange(span.Start(), span.Start()-1)
		return empty, 0, err
	}

	content, err := NewRange(start, end)
	if err != nil {
		return Range{}, 0, err
	}
	count := content.Len()
	for n := start; n <= end; n++ {
		if isMarker[n] {
			count--
		}
	}
	if err := fault.Check(count >= 0, "tree.measure", "negative line count %d for range %s", count, content); err != nil {
		return Range{}, 0, err
	}
	return content, count, nil
}

// validate re-derives every structural invariant the builder is meant to
// guarantee. A violation is a defect in Build, reported rather than ignored.
func (t Tree) validate() error {
	if err := fault.Check(t.count >= 0, "tree.Tree", "negative line count %d", t.count); err != nil {
		return err
	}
	// A zero-line file yields NewRange(1, 0), the empty range, which contains
	// nothing — correct, since such a file can hold no annotations.
	file, err := NewRange(1, t.count)
	if err != nil {
		return err
	}
	return validateNodes(t.children, file, -1, t.count)
}

func validateNodes(nodes []Node, within Range, parentRank int, fileLines int) error {
	const where = "tree.Tree"
	prevEnd := 0
	for _, n := range nodes {
		if err := fault.Check(n.kind.Valid(), where, "node %q has invalid kind %d", n.name, int(n.kind)); err != nil {
			return err
		}
		if err := fault.Check(n.name != "", where, "node at line %d has an empty name", n.markerLine); err != nil {
			return err
		}
		if err := fault.Check(n.kind.Rank() > parentRank, where,
			"node %q has rank %d inside a parent of rank %d", n.name, n.kind.Rank(), parentRank); err != nil {
			return err
		}
		if err := fault.Check(n.markerLine >= 1 && n.markerLine <= fileLines, where,
			"node %q has marker line %d outside 1..%d", n.name, n.markerLine, fileLines); err != nil {
			return err
		}
		if err := fault.Check(within.Contains(n.span), where,
			"node %q spans %s outside its parent's %s", n.name, n.span, within); err != nil {
			return err
		}
		if err := fault.Check(n.span.Contains(n.content), where,
			"node %q has content %s outside its span %s", n.name, n.content, n.span); err != nil {
			return err
		}
		if err := fault.Check(n.lines >= 0 && n.lines <= n.span.Len(), where,
			"node %q reports %d lines for a span of %d", n.name, n.lines, n.span.Len()); err != nil {
			return err
		}
		if err := fault.Check(n.span.Empty() || n.span.Start() > prevEnd, where,
			"node %q starts at %d, overlapping a sibling ending at %d", n.name, n.span.Start(), prevEnd); err != nil {
			return err
		}
		if !n.span.Empty() {
			prevEnd = n.span.End()
		}
		if err := validateNodes(n.children, n.span, n.kind.Rank(), fileLines); err != nil {
			return err
		}
	}
	return nil
}
