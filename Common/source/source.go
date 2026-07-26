// Package source loads files into an immutable, line-addressed view.
//
// A File keeps the original bytes verbatim and records where each line begins
// and ends, so every projection built on it — reading a span, splicing a
// replacement — is a slice of the original rather than a reconstruction. Files
// with mixed or unusual line endings therefore round-trip exactly.
//
// That exactness is why the package is shared rather than copied. Anno reads
// spans of code and Dock reads spans of prose, and both promise that read and
// write are inverses; two implementations of "where does this line begin" would
// eventually disagree about a lone carriage return, and one of them would be
// silently corrupting files.
package source

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"unicode/utf8"

	"orc/common/fault"
)

// MaxSize is the largest file Anno will load. Annotated source files are small;
// the cap turns a pathological input into a clear error instead of an
// out-of-memory kill.
const MaxSize = 64 << 20

// Ending is a line terminator style.
type Ending int

const (
	// LF is "\n". It is the style assumed for files that have no terminator at all.
	LF Ending = iota
	// CRLF is "\r\n".
	CRLF
)

// String implements fmt.Stringer.
func (e Ending) String() string {
	switch e {
	case CRLF:
		return "CRLF"
	case LF:
		return "LF"
	default:
		return fmt.Sprintf("Ending(%d)", int(e))
	}
}

// Bytes returns the terminator's byte sequence.
func (e Ending) Bytes() []byte {
	if e == CRLF {
		return []byte("\r\n")
	}
	return []byte("\n")
}

// line records one line's extent within the raw bytes. content is the offset
// one past the last content byte; term is the offset one past the terminator,
// equal to content for a final line with no terminator.
type line struct {
	start   int
	content int
	term    int
}

// File is an immutable view of a loaded file. The zero value is not usable;
// construct one with Load or Parse.
type File struct {
	path   string
	raw    []byte
	lines  []line
	ending Ending
	sum    [sha256.Size]byte
}

// Load reads path from disk. It rejects anything that is not a regular file,
// anything larger than MaxSize, and any content that is not valid UTF-8 or that
// contains a NUL byte — the latter being Anno's binary-file test.
func Load(path string) (File, error) {
	if path == "" {
		return File{}, fault.Usage{Reason: "empty file path"}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return File{}, fault.IO{Op: "stat", Path: path, Err: err}
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		// Resolve explicitly so the mode check below describes the real target.
		info, err = os.Stat(path)
		if err != nil {
			return File{}, fault.IO{Op: "stat", Path: path, Err: err}
		}
	}
	if info.IsDir() {
		return File{}, fault.IO{Op: "read", Path: path, Err: fmt.Errorf("is a directory")}
	}
	if !info.Mode().IsRegular() {
		return File{}, fault.IO{Op: "read", Path: path, Err: fmt.Errorf("not a regular file (%s)", info.Mode().Type())}
	}
	if info.Size() > MaxSize {
		return File{}, fault.IO{Op: "read", Path: path, Err: fmt.Errorf("file is %d bytes, limit is %d", info.Size(), MaxSize)}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, fault.IO{Op: "read", Path: path, Err: err}
	}
	if len(data) > MaxSize {
		return File{}, fault.IO{Op: "read", Path: path, Err: fmt.Errorf("file grew past the %d byte limit while reading", MaxSize)}
	}
	return Parse(path, data)
}

// Parse builds a File from bytes already in hand. It performs the same content
// validation as Load and touches no filesystem, which makes it the entry point
// for tests and for re-checking a candidate write before it is committed.
func Parse(path string, data []byte) (File, error) {
	if path == "" {
		return File{}, fault.Usage{Reason: "empty file path"}
	}
	if i := bytes.IndexByte(data, 0); i >= 0 {
		return File{}, fault.Parse{Path: path, Reason: fmt.Sprintf("binary file (NUL byte at offset %d)", i)}
	}
	if !utf8.Valid(data) {
		return File{}, fault.Parse{Path: path, Reason: "file is not valid UTF-8"}
	}

	raw := slices.Clone(data)
	lines := scan(raw)

	f := File{path: path, raw: raw, lines: lines, ending: dominant(lines), sum: sha256.Sum256(raw)}
	if err := f.validate(); err != nil {
		return File{}, err
	}
	return f, nil
}

// scan splits raw into lines, recognising "\n" and "\r\n" and tolerating a final
// line with no terminator.
func scan(raw []byte) []line {
	if len(raw) == 0 {
		return nil
	}
	lines := make([]line, 0, bytes.Count(raw, []byte("\n"))+1)
	start := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\n' {
			continue
		}
		content := i
		if content > start && raw[content-1] == '\r' {
			content--
		}
		lines = append(lines, line{start: start, content: content, term: i + 1})
		start = i + 1
	}
	if start < len(raw) {
		lines = append(lines, line{start: start, content: len(raw), term: len(raw)})
	}
	return lines
}

