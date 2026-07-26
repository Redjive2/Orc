// Package edit plans and applies a write to one section.
//
// Writing is the only mutating path, so it gets the most scrutiny. The sequence
// is Anno's, and the file replacement itself is common/commit's: re-read and
// re-hash so an edit computed against stale content is refused rather than
// applied over someone else's work, write a temporary file beside the original,
// flush, chmod, rename, flush the directory.
//
// What this package adds is the part that knows about documents. Two rules do
// the work, and both exist to stop a write changing a document's shape when the
// caller only asked to change its words:
//
//   - content may not carry a section heading that would create, split, or end
//     a section the caller did not name, and
//   - the section tree after the write must be identical to the tree before it.
//
// The second is a backstop for the first. If a defect in the content rules ever
// lets something through, the re-parse catches it and nothing is written.
package edit

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"strings"

	"orc/common/commit"
	"orc/common/fault"
	"orc/common/source"
	"orc/dock/internal/doc"
	"orc/dock/internal/link"
	"orc/dock/internal/scan"
)

// MaxContent bounds a single write. A section is prose; content past this is a
// document rather than a part of one.
const MaxContent = 4 << 20

// Plan is a prepared write. The zero value is not meaningful; plans are
// produced only by Prepare.
type Plan struct {
	path     string
	result   []byte
	before   [sha256.Size]byte
	replaced doc.Range
	target   string
	oldLines int
	newLines int
}

// Path returns the file the plan will replace.
func (p Plan) Path() string { return p.path }

// Result returns a copy of the bytes the file will hold.
func (p Plan) Result() []byte { return bytes.Clone(p.result) }

// Replaced returns the line range the plan overwrites.
func (p Plan) Replaced() doc.Range { return p.replaced }

// Summary describes the write in one line, for a caller that wants to say what
// it did.
func (p Plan) Summary() string {
	where := "into " + p.target
	switch {
	case p.replaced.Empty():
		return fmt.Sprintf("wrote %s %s", lines(p.newLines), where)
	default:
		return fmt.Sprintf("replaced %s with %s %s", lines(p.oldLines), lines(p.newLines), where)
	}
}

func lines(n int) string {
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
}

// Prepare plans a write of content into a section, without touching the disk.
//
// tree selects which span is replaced: a section's own prose by default, or the
// section and everything under it. read and write take the same flag and mean
// the same span by it, which is what makes them exact inverses.
func Prepare(f source.File, d doc.Doc, s doc.Section, tree bool, content string) (Plan, error) {
	if len(content) > MaxContent {
		return Plan{}, fault.Usage{Reason: fmt.Sprintf(
			"content is %d bytes, past the %d byte limit for one section", len(content), MaxContent)}
	}

	span := s.Own()
	if tree {
		span = s.Tree()
	}
	if err := checkContent(f.Path(), s, tree, content); err != nil {
		return Plan{}, err
	}

	// A span that ends at an unterminated last line is the one place a
	// terminator must not be added: the file has no final newline, and
	// inventing one would change a line the caller never addressed.
	atEOF := !span.Empty() && span.End() == f.Count() && !f.FinalNewline()

	body := normalise(f, content, atEOF)
	result, err := splice(f, span, s, body)
	if err != nil {
		return Plan{}, err
	}

	// The backstop: re-parse the result and refuse anything that moved.
	if err := verify(f.Path(), d, result); err != nil {
		return Plan{}, err
	}

	return Plan{
		path:     f.Path(),
		result:   result,
		before:   f.Sum(),
		replaced: span,
		target:   f.Path() + doc.Sigil + s.Number(),
		oldLines: span.Len(),
		newLines: countLines(body),
	}, nil
}

// checkContent refuses content that would change the document's shape.
//
// The rule differs by span, and both readings come from the same principle: a
// write may change a section's words, never the document's structure.
func checkContent(path string, s doc.Section, tree bool, content string) error {
	r := scan.Scan(content)
	for _, l := range r.Lines() {
		if l.Kind() != scan.Heading {
			continue
		}
		if !strings.HasPrefix(l.Head(), doc.Sigil) {
			// An unmarked heading is prose. It is allowed, and has to be: a
			// section's own prose may legitimately contain one, and refusing it
			// would break the round trip for any document that does.
			continue
		}

		if !tree {
			return fault.Usage{Reason: fmt.Sprintf(
				"%s:%d: content for a section's own prose may not declare a section (%q); "+
					"use --tree to replace %s%s and everything under it",
				path, l.Num(), l.Head(), doc.Sigil, s.Number())}
		}
		if l.Level() <= s.Level() {
			return fault.Usage{Reason: fmt.Sprintf(
				"%s:%d: %q is at depth %d, which would end %s%s rather than sit inside it",
				path, l.Num(), l.Head(), l.Level(), doc.Sigil, s.Number())}
		}
	}
	return nil
}

