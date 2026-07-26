// Package query parses and evaluates the expressions every Mailman command
// selects mail with.
//
// The language is the one Reference.md shows — `from="boss"`, joined by `&` and
// `|` — formalised, with grouping, negation, and a substring operator added.
// Every documented form parses identically under this grammar.
//
// Parsing and evaluation are pure. Nothing here reads a file or a clock, which
// is what makes the whole language exhaustively table-testable and cheap to
// fuzz — and a query language is exactly the surface where a silent
// misinterpretation turns a `prune` into a disaster.
package query

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"orc/common/fault"
)

// Limits on a query. Each turns a pathological input into a clear message
// instead of a stack overflow or a hung command.
const (
	// MaxLength is far beyond any query a person or an agent writes.
	MaxLength = 8 << 10

	// MaxDepth bounds parenthesised nesting. Recursive descent uses the
	// goroutine stack, so this is what stops "((((((…" from crashing the process.
	MaxDepth = 32

	// MaxValue bounds one predicate's value.
	MaxValue = 1 << 10
)

// kind classifies a token.
type kind int

const (
	tokEOF kind = iota
	tokIdent
	tokValue // a quoted string; always a value, never a field
	tokAnd
	tokOr
	tokNot
	tokOpen
	tokClose
	tokEQ
	tokNE
	tokContains
)

func (k kind) String() string {
	switch k {
	case tokEOF:
		return "end of query"
	case tokIdent:
		return "name"
	case tokValue:
		return "quoted value"
	case tokAnd:
		return `"&"`
	case tokOr:
		return `"|"`
	case tokNot:
		return `"!"`
	case tokOpen:
		return `"("`
	case tokClose:
		return `")"`
	case tokEQ:
		return `"="`
	case tokNE:
		return `"!="`
	case tokContains:
		return `"~"`
	default:
		return fmt.Sprintf("kind(%d)", int(k))
	}
}

// token is one lexical unit with the rune column it started at, so every
// complaint about it can point exactly there.
type token struct {
	kind kind
	text string
	col  int // 1-indexed rune column
}

// lex turns a query into tokens.
//
// It reports the first problem it finds rather than accumulating: a query is
// one short line, and a caret under the first mistake is more useful than a
// list of consequences of it.
func lex(raw string) ([]token, error) {
	bad := func(col int, format string, args ...any) error {
		return fault.Query{Query: raw, Col: col, Reason: fmt.Sprintf(format, args...)}
	}

	if len(raw) > MaxLength {
		return nil, bad(0, "query is %d bytes, limit is %d", len(raw), MaxLength)
	}
	if !utf8.ValidString(raw) {
		return nil, bad(0, "query is not valid UTF-8")
	}

	runes := []rune(raw)
	var out []token

	for i := 0; i < len(runes); {
		r := runes[i]
		col := i + 1

		switch {
		case unicode.IsSpace(r):
			i++

		case r == '&':
			out = append(out, token{kind: tokAnd, text: "&", col: col})
			i++
		case r == '|':
			out = append(out, token{kind: tokOr, text: "|", col: col})
			i++
		case r == '(':
			out = append(out, token{kind: tokOpen, text: "(", col: col})
			i++
		case r == ')':
			out = append(out, token{kind: tokClose, text: ")", col: col})
			i++
		case r == '=':
			out = append(out, token{kind: tokEQ, text: "=", col: col})
			i++
		case r == '~':
			out = append(out, token{kind: tokContains, text: "~", col: col})
			i++

		case r == '!':
			// "!=" and "!" are distinguished by what follows, so a stray "!" at
			// the very end of a query is a negation with nothing to negate
			// rather than a broken operator.
			if i+1 < len(runes) && runes[i+1] == '=' {
				out = append(out, token{kind: tokNE, text: "!=", col: col})
				i += 2
			} else {
				out = append(out, token{kind: tokNot, text: "!", col: col})
				i++
			}

		case r == '"' || r == '\'':
			text, next, err := lexQuoted(raw, runes, i)
			if err != nil {
				return nil, err
			}
			out = append(out, token{kind: tokValue, text: text, col: col})
			i = next

		default:
			text, next, err := lexBare(raw, runes, i)
			if err != nil {
				return nil, err
			}
			out = append(out, token{kind: tokIdent, text: text, col: col})
			i = next
		}

		if len(out) > MaxLength {
			return nil, bad(col, "query has too many terms")
		}
	}

	return append(out, token{kind: tokEOF, col: len(runes) + 1}), nil
}

// lexQuoted reads a quoted value. Both quote styles are accepted so a query can
// be embedded in either kind of shell quoting without escaping.
//
// A backslash escapes the quote character and itself, and nothing else: a
// language with a large escape table invites a query that means one thing to
// the shell and another to Mailman.
func lexQuoted(raw string, runes []rune, start int) (string, int, error) {
	quote := runes[start]
	var b strings.Builder

	for i := start + 1; i < len(runes); i++ {
		r := runes[i]
		switch {
		case r == '\\':
			if i+1 >= len(runes) {
				return "", 0, fault.Query{Query: raw, Col: i + 1, Reason: "a backslash at the end of the query escapes nothing"}
			}
			next := runes[i+1]
			if next != quote && next != '\\' {
				return "", 0, fault.Query{Query: raw, Col: i + 1,
					Reason: fmt.Sprintf("a backslash may only escape %c or another backslash, not %q", quote, next)}
			}
			b.WriteRune(next)
			i++

		case r == quote:
			if b.Len() > MaxValue {
				return "", 0, fault.Query{Query: raw, Col: start + 1,
					Reason: fmt.Sprintf("value is %d bytes, limit is %d", b.Len(), MaxValue)}
			}
			return b.String(), i + 1, nil

		case isForbidden(r):
			return "", 0, fault.Query{Query: raw, Col: i + 1, Reason: "value contains a control character"}

		default:
			b.WriteRune(r)
		}
	}

	return "", 0, fault.Query{Query: raw, Col: start + 1, Reason: fmt.Sprintf("unterminated %c quote", quote)}
}

// lexBare reads an unquoted name or value. It stops at whitespace or at any
// character with syntactic meaning, so `from=boss&to=alice` lexes the way it
// reads.
func lexBare(raw string, runes []rune, start int) (string, int, error) {
	i := start
	for i < len(runes) {
		r := runes[i]
		if unicode.IsSpace(r) || strings.ContainsRune("&|()=~!\"'", r) {
			break
		}
		if isForbidden(r) {
			return "", 0, fault.Query{Query: raw, Col: i + 1, Reason: "query contains a control character"}
		}
		i++
	}
	if i == start {
		return "", 0, fault.Query{Query: raw, Col: start + 1, Reason: fmt.Sprintf("unexpected %q", runes[start])}
	}
	text := string(runes[start:i])
	if len(text) > MaxValue {
		return "", 0, fault.Query{Query: raw, Col: start + 1,
			Reason: fmt.Sprintf("value is %d bytes, limit is %d", len(text), MaxValue)}
	}
	return text, i, nil
}

// isForbidden reports whether a rune may never appear in a query. Control
// characters are refused outright: a query is echoed back in error messages and
// in `prune`'s confirmation list, and an escape sequence hidden in one would
// let a query misrepresent what it is about to delete.
func isForbidden(r rune) bool {
	return r < 0x20 || r == 0x7F || (r >= 0x80 && r <= 0x9F)
}
