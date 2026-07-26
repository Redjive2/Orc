// Package target parses a link destination into the thing it addresses.
//
// One grammar covers both tools:
//
//	target := [path] ("§" ref | anno-chain)
//	ref    := number | "'" name "'"
//	number := digit { "." digit }
//
// The first resolver character decides who resolves it. § is Dock's, and @, :,
// and ^ are Anno's — an Anno chain is passed through untouched, partial or
// whole, so everything true of Anno chains holds inside a Dock link with no
// second implementation.
//
// Most destinations in a document are not targets at all. A URL, an anchor, a
// plain relative path: all ordinary markdown, all ignored. Dock's graph is about
// sections, and a graph that filled with every URL in the corpus is one nobody
// would read.
//
// # Why a malformed § is an error and a malformed chain is not
//
// § is Dock's own sign and appears nowhere else, so a destination carrying one
// means to address a section, and anything wrong after it is reported. Anno's
// resolvers are ordinary punctuation that occurs constantly in prose and in
// URLs, so a destination that fails to parse as a chain is simply not a target.
// Erring the other way would make Dock complain about every colon in every link
// in the corpus.
package target

import (
	"fmt"
	"strconv"
	"strings"

	"orc/common/fault"
)

// Sigil introduces a Dock section reference.
const Sigil = "§"

// Resolvers are Anno's, in Anno's meanings: @ section, : symbol, ^ part.
const Resolvers = "@:^"

// Limits on what a destination may carry.
const (
	// MaxLen bounds a destination. A link longer than this is not an address.
	MaxLen = 1024
	// MaxDepth bounds a section number's components, matching doc.MaxDepth.
	MaxDepth = 6
	// MaxComponent bounds one component of a number.
	MaxComponent = 9999
	// MaxSteps bounds an Anno chain's steps: section, symbol, part is three,
	// and the slack is for a chain that repeats a resolver.
	MaxSteps = 8
)

// nonHierarchical are URI schemes whose bodies would otherwise read as an Anno
// chain — "mailto:someone@example.com" parses as a path and two steps if
// nothing stops it. Like Anno's comment-closer table this is a judgement call
// rather than a specification, and it is one line to extend.
var nonHierarchical = []string{"mailto:", "tel:", "data:", "javascript:", "sms:", "urn:"}

// Kind is which tool resolves a target.
type Kind int

const (
	// Section is a Dock section, addressed with §.
	Section Kind = iota
	// Anno is an Anno annotation, addressed with a chain of @, :, and ^.
	Anno
)

// String implements fmt.Stringer.
func (k Kind) String() string {
	switch k {
	case Section:
		return "section"
	case Anno:
		return "anno"
	default:
		return "unknown"
	}
}

// Target is one reading of a destination. The zero value is not meaningful;
// targets are produced only by Parse.
type Target struct {
	path   string
	kind   Kind
	number string
	name   string
	chain  string
}

// Path returns the file part, which is empty for a same-file reference.
func (t Target) Path() string { return t.path }

// SameFile reports whether the target names something in the linking document.
func (t Target) SameFile() bool { return t.path == "" }

// Kind returns which tool resolves this target.
func (t Target) Kind() Kind { return t.kind }

// Number returns a section number without its §, or "" when the target names a
// section by name or is an Anno target.
func (t Target) Number() string { return t.number }

// Name returns a section name, or "" when the target names a section by number
// or is an Anno target.
func (t Target) Name() string { return t.name }

// Chain returns an Anno chain verbatim, resolver characters included, ready to
// be handed to anno. It is "" for a section target.
func (t Target) Chain() string { return t.chain }

// String renders the target in the canonical form, which is always a
// destination that parses back to the same reading.
func (t Target) String() string {
	switch {
	case t.kind == Anno:
		return t.path + t.chain
	case t.number != "":
		return t.path + Sigil + t.number
	default:
		return t.path + Sigil + "'" + t.name + "'"
	}
}

// Parse reads a destination into every valid reading of it, ordered
// most-path-first.
//
// A path may itself contain a resolver character — a directory named a:b, a
// file named guide§1.md — so the split between path and address cannot be
// decided by syntax alone. Parse returns all the readings and lets the command
// layer take the first whose path exists on disk, which is Anno's answer to the
// same problem.
//
// The bool reports whether the destination addresses anything at all. False
// with a nil error means an ordinary markdown link: a URL, an anchor, a plain
// path. That is not a failure and must not be reported as one.
func Parse(dest string) ([]Target, bool, error) {
	dest = strings.TrimSpace(dest)
	switch {
	case dest == "":
		return nil, false, nil
	case len(dest) > MaxLen:
		return nil, false, nil
	case strings.HasPrefix(dest, "#"):
		return nil, false, nil // an in-page anchor
	case strings.Contains(dest, "://"):
		return nil, false, nil // a URL
	}
	for _, scheme := range nonHierarchical {
		if strings.HasPrefix(strings.ToLower(dest), scheme) {
			return nil, false, nil
		}
	}

	if strings.Contains(dest, Sigil) {
		return parseSection(dest)
	}
	if out := parseAnno(dest); len(out) > 0 {
		return out, true, nil
	}
	return nil, false, nil
}