// dominant picks the terminator style to use when writing. Ties and files with
// no terminator at all resolve to LF.
func dominant(lines []line) Ending {
	crlf := 0
	lf := 0
	for _, l := range lines {
		switch l.term - l.content {
		case 2:
			crlf++
		case 1:
			lf++
		}
	}
	if crlf > lf {
		return CRLF
	}
	return LF
}

// validate re-derives the invariants scan is expected to establish. It runs on
// every constructed File so a defect in scan surfaces as an Internal error at
// the point of construction rather than as a corrupt splice much later.
func (f File) validate() error {
	const where = "source.File"
	prev := 0
	for i, l := range f.lines {
		if err := fault.Check(l.start == prev, where, "line %d starts at %d, expected %d", i+1, l.start, prev); err != nil {
			return err
		}
		if err := fault.Check(l.start <= l.content && l.content <= l.term && l.term <= len(f.raw),
			where, "line %d has disordered offsets (%d,%d,%d) in %d bytes", i+1, l.start, l.content, l.term, len(f.raw)); err != nil {
			return err
		}
		if err := fault.Check(l.term-l.content <= 2, where, "line %d has a %d byte terminator", i+1, l.term-l.content); err != nil {
			return err
		}
		if err := fault.Check(!bytes.ContainsAny(f.raw[l.start:l.content], "\n"),
			where, "line %d content contains a newline", i+1); err != nil {
			return err
		}
		prev = l.term
	}
	return fault.Check(prev == len(f.raw), where, "lines cover %d of %d bytes", prev, len(f.raw))
}

// Path returns the path the file was loaded from.
func (f File) Path() string { return f.path }

// Name returns the file's base name.
func (f File) Name() string { return filepath.Base(f.path) }

// Count returns the number of lines.
func (f File) Count() int { return len(f.lines) }

// Ending returns the terminator style writes should use.
func (f File) Ending() Ending { return f.ending }

// Uniform reports whether every terminated line in the file ends the same way.
//
// It is what tells a write whether it may safely impose the file's terminator
// on incoming content. In a file that already mixes styles there is no house
// style to impose, and rewriting terminators would change lines the caller
// never asked about.
func (f File) Uniform() bool {
	crlf, lf := 0, 0
	for _, l := range f.lines {
		switch l.term - l.content {
		case 2:
			crlf++
		case 1:
			lf++
		}
	}
	return crlf == 0 || lf == 0
}

// Sum returns the SHA-256 of the file's bytes, used to detect concurrent
// modification between a read and the write that depends on it.
func (f File) Sum() [sha256.Size]byte { return f.sum }

// Bytes returns a copy of the raw content.
func (f File) Bytes() []byte { return slices.Clone(f.raw) }

// FinalNewline reports whether the last line is terminated.
func (f File) FinalNewline() bool {
	if len(f.lines) == 0 {
		return false
	}
	last := f.lines[len(f.lines)-1]
	return last.term > last.content
}

// Line returns the content of line n, 1-indexed, without its terminator.
func (f File) Line(n int) (string, error) {
	if n < 1 || n > len(f.lines) {
		return "", fault.Internal{Where: "source.File.Line", Detail: fmt.Sprintf("line %d out of range 1..%d in %s", n, len(f.lines), f.path)}
	}
	l := f.lines[n-1]
	return string(f.raw[l.start:l.content]), nil
}

// Lines returns copies of every line's content, in order.
func (f File) Lines() []string {
	out := make([]string, len(f.lines))
	for i, l := range f.lines {
		out[i] = string(f.raw[l.start:l.content])
	}
	return out
}

// Slice returns the raw bytes of lines start..end inclusive, terminators
// included except where the final line of the file carries none. An empty range
// (end < start) yields nil.
func (f File) Slice(start, end int) ([]byte, error) {
	if end < start {
		return nil, nil
	}
	from, to, err := f.ByteRange(start, end)
	if err != nil {
		return nil, err
	}
	return slices.Clone(f.raw[from:to]), nil
}

// ByteRange returns the half-open byte extent of lines start..end inclusive.
func (f File) ByteRange(start, end int) (int, int, error) {
	if start < 1 || end > len(f.lines) || end < start {
		return 0, 0, fault.Internal{
			Where:  "source.File.ByteRange",
			Detail: fmt.Sprintf("range %d:%d out of 1..%d in %s", start, end, len(f.lines), f.path),
		}
	}
	return f.lines[start-1].start, f.lines[end-1].term, nil
}

// InsertOffset returns the byte offset at which a new line n would begin.
// n may be one past the last line, which appends at end of file.
func (f File) InsertOffset(n int) (int, error) {
	if n < 1 || n > len(f.lines)+1 {
		return 0, fault.Internal{
			Where:  "source.File.InsertOffset",
			Detail: fmt.Sprintf("line %d out of 1..%d in %s", n, len(f.lines)+1, f.path),
		}
	}
	if n == len(f.lines)+1 {
		return len(f.raw), nil
	}
	return f.lines[n-1].start, nil
}
