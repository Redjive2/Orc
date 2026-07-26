// Package link turns a document's scanned links into edges anchored to
// sections.
//
// Two jobs, both pure. Attribution decides which section a link belongs to —
// the one whose heading most recently preceded it, which is the deepest section
// enclosing that line. Resolution answers a same-file reference outright, since
// a document already knows its own sections; a cross-file reference needs a path
// resolved against the filesystem, and that belongs to the command layer.
//
// A destination that addresses nothing is not an error and not an edge. Most
// links in a document are ordinary markdown, and Dock's graph is about sections.
package link

import (
	"errors"
	"fmt"

	"orc/common/fault"
	"orc/dock/internal/doc"
	"orc/dock/internal/scan"
	"orc/dock/internal/target"
)

// Root is the From of an edge declared before any section — a "see also" that
// governs the whole document rather than one part of it.
const Root = ""

// Edge is one link, from a section of one document to a target.
//
// It carries every reading of its destination rather than one, because the
// split between path and address cannot be decided without the filesystem. The
// command layer takes the first reading whose path exists; until then they are
// all equally true.
type Edge struct {
	from     string
	readings []target.Target
	label    string
	line     int
	col      int
}

// From returns the number of the section the link sits in, or Root.
func (e Edge) From() string { return e.from }

// Readings returns a copy of the destination's readings, most-path-first.
func (e Edge) Readings() []target.Target { return append([]target.Target(nil), e.readings...) }

// To returns the preferred reading: the one with the longest path, which is
// what a caller gets when that path exists.
func (e Edge) To() target.Target { return e.readings[0] }

// Label returns the link text, which is what a human already wrote to describe
// the edge. Dock invents no second labelling mechanism.
func (e Edge) Label() string { return e.label }

// Line returns the 1-indexed line the link sits on.
func (e Edge) Line() int { return e.line }

// Col returns the 1-indexed rune column of the link's opening bracket.
func (e Edge) Col() int { return e.col }

// SameFile reports whether every reading stays inside the linking document.
func (e Edge) SameFile() bool {
	for _, r := range e.readings {
		if !r.SameFile() {
			return false
		}
	}
	return true
}

// String renders the edge the way check and links report it.
func (e Edge) String() string {
	from := e.from
	if from == Root {
		from = "(root)"
	} else {
		from = "§" + from
	}
	if e.label == "" {
		return fmt.Sprintf("%s -> %s", from, e.To())
	}
	return fmt.Sprintf("%s -> %s [%s]", from, e.To(), e.label)
}

// Edges attributes every link in a scan to the section that encloses it.
//
// Destinations that address nothing are skipped. Malformed § destinations are
// collected and reported together, so a document with several broken links is
// fixed in one round trip — and every fault carries the line and column of the
// link that caused it.
func Edges(d doc.Doc, r scan.Result) ([]Edge, error) {
	sections := d.Sections()
	var (
		out      []Edge
		problems []error
	)

	for _, l := range r.Links() {
		readings, ok, err := target.Parse(l.Dest())
		if err != nil {
			problems = append(problems, fault.Parse{
				Path:   d.Path(),
				Line:   l.Line(),
				Col:    l.Col(),
				Reason: err.Error(),
			})
			continue
		}
		if !ok {
			continue
		}
		out = append(out, Edge{
			from:     enclosing(sections, l.Line()),
			readings: readings,
			label:    l.Text(),
			line:     l.Line(),
			col:      l.Col(),
		})
	}

	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	return out, nil
}

// enclosing finds the section a line belongs to: the last one whose heading is
// at or before it.
//
// "At" matters. A link in a heading's own text belongs to the section that
// heading opens, not to the one before it — otherwise a section whose title
// cites another document would attribute the citation to its predecessor.
//
// Sections are in document order, so this is a binary search rather than a scan.
func enclosing(sections []doc.Section, line int) string {
	lo, hi := 0, len(sections)
	for lo < hi {
		mid := (lo + hi) / 2
		if sections[mid].Head() <= line {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == 0 {
		return Root
	}
	return sections[lo-1].Number()
}

// Resolve answers a same-file reference against the document it appears in.
//
// It reports false for a target that names another file, which is not a failure
// — it means the caller has to resolve a path first. A same-file target that
// matches nothing is a dangling link, and that is what check reports.
func Resolve(d doc.Doc, t target.Target) (doc.Section, bool) {
	if !t.SameFile() || t.Kind() != target.Section {
		return doc.Section{}, false
	}
	if n := t.Number(); n != "" {
		return d.ByNumber(n)
	}
	return d.ByName(t.Name())
}

// SameFileDangling returns the edges whose same-file targets resolve to
// nothing. It is the narrow answer, available without a corpus; Graph.Dangling
// is the whole one.
//
// Only same-file edges are judged: a cross-file edge cannot be checked without
// reading the other document, and an Anno target cannot be checked without anno.
// Reporting either as dangling here would be a guess.
func SameFileDangling(d doc.Doc, edges []Edge) []Edge {
	var out []Edge
	for _, e := range edges {
		if !e.SameFile() || e.To().Kind() != target.Section {
			continue
		}
		if _, ok := Resolve(d, e.To()); !ok {
			out = append(out, e)
		}
	}
	return out
}