// parseSection reads every "path§ref" split of a destination containing a §.
//
// Every § is tried as the separator, longest path first. A destination with a §
// in it means to address a section, so failing to find one valid reading is an
// error rather than a shrug.
func parseSection(dest string) ([]Target, bool, error) {
	var (
		out  []Target
		last error
	)
	for i := strings.LastIndex(dest, Sigil); i >= 0; i = strings.LastIndex(dest[:i], Sigil) {
		path := dest[:i]
		rest := dest[i+len(Sigil):]
		number, name, err := parseRef(rest)
		if err != nil {
			last = err
			continue
		}
		out = append(out, Target{path: path, kind: Section, number: number, name: name})
	}
	if len(out) == 0 {
		if last == nil {
			last = fault.Parse{Reason: fmt.Sprintf("%q addresses no section; expected %s1.2 or %s'a name'", dest, Sigil, Sigil)}
		}
		return nil, false, last
	}
	return out, true, nil
}

// parseRef reads the part after a §: either a dotted number or a quoted name.
func parseRef(s string) (number, name string, err error) {
	fail := func(format string, args ...any) (string, string, error) {
		return "", "", fault.Parse{Reason: fmt.Sprintf(format, args...)}
	}
	if s == "" {
		return fail("%s must be followed by a number or a quoted name", Sigil)
	}

	if s[0] == '\'' {
		end := strings.IndexByte(s[1:], '\'')
		if end < 0 {
			return fail("%s%s has no closing quote", Sigil, s)
		}
		if rest := s[1+end+1:]; rest != "" {
			return fail("%s%s has trailing text after the closing quote", Sigil, s)
		}
		inner := strings.TrimSpace(s[1 : 1+end])
		if inner == "" {
			return fail("%s'' names no section", Sigil)
		}
		return "", inner, nil
	}

	parts := strings.Split(s, ".")
	if len(parts) > MaxDepth {
		return fail("%s%s is %d deep, past the maximum of %d", Sigil, s, len(parts), MaxDepth)
	}
	for _, p := range parts {
		switch {
		case p == "":
			return fail("%s%s has an empty component", Sigil, s)
		case len(p) > 1 && p[0] == '0':
			return fail("%s%s has a leading zero", Sigil, s)
		}
		n, convErr := strconv.Atoi(p)
		if convErr != nil {
			return fail("%s%s is not a number or a quoted name", Sigil, s)
		}
		if n < 1 {
			return fail("%s%s numbers sections from 1", Sigil, s)
		}
		if n > MaxComponent {
			return fail("%s%s exceeds the maximum component %d", Sigil, s, MaxComponent)
		}
	}
	return s, "", nil
}

// parseAnno reads every "path<chain>" split of a destination, longest path
// first. A destination that yields no valid reading is not an Anno target and
// not an error — see the package comment.
func parseAnno(dest string) []Target {
	var out []Target
	for i := strings.LastIndexAny(dest, Resolvers); i >= 0; i = strings.LastIndexAny(dest[:i], Resolvers) {
		if !validChain(dest[i:]) {
			continue
		}
		out = append(out, Target{path: dest[:i], kind: Anno, chain: dest[i:]})
	}
	return out
}

// validChain reports whether s is a well-formed sequence of resolver-qualified
// steps. Step names exclude whitespace, path separators, brackets, and the
// resolver characters, so a chain once started has exactly one reading.
func validChain(s string) bool {
	if s == "" || !strings.ContainsRune(Resolvers, rune(s[0])) {
		return false
	}
	steps := 0
	for i := 0; i < len(s); {
		i++ // the resolver
		start := i
		for i < len(s) && !strings.ContainsRune(Resolvers, rune(s[i])) {
			if !nameByte(s[i]) {
				return false
			}
			i++
		}
		if i == start {
			return false // a resolver with no name
		}
		steps++
		if steps > MaxSteps {
			return false
		}
	}
	return steps > 0
}

func nameByte(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '/', '\\', '[', ']', '(', ')', '<', '>', '"', '\'', '#', '?':
		return false
	}
	return c >= 0x21 && c != 0x7f
}
