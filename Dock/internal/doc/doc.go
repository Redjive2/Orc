// Package doc builds a document's section tree from its scanned lines.
//
// A section is a heading that carries a § number — "## §1.2 Sections". Three
// rules make the structure self-checking, and all three are enforced here:
//
//   - depth: the number of #s equals the number of dot-separated components,
//   - parent: §1.2.1 appears under an open §1.2,
//   - sequence: siblings run 1, 2, 3 … in order, with no gaps and no repeats.
//
// The structure is stated twice, in the heading level and in the number, and
// Dock refuses to guess when the two disagree. What that buys is addressing:
// because the number encodes the whole path and names are unique per file,
// §1.2.1 and §'Numbering' each name one section outright, and none of Anno's
// chain matching or ambiguity reporting is needed.
//
// A heading without a § is not a section. It is ordinary prose inside whatever
// section encloses it, which is what makes marking up a document incremental
// and per-heading, and what makes a document with no §s invisible to Dock.
package doc

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"orc/common/fault"
	"orc/dock/internal/scan"
)

// Sigil introduces a section number in a heading. It is the section sign, and
// it is the whole of Dock's syntax.
const Sigil = "§"

// Limits. A document is read whole, so these bound what a malformed or
// generated file can turn into rather than what an author would ever write.
const (
	// MaxDepth is the deepest section number, and matches the six ATX heading
	// levels markdown has. A seventh # is not a heading, so a seventh
	// component could never satisfy the depth rule.
	MaxDepth = 6
	// MaxComponent bounds one component of a number, so a heading cannot
	// declare itself the ten-thousandth sibling of anything.
	MaxComponent = 9999
	// MaxNameLen bounds a section name.
	MaxNameLen = 200
)

// Range is a 1-indexed inclusive span of lines. The zero value is empty, which
// is the honest representation of a section with no content under it.
type Range struct {
	start int
	end   int
}

// NewRange builds a range. A non-positive start, or a start past the end, is
// the empty range: that is the honest representation of "no lines", and it
// needs no error to say so.
func NewRange(start, end int) Range { return newRange(start, end) }

// Start returns the first line, or 0 when the range is empty.
func (r Range) Start() int { return r.start }

// End returns the last line, or 0 when the range is empty.
func (r Range) End() int { return r.end }

// Empty reports whether the range covers no lines.
func (r Range) Empty() bool { return r.start == 0 || r.end < r.start }

// Len returns the number of lines covered.
func (r Range) Len() int {
	if r.Empty() {
		return 0
	}
	return r.end - r.start + 1
}

// String renders the range the way the index does.
func (r Range) String() string {
	if r.Empty() {
		return "<empty>"
	}
	return fmt.Sprintf("<%d:%d>", r.start, r.end)
}

// Section is one numbered heading and the lines beneath it. The zero value is
// not meaningful; sections are produced only by Build.
type Section struct {
	number []int
	name   string
	norm   string
	level  int
	head   int
	own    Range
	tree   Range
	parent int
	kids   []int
}

// Number renders the section's number, as it appears after the §.
func (s Section) Number() string { return join(s.number) }

// Parts returns a copy of the number's components.
func (s Section) Parts() []int { return append([]int(nil), s.number...) }

// Depth returns how deep the section sits, which is both its number's component
// count and its heading's level.
func (s Section) Depth() int { return len(s.number) }

// Name returns the heading text after the number, verbatim.
func (s Section) Name() string { return s.name }

// Level returns the heading level, 1 to 6.
func (s Section) Level() int { return s.level }

// Head returns the 1-indexed line of the section's heading. It is never part of
// any span: the heading is structure, not content, and Dock never writes it.
func (s Section) Head() int { return s.head }

// Own returns the section's own prose — the lines after its heading, up to the
// first section heading of any depth. This is what read and write address by
// default, because asking for §1 in a chapter-sized document should not return
// the chapter.
func (s Section) Own() Range { return s.own }

// Tree returns the section and every subsection under it — the lines after its
// heading up to the next section heading of depth at or above its own. This is
// what --tree addresses, and what the index displays.
func (s Section) Tree() Range { return s.tree }

// Doc is a document's frozen section tree.
type Doc struct {
	path     string
	lines    int
	sections []Section
	byNumber map[string]int
	byName   map[string]int
}

// Path returns the document's path, as given to Build.
func (d Doc) Path() string { return d.path }

// Lines returns the document's total line count.
func (d Doc) Lines() int { return d.lines }

// Len returns how many sections the document declares.
func (d Doc) Len() int { return len(d.sections) }

// Sections returns a copy of the sections in document order.
func (d Doc) Sections() []Section { return append([]Section(nil), d.sections...) }

// At returns the i'th section in document order.
func (d Doc) At(i int) (Section, bool) {
	if i < 0 || i >= len(d.sections) {
		return Section{}, false
	}
	return d.sections[i], true
}