// normalise prepares content for splicing: the file's line endings when the
// file is uniform, and a terminator so the next line starts where it should.
//
// A file that already mixes endings has none to impose, and rewriting its
// terminators would change lines the caller never addressed.
//
// atEOF suppresses the terminator. A terminator exists to keep the next line
// from being swallowed onto this one, and at the end of an unterminated file
// there is no next line — so adding one would be inventing a final newline the
// file did not have, which is exactly the change the round-trip property
// forbids.
func normalise(f source.File, content string, atEOF bool) []byte {
	if content == "" {
		return nil
	}

	body := content
	if f.Uniform() {
		// One style to impose, so impose it: fold to LF and then out to the
		// file's own ending.
		body = strings.ReplaceAll(body, "\r\n", "\n")
		if f.Ending() == source.CRLF {
			body = strings.ReplaceAll(body, "\n", "\r\n")
		}
	}
	// A file that already mixes styles has none to impose, so content passes
	// through byte for byte. Folding it to LF and not folding it back — which
	// is what an earlier draft did — silently rewrote the caller's CRLFs, found
	// by FuzzWriteReadRoundTrip on a file whose two lines ended differently.

	if !atEOF && !strings.HasSuffix(body, "\n") {
		// The terminator is needed or the next line joins this one. For a mixed
		// file there is no right answer, so the dominant style is used.
		body += string(f.Ending().Bytes())
	}
	return []byte(body)
}

// splice replaces a span's bytes, or inserts where the span is empty.
func splice(f source.File, span doc.Range, s doc.Section, body []byte) ([]byte, error) {
	raw := f.Bytes()

	if span.Empty() {
		// Inserting nothing changes nothing. Without this the branch below
		// would still supply a terminator for a file that had none, inventing a
		// final newline — which FuzzWriteReadRoundTrip found on "# §1 A" with no
		// content and no trailing newline, where reading gives "" and writing it
		// back must be a no-op.
		if len(body) == 0 {
			return raw, nil
		}
		// A section with no content: insert on the line after its heading,
		// which may be one past the end of the file.
		at, err := f.InsertOffset(min(s.Head()+1, f.Count()+1))
		if err != nil {
			return nil, err
		}
		out := make([]byte, 0, len(raw)+len(body)+2)
		out = append(out, raw[:at]...)
		// A file whose last line has no terminator needs one before anything
		// can be appended after it.
		if at == len(raw) && at > 0 && !f.FinalNewline() {
			out = append(out, f.Ending().Bytes()...)
		}
		out = append(out, body...)
		out = append(out, raw[at:]...)
		return out, nil
	}

	from, to, err := f.ByteRange(span.Start(), span.End())
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(raw)-(to-from)+len(body))
	out = append(out, raw[:from]...)
	out = append(out, body...)
	out = append(out, raw[to:]...)
	return out, nil
}

// verify re-parses the result and refuses any change to the document's shape.
//
// It also re-extracts the links, because content carrying a malformed
// destination would otherwise produce a file that indexes fine and whose links
// have silently vanished.
func verify(path string, before doc.Doc, result []byte) error {
	r := scan.Scan(string(result))
	after, err := doc.Build(path, r)
	if err != nil {
		return fault.Conflict{Path: path, Reason: "the edit would leave the document unreadable: " + err.Error()}
	}

	if got, want := after.Len(), before.Len(); got != want {
		return fault.Conflict{Path: path, Reason: fmt.Sprintf(
			"the edit would change the document from %d sections to %d", want, got)}
	}
	for i, was := range before.Sections() {
		is, ok := after.At(i)
		if !ok {
			return fault.Internal{Where: "edit.verify", Detail: "section count agreed but index did not"}
		}
		if is.Number() != was.Number() || is.Name() != was.Name() || is.Depth() != was.Depth() {
			return fault.Conflict{Path: path, Reason: fmt.Sprintf(
				"the edit would change %s%s %q into %s%s %q",
				doc.Sigil, was.Number(), was.Name(), doc.Sigil, is.Number(), is.Name())}
		}
	}

	if _, err := link.Edges(after, r); err != nil {
		return fault.Conflict{Path: path, Reason: "the content carries a malformed link: " + err.Error()}
	}
	return nil
}

func countLines(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	return bytes.Count(body, []byte("\n"))
}

// Commit writes the plan to disk.
func Commit(p Plan) error { return CommitWith(p, commit.Real()) }

// CommitWith writes the plan through the given operations, which is how the
// failure paths are reached from a test: every one of them must leave the
// original file untouched and no debris behind.
func CommitWith(p Plan, ops commit.Ops) error {
	if p.path == "" {
		return fault.Internal{Where: "edit.Commit", Detail: "plan has no path"}
	}
	return commit.ReplaceWith(commit.Request{
		Path:    p.path,
		Content: p.result,
		Expect:  &p.before,
		Tag:     "dock",
		Where:   "edit.Commit",
	}, ops)
}
