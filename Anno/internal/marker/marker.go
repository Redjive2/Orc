// Package marker classifies a single line of source as an annotation marker.
//
// It is a pure function of the line text: no file access, no surrounding
// context, no state. Everything about whether a line is well formed is decided
// here, so the tree builder downstream can assume it is handed valid markers.
package marker

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"orc/common/fault"
)

// Op is what a marker does to the annotation stack.
type Op int

const (
	// Open begins an annotation that runs until it is closed or superseded.
	Open Op = iota
	// Close ends the innermost open annotation of a given name.
	Close
	// Next annotates exactly the following line.
	Next
)

// String implements fmt.Stringer.
func (o Op) String() string {
	switch o {
	case Open:
		return "open"
	case Close:
		return "close"
	case Next:
		return "next"
	default:
		return fmt.Sprintf("Op(%d)", int(o))
	}
}

// Sigil returns the three-character sequence that introduces the op.
func (o Op) Sigil() string {
	switch o {
	case Open:
		return "@:>"
	case Close:
		return "@:<"
	case Next:
		return "@:;"
	default:
		return ""
	}
}

// Kind is an annotation's granularity. Its numeric value is its rank: a kind
// nests inside every kind of lower rank and terminates every kind of equal or
// higher rank.
type Kind int

const (
	// Section is a region of a file: types, handlers, and the like.
	Section Kind = iota
	// Symbol is a single declaration.
	Symbol
	// Part is a handful of lines inside a declaration.
	Part
)

// Kinds lists every kind in rank order.
var Kinds = [...]Kind{Section, Symbol, Part}

// String returns the kind's spelling as it appears in a marker.
func (k Kind) String() string {
	switch k {
	case Section:
		return "section"
	case Symbol:
		return "symbol"
	case Part:
		return "part"
	default:
		return fmt.Sprintf("Kind(%d)", int(k))
	}
}

// Resolver returns the character that selects this kind in a target chain.
func (k Kind) Resolver() rune {
	switch k {
	case Section:
		return '@'
	case Symbol:
		return ':'
	case Part:
		return '^'
	default:
		return 0
	}
}

// Rank returns the kind's nesting depth. Lower ranks contain higher ranks.
func (k Kind) Rank() int { return int(k) }

// Valid reports whether k is one of the three defined kinds.
func (k Kind) Valid() bool { return k >= Section && k <= Part }

// ParseKind maps a kind's spelling to its value.
func ParseKind(s string) (Kind, bool) {
	for _, k := range Kinds {
		if k.String() == s {
			return k, true
		}
	}
	return 0, false
}

// KindForResolver maps a resolver character to the kind it selects.
func KindForResolver(r rune) (Kind, bool) {
	for _, k := range Kinds {
		if k.Resolver() == r {
			return k, true
		}
	}
	return 0, false
}

// IsResolver reports whether r selects a kind.
func IsResolver(r rune) bool {
	_, ok := KindForResolver(r)
	return ok
}

// closers are trailing tokens that a host language requires after a
// line-terminal comment. They are stripped before the annotation is parsed, so
// block-comment languages can carry annotations too. Longest first, so "--}}"
// is not mistaken for "--".
var closers = [...]string{"--}}", "-->", "*/", "*)", "#}", "}}", "--", "*>"}

// Marker is a parsed annotation marker. The zero value is not meaningful;
// markers are produced only by Classify.
type Marker struct {
	op   Op
	kind Kind
	name string
	meta []string
	line int
	col  int
}

// Op returns what the marker does.
func (m Marker) Op() Op { return m.op }

// Kind returns the annotation kind. It is meaningless for Close markers, which
// name an annotation without restating its kind.
func (m Marker) Kind() Kind { return m.kind }

// Name returns the annotation name.
func (m Marker) Name() string { return m.name }

// Meta returns a copy of the metadata list.
func (m Marker) Meta() []string {
	out := make([]string, len(m.meta))
	copy(out, m.meta)
	return out
}

// Line returns the 1-indexed line the marker was found on.
func (m Marker) Line() int { return m.line }

// Col returns the 1-indexed rune column of the marker's sigil.
func (m Marker) Col() int { return m.col }

// String renders the marker back to its canonical source form.
func (m Marker) String() string {
	var b strings.Builder
	b.WriteString(m.op.Sigil())
	if m.op != Close {
		b.WriteString(" ")
		b.WriteString(m.kind.String())
	}
	b.WriteString(" ")
	b.WriteString(m.name)
	if len(m.meta) > 0 {
		b.WriteString(" [")
		b.WriteString(strings.Join(m.meta, " "))
		b.WriteString("]")
	}
	return b.String()
}