// ByNumber finds a section by its number, written with or without the §.
func (d Doc) ByNumber(number string) (Section, bool) {
	i, ok := d.byNumber[strings.TrimPrefix(strings.TrimSpace(number), Sigil)]
	if !ok {
		return Section{}, false
	}
	return d.sections[i], true
}

// ByName finds a section by name, matched case-insensitively with internal
// whitespace collapsed — so a caller need not reproduce a heading's spacing
// exactly to address it.
func (d Doc) ByName(name string) (Section, bool) {
	i, ok := d.byName[normalise(name)]
	if !ok {
		return Section{}, false
	}
	return d.sections[i], true
}

// Children returns copies of a section's immediate subsections.
func (d Doc) Children(s Section) []Section {
	out := make([]Section, 0, len(s.kids))
	for _, i := range s.kids {
		out = append(out, d.sections[i])
	}
	return out
}

// Content narrows a range to its non-blank extent, which is what the index
// counts. A section whose content is all blank lines reports as empty rather
// than as a run of nothing.
func (d Doc) Content(lines []scan.Line, r Range) Range {
	if r.Empty() {
		return Range{}
	}
	start, end := r.start, r.end
	for start <= end && blank(lines, start) {
		start++
	}
	for end >= start && blank(lines, end) {
		end--
	}
	if start > end {
		return Range{}
	}
	return Range{start: start, end: end}
}

func blank(lines []scan.Line, n int) bool {
	if n < 1 || n > len(lines) {
		return true
	}
	return strings.TrimSpace(lines[n-1].Text()) == ""
}

// Build assembles the section tree, reporting every structural fault it finds
// rather than the first — a document with four numbering mistakes is fixed in
// one round trip.
func Build(path string, r scan.Result) (Doc, error) {
	lines := r.Lines()
	d := Doc{
		path:     path,
		lines:    len(lines),
		byNumber: map[string]int{},
		byName:   map[string]int{},
	}

	var (
		problems []error
		open     []int   // indices of the currently open ancestors, by depth
		topKids  []int   // the document root's children
		kids     [][]int // children per section, parallel to d.sections
	)

	for _, line := range lines {
		if line.Kind() != scan.Heading {
			continue
		}
		number, name, ok, err := parseHead(path, line)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if !ok {
			continue // an unmarked heading is prose
		}

		depth := len(number)
		if depth != line.Level() {
			problems = append(problems, fault.Parse{
				Path: path, Line: line.Num(),
				Reason: fmt.Sprintf("§%s has %d components under %d heading levels; the two must match",
					join(number), depth, line.Level()),
			})
			continue
		}

		// Parent and sequence are one check: the number's last component must
		// be exactly the next index its parent expects, and its prefix must be
		// the ancestor currently open at that depth.
		if depth > len(open)+1 {
			problems = append(problems, fault.Parse{
				Path: path, Line: line.Num(),
				Reason: fmt.Sprintf("§%s has no open parent; §%s must appear first",
					join(number), join(number[:depth-1])),
			})
			continue
		}
		open = open[:depth-1]

		// Indices rather than pointers: d.sections grows below, and a pointer
		// into it would be left addressing the old backing array.
		parentIdx := -1
		siblings := topKids
		if depth > 1 {
			parentIdx = open[depth-2]
			if got, want := join(number[:depth-1]), d.sections[parentIdx].Number(); got != want {
				problems = append(problems, fault.Parse{
					Path: path, Line: line.Num(),
					Reason: fmt.Sprintf("§%s sits under §%s, so it should be numbered §%s…",
						got, want, want),
				})
				continue
			}
			siblings = kids[parentIdx]
		}
		if got, want := number[depth-1], len(siblings)+1; got != want {
			problems = append(problems, fault.Parse{
				Path: path, Line: line.Num(),
				Reason: fmt.Sprintf("§%s is out of sequence; expected §%s",
					join(number), join(append(append([]int(nil), number[:depth-1]...), want))),
			})
			continue
		}

		norm := normalise(name)
		if prev, dup := d.byName[norm]; dup {
			problems = append(problems, fault.Parse{
				Path: path, Line: line.Num(),
				Reason: fmt.Sprintf("a section named %q is already declared on line %d; names must be unique so one can address a section",
					name, d.sections[prev].head),
			})
			continue
		}

		idx := len(d.sections)
		d.sections = append(d.sections, Section{
			number: number,
			name:   name,
			norm:   norm,
			level:  line.Level(),
			head:   line.Num(),
			parent: parentIdx,
		})
		kids = append(kids, nil)
		if parentIdx < 0 {
			topKids = append(topKids, idx)
		} else {
			kids[parentIdx] = append(kids[parentIdx], idx)
		}
		d.byNumber[join(number)] = idx
		d.byName[norm] = idx
		open = append(open, idx)
	}

	if len(problems) > 0 {
		return Doc{}, errors.Join(problems...)
	}

	for i := range d.sections {
		d.sections[i].kids = kids[i]
	}

	d.spans()
	if err := d.validate(); err != nil {
		return Doc{}, err
	}
	return d, nil
}

