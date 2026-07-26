package protocol

import (
	"regexp"
	"strings"

	"orc/cq/internal/fault"
)

// The library: the repository as something to read from a browser.
//
// Two views of the same tree. Documents are what Dock sees — markdown carrying
// § sections — and files are what is on disk, with Anno's annotations attached
// where a file has them. Both carry their whole text, because the two sides
// never meet: the server cannot ask the agent for a file, so anything the site
// can show is something a sync already carried.
//
// That is affordable and was measured before it was designed: the documentation
// is tens of kilobytes and the source is a couple of megabytes, against a
// snapshot limit of 32. Folding in the interface is about what a reader wants to
// look at, not about what would fit.

// Library size limits. A repository that exceeds them is reported as truncated
// rather than silently cut: a reader who cannot see a file must be able to tell
// that from a file that does not exist.
const (
	// MaxLibraryBytes bounds the whole library within one snapshot.
	MaxLibraryBytes = 24 << 20
	// MaxFileBytes bounds one file. A generated file of a million lines is not
	// something anybody browses, and holding it costs everything else its place.
	MaxFileBytes = 1 << 20
	// MaxLibraryFiles bounds how many files are carried at all.
	MaxLibraryFiles = 8192
	// MaxPathRunes bounds a path.
	MaxPathRunes = 1024
)