// Classify examines one line. It reports whether the line carries a marker and,
// if the line carries something that means to be a marker but is malformed,
// returns a fault.Parse naming the position and the problem. A line with no
// sigil at all is not an error: most lines are ordinary code.
func Classify(path string, lineNo int, text string) (Marker, bool, error) {
	if lineNo < 1 {
		return Marker{}, false, fault.Internal{Where: "marker.Classify", Detail: fmt.Sprintf("line number %d is not positive", lineNo)}
	}

	op, idx, ok := findSigil(text)
	if !ok {
		return Marker{}, false, nil
	}
	col := utf8.RuneCountInString(text[:idx]) + 1

	fail := func(format string, args ...any) (Marker, bool, error) {
		return Marker{}, false, fault.Parse{Path: path, Line: lineNo, Col: col, Reason: fmt.Sprintf(format, args...)}
	}

	tail := strings.TrimSpace(text[idx+len(op.Sigil()):])
	tail = strings.TrimSpace(stripCloser(tail))
	if tail == "" {
		return fail("%s marker has no body; expected %s", op.Sigil(), shape(op))
	}

	body, meta, err := splitMeta(tail)
	if err != nil {
		return fail("%s", err.Error())
	}
	if op == Close && meta != nil {
		return fail("close marker takes no metadata; expected %s", shape(op))
	}

	// splitMeta has already guaranteed that body carries no brackets.
	fields := strings.Fields(body)

	if op == Close {
		if len(fields) != 1 {
			return fail("close marker takes exactly one name, got %d; expected %s", len(fields), shape(op))
		}
		name, err := checkName(fields[0])
		if err != nil {
			return fail("%s", err.Error())
		}
		return Marker{op: op, kind: Section, name: name, line: lineNo, col: col}, true, nil
	}

	if len(fields) < 2 {
		return fail("expected %s, got %q", shape(op), tail)
	}
	if len(fields) > 2 {
		return fail("expected %s; %q has %d words before the metadata group", shape(op), body, len(fields))
	}
	kind, ok := ParseKind(fields[0])
	if !ok {
		return fail("unknown annotation kind %q; expected one of section, symbol, part", fields[0])
	}
	name, err := checkName(fields[1])
	if err != nil {
		return fail("%s", err.Error())
	}
	return Marker{op: op, kind: kind, name: name, meta: meta, line: lineNo, col: col}, true, nil
}

// shape describes an op's expected syntax, for error messages.
func shape(op Op) string {
	if op == Close {
		return "@:< <name>"
	}
	return op.Sigil() + " <kind> <name> [<metadata...>]"
}

// findSigil locates the marker sigil on a line, if there is one.
//
// The last sigil that is not inside a string literal. Last, because an
// annotation occupies the tail of its line and a sigil earlier on it is a
// discussion of the syntax with the real marker following. Not inside a
// literal, because a program that *mentions* `@:>` in a string is not a program
// that is annotated — and without that rule Anno cannot read its own source,
// its own tests, or anything else that talks about how it works.
func findSigil(text string) (Op, int, bool) {
	best := -1
	var bestOp Op
	for _, op := range [...]Op{Open, Close, Next} {
		// Every occurrence, not only the last: a quoted sigil at the end of a
		// line must not hide a real marker earlier on it.
		for i := 0; ; {
			at := strings.Index(text[i:], op.Sigil())
			if at < 0 {
				break
			}
			at += i
			if at > best && !insideLiteral(text, at) {
				best, bestOp = at, op
			}
			i = at + 1
		}
	}
	if best < 0 {
		return 0, 0, false
	}
	return bestOp, best, true
}

// insideLiteral reports whether the byte at idx sits inside a quoted string.
//
// It is a scanner, not a parser: Anno annotates any text file and has no idea
// what language it is looking at, so this knows only the two quoting characters
// that mean "literal" in nearly all of them — the double quote, with backslash
// escapes, and the backtick, which has none.
//
// The single quote is deliberately not one of them. It is an apostrophe far more
// often than it is a delimiter, and treating "don't" as an open string would
// hide every marker after it on the line.
func insideLiteral(text string, idx int) bool {
	var quote byte
	for i := 0; i < idx && i < len(text); i++ {
		switch c := text[i]; {
		case quote == '"' && c == '\\':
			// An escaped character cannot end the string, whatever it is.
			i++
		case quote != 0 && c == quote:
			quote = 0
		case quote == 0 && (c == '"' || c == '`'):
			quote = c
		}
	}
	return quote != 0
}

// stripCloser removes one trailing comment terminator, if present.
func stripCloser(s string) string {
	trimmed := strings.TrimRightFunc(s, unicode.IsSpace)
	for _, c := range closers {
		if strings.HasSuffix(trimmed, c) {
			return trimmed[:len(trimmed)-len(c)]
		}
	}
	return s
}

// splitMeta separates the body from a trailing bracketed metadata group. A nil
// result means there was no group at all, which is distinct from an empty one.
func splitMeta(tail string) (string, []string, error) {
	open := strings.IndexByte(tail, '[')
	if open < 0 {
		if strings.ContainsRune(tail, ']') {
			return "", nil, fmt.Errorf("metadata group closes with ] but never opens")
		}
		return tail, nil, nil
	}
	if !strings.HasSuffix(strings.TrimRightFunc(tail, unicode.IsSpace), "]") {
		return "", nil, fmt.Errorf("metadata group opens with [ but is not closed at end of line")
	}
	body := tail[:open]
	inner := strings.TrimRightFunc(tail, unicode.IsSpace)
	inner = inner[open+1 : len(inner)-1]
	if strings.ContainsAny(inner, "[]") {
		return "", nil, fmt.Errorf("metadata group contains a nested bracket")
	}
	return body, strings.Fields(inner), nil
}

// checkName rejects names that could not be addressed by a target chain.
// checkName is only ever handed a word from strings.Fields, which is never
// empty; what it guards against is a word that cannot be addressed.
func checkName(name string) (string, error) {
	// A name ending in a comment terminator would lose that ending the next time
	// the marker was read, because stripCloser removes one terminator and is
	// therefore not idempotent. Such a name could never be addressed twice
	// running, so it is refused at the point it is written.
	for _, c := range closers {
		if strings.HasSuffix(name, c) {
			return "", fmt.Errorf("annotation name %q ends in %q, which would be read as a comment terminator", name, c)
		}
	}
	for _, r := range name {
		switch {
		case IsResolver(r):
			return "", fmt.Errorf("annotation name %q contains the resolver character %q, which would make it unaddressable", name, string(r))
		case r == '/' || r == '\\':
			return "", fmt.Errorf("annotation name %q contains a path separator", name)
		case unicode.IsSpace(r) || !unicode.IsPrint(r):
			return "", fmt.Errorf("annotation name %q contains a non-printing character", name)
		}
	}
	return name, nil
}