// spans computes both ranges for every section in one backward pass.
//
// The own span ends at the next section heading in document order, whatever its
// depth; the tree span ends at the next heading of depth at or above this one.
// Both start on the line after the heading, because the heading is structure.
func (d *Doc) spans() {
	for i := range d.sections {
		s := &d.sections[i]
		start := s.head + 1

		ownEnd := d.lines
		if i+1 < len(d.sections) {
			ownEnd = d.sections[i+1].head - 1
		}

		treeEnd := d.lines
		for j := i + 1; j < len(d.sections); j++ {
			if d.sections[j].Depth() <= s.Depth() {
				treeEnd = d.sections[j].head - 1
				break
			}
		}

		s.own = newRange(start, ownEnd)
		s.tree = newRange(start, treeEnd)
	}
}

func newRange(start, end int) Range {
	if start > end || start < 1 {
		return Range{}
	}
	return Range{start: start, end: end}
}

// validate re-checks the invariants Build is supposed to have established. They
// cannot fail for a document Build accepted, which is exactly why they are
// worth asserting: reaching one means the builder has a hole in it.
func (d Doc) validate() error {
	const where = "doc.Build"
	for i, s := range d.sections {
		if s.Depth() != s.level {
			return fault.Internal{Where: where, Detail: fmt.Sprintf("§%s has depth %d at level %d", s.Number(), s.Depth(), s.level)}
		}
		if s.head < 1 || s.head > d.lines {
			return fault.Internal{Where: where, Detail: fmt.Sprintf("§%s heads line %d of %d", s.Number(), s.head, d.lines)}
		}
		// The own span can never reach past the tree span: own stops at the
		// next section of any depth, tree at the next one no deeper.
		if !s.own.Empty() && !s.tree.Empty() && s.own.End() > s.tree.End() {
			return fault.Internal{Where: where, Detail: fmt.Sprintf("§%s own span %s outruns tree span %s", s.Number(), s.own, s.tree)}
		}
		if i > 0 && s.head <= d.sections[i-1].head {
			return fault.Internal{Where: where, Detail: "sections are not in document order"}
		}
	}
	return nil
}

// parseHead reads a section number and name out of a heading.
//
// A heading with no § is not a section and is not an error. A heading that
// starts with § means to be one, so anything malformed after it is reported
// rather than ignored — silently skipping it would leave an author wondering
// why their section cannot be addressed.
func parseHead(path string, line scan.Line) (number []int, name string, ok bool, err error) {
	text := line.Head()
	if !strings.HasPrefix(text, Sigil) {
		return nil, "", false, nil
	}
	fail := func(format string, args ...any) ([]int, string, bool, error) {
		return nil, "", false, fault.Parse{Path: path, Line: line.Num(), Reason: fmt.Sprintf(format, args...)}
	}

	rest := strings.TrimPrefix(text, Sigil)
	i := strings.IndexFunc(rest, unicode.IsSpace)
	digits, name := rest, ""
	if i >= 0 {
		digits, name = rest[:i], strings.TrimSpace(rest[i:])
	}
	if digits == "" {
		return fail("a § heading needs a number, as in \"%s1.2 Name\"", Sigil)
	}

	for _, part := range strings.Split(digits, ".") {
		switch {
		case part == "":
			return fail("§%s has an empty component", digits)
		case len(part) > 1 && part[0] == '0':
			return fail("§%s has a leading zero, so it is not the canonical form of its number", digits)
		}
		n, convErr := strconv.Atoi(part)
		if convErr != nil {
			return fail("§%s is not a number", digits)
		}
		if n < 1 {
			return fail("§%s numbers sections from 1", digits)
		}
		if n > MaxComponent {
			return fail("§%s exceeds the maximum component %d", digits, MaxComponent)
		}
		number = append(number, n)
	}
	if len(number) > MaxDepth {
		return fail("§%s is %d deep, past the maximum of %d", digits, len(number), MaxDepth)
	}
	if name == "" {
		return fail("§%s has no name; a section needs one so it can be addressed by name", join(number))
	}
	if len(name) > MaxNameLen {
		return fail("§%s has a name of %d bytes, past the maximum of %d", join(number), len(name), MaxNameLen)
	}
	return number, name, true, nil
}

// join renders a number without its §.
func join(parts []int) string {
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = strconv.Itoa(p)
	}
	return strings.Join(out, ".")
}

// normalise folds a name to the form names are compared in: case-insensitive,
// with runs of whitespace collapsed to one space.
func normalise(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}
