// Package edit replaces an annotation's span with new content.
//
// Writing is the only way Anno modifies anything, so it is the one place that
// deserves paranoia. A write is planned in full, checked against the content
// that will result, verified against the file still on disk, and only then
// committed through a temporary file and a rename. Every failure path leaves
// the original file exactly as it was.
package edit

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"

	"orc/anno/internal/marker"
	"orc/anno/internal/target"
	"orc/anno/internal/tree"
	"orc/common/commit"
	"orc/common/fault"
	"orc/common/source"
)

// Plan is a prepared, not yet committed write.
type Plan struct {
	path      string
	result    []byte
	before    [sha256.Size]byte
	replaced  tree.Range
	newLines  int
	qualified string
}

// Path returns the file the plan applies to.
func (p Plan) Path() string { return p.path }

// Result returns a copy of the bytes the file will contain.
func (p Plan) Result() []byte { return slices.Clone(p.result) }

// Replaced returns the line range the plan overwrites.
func (p Plan) Replaced() tree.Range { return p.replaced }

// NewLines returns how many lines the replacement contributes.
func (p Plan) NewLines() int { return p.newLines }

// Qualified returns the fully qualified target the plan was built for.
func (p Plan) Qualified() string { return p.qualified }

// Summary describes the edit in one line, for the command's output.
func (p Plan) Summary() string {
	old := p.replaced.Len()
	return fmt.Sprintf("%s: replaced %s with %s at %s",
		p.path, plural(old, "line"), plural(p.newLines, "line"), rangeText(p.replaced))
}