// Section is one § section of a document, as Dock reports it.
type Section struct {
	Number string `json:"number"`
	Name   string `json:"name"`
	Depth  int    `json:"depth"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Lines  int    `json:"lines"`
	Out    int    `json:"out"`
	// In is how many sections link here, absent when it was not counted.
	In *int `json:"in,omitempty"`
}

// Validate checks the section is addressable and its span is coherent.
func (s Section) Validate() error {
	if s.Number == "" {
		return fault.Field("Section", "number", "section number is empty")
	}
	if err := checkText("Section", "number", s.Number, 64, false); err != nil {
		return err
	}
	if err := checkText("Section", "name", s.Name, MaxSubjectRunes, true); err != nil {
		return err
	}
	if s.Depth < 1 {
		return fault.Field("Section", "depth", "depth %d is not a heading level", s.Depth)
	}
	// A heading with nothing under it of its own is a real section, and Dock
	// reports its span as 0:0. Refusing that failed the whole sync the first time
	// somebody created a document from the browser and had not written the body
	// yet — one empty section costing an entire mirror.
	if s.Start == 0 && s.End == 0 {
		return nil
	}
	return checkSpan("Section", s.Start, s.End)
}

// Annotation is one Anno annotation, with the ones nested inside it.
type Annotation struct {
	Kind         string       `json:"kind"`
	Name         string       `json:"name"`
	Meta         []string     `json:"meta,omitempty"`
	Start        int          `json:"start"`
	End          int          `json:"end"`
	Lines        int          `json:"lines"`
	ContentStart int          `json:"content_start"`
	ContentEnd   int          `json:"content_end"`
	Children     []Annotation `json:"children,omitempty"`
}

// Validate checks the annotation and everything under it.
//
// Depth is bounded because the structure is recursive and arrives over a wire:
// without a limit a hostile or damaged snapshot could nest deeply enough to
// exhaust the stack while being validated.
func (a Annotation) Validate() error { return a.validate(0) }

// MaxAnnotationDepth is far above Anno's own three ranks, so it bounds damage
// without ever bounding a real file.
const MaxAnnotationDepth = 32

func (a Annotation) validate(depth int) error {
	if depth > MaxAnnotationDepth {
		return fault.Field("Annotation", "children", "nested more than %d deep", MaxAnnotationDepth)
	}
	switch a.Kind {
	case "section", "symbol", "part":
	default:
		return fault.Field("Annotation", "kind", "unknown kind %q", a.Kind)
	}
	if a.Name == "" {
		return fault.Field("Annotation", "name", "annotation name is empty")
	}
	if err := checkText("Annotation", "name", a.Name, MaxNameRunes, false); err != nil {
		return err
	}
	for _, m := range a.Meta {
		if err := checkText("Annotation", "meta", m, MaxSubjectRunes, true); err != nil {
			return err
		}
	}
	if err := checkSpan("Annotation", a.Start, a.End); err != nil {
		return err
	}
	for _, child := range a.Children {
		if err := child.validate(depth + 1); err != nil {
			return err
		}
	}
	return nil
}

// File is one file of the repository, with its text.
type File struct {
	Path  string `json:"path"`
	Lines int    `json:"lines"`
	Bytes int    `json:"bytes"`
	// Text is the file's contents, empty when it was too large to carry. Bytes
	// still reports its real size, so the interface can say what it is missing
	// rather than showing an empty file.
	Text string `json:"text,omitempty"`
	// Sections are Dock's view, present on documents.
	Sections []Section `json:"sections,omitempty"`
	// Annotations are Anno's view, present on files that carry them.
	Annotations []Annotation `json:"annotations,omitempty"`
	// Skipped says why the text is absent, empty when it is present.
	Skipped string `json:"skipped,omitempty"`
}

// Validate checks the file is addressable and internally consistent.
func (f File) Validate() error {
	if err := checkPath("File", "path", f.Path); err != nil {
		return err
	}
	if f.Bytes < 0 {
		return fault.Field("File", "bytes", "size %d is negative", f.Bytes)
	}
	if f.Text != "" && f.Skipped != "" {
		return fault.Field("File", "skipped", "a file with text also says why it has none")
	}
	if err := checkText("File", "text", f.Text, MaxFileBytes, true); err != nil {
		return err
	}
	for _, s := range f.Sections {
		if err := s.Validate(); err != nil {
			return err
		}
	}
	for _, a := range f.Annotations {
		if err := a.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Library is the readable repository.
type Library struct {
	// Root is what the paths are relative to, for display only.
	Root  string `json:"root"`
	Files []File `json:"files"`
	// Truncated says what was left out, empty when nothing was. A reader who
	// cannot find a file must be able to tell "too big to carry" from "not
	// there", and silence would make those the same.
	Truncated string `json:"truncated,omitempty"`
	// Notes are things that went wrong while collecting, in the reader's terms.
	//
	// A lens that could not be run is the case this exists for: without it, a
	// missing `dock` shows up as a documentation tab confidently reporting that
	// no document carries a section — a claim about the operator's files that is
	// not true, when the truth is that a tool is not installed.
	Notes []string `json:"notes,omitempty"`
}

// Validate checks the library and every file in it.
func (l Library) Validate() error {
	if err := checkText("Library", "root", l.Root, MaxPathRunes, true); err != nil {
		return err
	}
	for _, note := range l.Notes {
		if err := checkText("Library", "notes", note, MaxSubjectRunes, false); err != nil {
			return err
		}
	}
	if len(l.Files) > MaxLibraryFiles {
		return fault.Field("Library", "files", "%d files exceeds the limit of %d", len(l.Files), MaxLibraryFiles)
	}
	seen := make(map[string]bool, len(l.Files))
	for _, f := range l.Files {
		if err := f.Validate(); err != nil {
			return err
		}
		// One path, one file. A duplicate means the two ends disagree about
		// what a path addresses, and the interface would show whichever it
		// happened to reach first.
		if seen[f.Path] {
			return fault.Field("Library", "files", "%q appears twice", f.Path)
		}
		seen[f.Path] = true
	}
	return nil
}

// CheckPath is the path rule, for a caller outside this package.
//
// It is exported so the server can refuse a bad path when it arrives rather than
// only when the action it becomes is validated. One rule, in one place: a second
// copy in the handler would be a second opinion about what a path is.
func CheckPath(field, path string) error { return checkPath("request", field, path) }

// CheckText is the text rule, for the same reason. Empty is allowed: an empty
// file is a real file.
func CheckText(field, value string, max int) error {
	return checkText("request", field, value, max, true)
}

// checkSpan validates a one-based inclusive line range.
func checkSpan(where string, start, end int) error {
	if start < 1 {
		return fault.Field(where, "start", "line %d is not a line number", start)
	}
	if end < start {
		return fault.Field(where, "end", "range %d:%d ends before it begins", start, end)
	}
	return nil
}

// checkPath validates a repository-relative path.
//
// It refuses anything absolute or containing a parent step, because the
// interface turns these into links and the server looks files up by them: a
// path that climbs out of the tree is either a bug or an attempt to address
// something the library was never meant to carry.
func checkPath(where, field, path string) error {
	if path == "" {
		return fault.Field(where, field, "path is empty")
	}
	if err := checkText(where, field, path, MaxPathRunes, false); err != nil {
		return err
	}
	if strings.HasPrefix(path, "/") {
		return fault.Field(where, field, "path %q is absolute", path)
	}
	// A drive letter is absolute too, and `filepath.IsAbs` only knows that on
	// the platform that has drives — so the server would pass it along and only
	// the agent would notice.
	if len(path) >= 2 && path[1] == ':' && isLetter(path[0]) {
		return fault.Field(where, field, "path %q names a volume", path)
	}
	// A path here is separated by `/`, on every machine. A backslash is not a
	// separator to the server and is one to a Windows agent, so a path holding
	// one means two different things at the two ends of the wire — and the
	// splitting below, which is what refuses `..`, would be looking at the
	// wrong parts.
	if strings.ContainsRune(path, '\\') {
		return fault.Field(where, field, "path %q holds a backslash; paths are separated by / here, on every machine", path)
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return fault.Field(where, field, "path %q climbs out of the tree", path)
		}
	}
	return nil
}

func isLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// hex64 is a SHA-256 rendered the way every other digest in this tree is.
var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// checkDigest validates the precondition a library verb carries.
//
// It is the whole of what makes editing a mirror safe: a snapshot is minutes old
// by the time somebody acts on it, and an action that could not say what it
// expected to find would be an action that silently overwrites whatever arrived
// in between.
func checkDigest(where, field, digest string) error {
	if digest == "" {
		return fault.Field(where, field, "no digest of what was edited")
	}
	if !hex64.MatchString(digest) {
		return fault.Field(where, field, "digest %q is not 64 hex characters", digest)
	}
	return nil
}