func rangeText(r tree.Range) string {
	if r.Empty() {
		return fmt.Sprintf("line %d", r.Start())
	}
	return fmt.Sprintf("lines %d:%d", r.Start(), r.End())
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// Prepare builds a plan to replace match's span with content.
//
// It refuses content that would damage the file's structure — an unbalanced
// close, or an open marker whose kind would swallow the code that follows the
// annotation being written — and it re-parses the resulting file to confirm the
// target still resolves to the same node. Nothing touches the disk here.
func Prepare(f source.File, m target.Match, steps []target.Step, content string) (Plan, error) {
	node, err := m.Node()
	if err != nil {
		return Plan{}, err
	}

	if err := checkContent(f.Path(), node, content); err != nil {
		return Plan{}, err
	}

	body, lineCount, err := normalise(f, node, content)
	if err != nil {
		return Plan{}, err
	}

	raw := f.Bytes()
	span := node.Span()

	var from, to int
	if span.Empty() {
		// Nothing to overwrite: insert immediately after the opening marker.
		from, err = f.InsertOffset(node.MarkerLine() + 1)
		if err != nil {
			return Plan{}, err
		}
		to = from
	} else {
		from, to, err = f.ByteRange(span.Start(), span.End())
		if err != nil {
			return Plan{}, err
		}
	}
	if err := fault.Check(from <= to && to <= len(raw), "edit.Prepare",
		"splice range %d:%d does not fit %d bytes", from, to, len(raw)); err != nil {
		return Plan{}, err
	}

	result := make([]byte, 0, len(raw)-(to-from)+len(body))
	result = append(result, raw[:from]...)
	result = append(result, body...)
	result = append(result, raw[to:]...)

	if err := verify(f, result, m, steps); err != nil {
		return Plan{}, err
	}

	return Plan{
		path:      f.Path(),
		result:    result,
		before:    f.Sum(),
		replaced:  span,
		newLines:  lineCount,
		qualified: m.Qualified(),
	}, nil
}

// checkContent rejects replacements that would restructure the file rather than
// fill in the annotation.
//
// Two rules. An open marker of equal or lower rank would terminate the very
// annotation being written, silently absorbing everything after it. And a close
// marker may only close a name the content itself opened: a close naming an
// enclosing annotation would cut that annotation short at the splice point,
// which is a change to code the caller never addressed.
func checkContent(path string, node tree.Node, content string) error {
	var open []string

	for i, text := range strings.Split(content, "\n") {
		text = strings.TrimSuffix(text, "\r")
		m, ok, err := marker.Classify(path, i+1, text)
		if err != nil {
			return fmt.Errorf("replacement content is not valid: %w", err)
		}
		if !ok {
			continue
		}

		reject := func(reason string) error {
			return fault.Parse{Path: path, Line: i + 1, Col: m.Col(), Reason: reason}
		}

		switch m.Op() {
		case marker.Open, marker.Next:
			if m.Kind().Rank() <= node.Kind().Rank() {
				return reject(fmt.Sprintf(
					"replacement content opens %s %q inside %s %q; a %s would end the annotation being written",
					m.Kind(), m.Name(), node.Kind(), node.Name(), m.Kind()))
			}
			if m.Op() == marker.Open {
				open = append(open, m.Name())
			}

		case marker.Close:
			at := -1
			for j := len(open) - 1; j >= 0; j-- {
				if open[j] == m.Name() {
					at = j
					break
				}
			}
			if at < 0 {
				return reject(fmt.Sprintf(
					"replacement content closes %q, which it never opened", m.Name()))
			}
			open = open[:at]
		}
	}
	return nil
}

// normalise renders content as whole lines and reports how many it contributes.
//
// A file that terminates every line the same way has a house style, and content
// written into it adopts that style: an agent emitting "\n" into a CRLF file
// gets CRLF. A file that already mixes styles has no house style to adopt, so
// the content's own terminators are kept exactly — otherwise reading a region
// and writing it straight back would silently rewrite it.
//
// Replacing the file's last line when that line carries no terminator preserves
// the missing terminator.
func normalise(f source.File, node tree.Node, content string) ([]byte, int, error) {
	if content == "" {
		return nil, 0, nil
	}
	if i := strings.IndexByte(content, 0); i >= 0 {
		return nil, 0, fault.Usage{Reason: fmt.Sprintf("replacement content contains a NUL byte at offset %d", i)}
	}

	term := f.Ending().Bytes()

	var body []byte
	var count int

	// final is the length of the terminator this function put at the end of the
	// body. Tracking it explicitly is what keeps a trailing carriage return that
	// is content — not a terminator — from being mistaken for one and dropped.
	var final int

	if f.Uniform() {
		flat := strings.ReplaceAll(content, "\r\n", "\n")
		flat = strings.TrimSuffix(flat, "\n")
		lines := strings.Split(flat, "\n")

		var b bytes.Buffer
		for _, line := range lines {
			b.WriteString(line)
			b.Write(term)
		}
		body, count, final = b.Bytes(), len(lines), len(term)
	} else {
		body = []byte(content)
		switch {
		case bytes.HasSuffix(body, []byte("\r\n")):
			final = 2
		case bytes.HasSuffix(body, []byte("\n")):
			final = 1
		default:
			body = append(slices.Clone(body), term...)
			final = len(term)
		}
		count = bytes.Count(body, []byte("\n"))
	}

	span := node.Span()
	if atEOF := !span.Empty() && span.End() == f.Count(); atEOF && !f.FinalNewline() {
		if err := fault.Check(final <= len(body), "edit.normalise",
			"terminator of %d bytes does not fit a %d byte body", final, len(body)); err != nil {
			return nil, 0, err
		}
		body = body[:len(body)-final]
	}
	return slices.Clone(body), count, nil
}

// verify re-parses the prospective file and confirms the target still resolves
// to one node at the same qualified address. This is what makes a bad splice
// impossible to commit: if the edit disturbed the structure, resolution changes
// and the write is abandoned.
func verify(f source.File, result []byte, m target.Match, steps []target.Step) error {
	after, err := source.Parse(f.Path(), result)
	if err != nil {
		return fmt.Errorf("the edit would make %s unreadable: %w", f.Path(), err)
	}
	t, err := tree.Build(after)
	if err != nil {
		return fmt.Errorf("the edit would leave %s with broken annotations: %w", f.Path(), err)
	}
	matches, err := target.Resolve(t, steps)
	if err != nil {
		return err
	}
	switch len(matches) {
	case 1:
		if got := matches[0].Qualified(); got != m.Qualified() {
			return fault.Conflict{Path: f.Path(), Reason: fmt.Sprintf(
				"the edit would move the annotation from %s to %s, so it was abandoned", m.Qualified(), got)}
		}
		return nil
	case 0:
		return fault.Conflict{Path: f.Path(), Reason: fmt.Sprintf(
			"the edit would remove %s, so it was abandoned", m.Qualified())}
	default:
		return fault.Conflict{Path: f.Path(), Reason: fmt.Sprintf(
			"the edit would make %s ambiguous (%d matches), so it was abandoned", m.Qualified(), len(matches))}
	}
}

// Commit writes the plan to disk.
//
// The sequence — re-read, re-hash, write a temporary file beside the original,
// flush, chmod, rename, flush the directory — lives in common/commit, because
// Anno, Dock, and Macmuffin all need exactly it and none of them should own a
// second copy. What stays here is the part that knows about annotations: the
// plan that decided which bytes to replace.
func Commit(p Plan) error { return commitWith(p, commit.Real()) }

func commitWith(p Plan, fs commit.Ops) error {
	return commit.ReplaceWith(commit.Request{
		Path:    p.path,
		Content: p.result,
		Expect:  &p.before,
		Tag:     "anno",
		Where:   "edit.Commit",
	}, fs)
}
