package query

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"orc/orcprobe/internal/fault"
)

// Parse reads a query.
//
// The grammar, which is Mailman's:
//
//	query   := or
//	or      := and ("|" and)*
//	and     := unary ("&" unary)*
//	unary   := "!" unary | "(" or ")" | term
//	term    := field ("="|"!="|"~") value
//	value   := quoted | bare
//
// `|` binds loosest, then `&`, then `!`. Every failure carries the column it
// gave up at, so the error can underline the mistake rather than describe it.
func Parse(text string) (Query, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return Everything(), nil
	}

	tokens, err := lex(trimmed)
	if err != nil {
		return Query{}, err
	}
	p := &parser{tokens: tokens, text: trimmed}

	root, err := p.parseOr()
	if err != nil {
		return Query{}, err
	}
	if !p.done() {
		return Query{}, fault.Query{Query: trimmed, Col: p.col(), Reason: "unexpected " + p.peek().text}
	}
	return Query{root: root, text: trimmed}, nil
}

type kind int

const (
	kindField kind = iota
	kindOp
	kindValue
	kindAnd
	kindOr
	kindNot
	kindOpen
	kindClose
)

type token struct {
	kind kind
	text string
	col  int // 1-indexed rune column, for the caret in an error
}

func lex(text string) ([]token, error) {
	var out []token
	runes := []rune(text)

	for i := 0; i < len(runes); {
		r := runes[i]
		col := i + 1

		switch {
		case unicode.IsSpace(r):
			i++

		case r == '&':
			out = append(out, token{kindAnd, "&", col})
			i++
		case r == '|':
			out = append(out, token{kindOr, "|", col})
			i++
		case r == '(':
			out = append(out, token{kindOpen, "(", col})
			i++
		case r == ')':
			out = append(out, token{kindClose, ")", col})
			i++

		case r == '!':
			// `!=` is an operator; a bare `!` is negation. The difference is one
			// character of lookahead, and getting it wrong would silently turn
			// `from!="boss"` into "not (from = boss)" — which happens to mean the
			// same thing here, and would not for a multi-valued field.
			if i+1 < len(runes) && runes[i+1] == '=' {
				out = append(out, token{kindOp, "!=", col})
				i += 2
				continue
			}
			out = append(out, token{kindNot, "!", col})
			i++

		case r == '=':
			out = append(out, token{kindOp, "=", col})
			i++
		case r == '~':
			out = append(out, token{kindOp, "~", col})
			i++

		case r == '"' || r == '\'':
			quote := r
			i++
			var b strings.Builder
			closed := false
			for i < len(runes) {
				if runes[i] == '\\' && i+1 < len(runes) {
					i++
					b.WriteRune(runes[i])
					i++
					continue
				}
				if runes[i] == quote {
					i++
					closed = true
					break
				}
				b.WriteRune(runes[i])
				i++
			}
			if !closed {
				return nil, fault.Query{Query: text, Col: col, Reason: "unclosed quote"}
			}
			out = append(out, token{kindValue, b.String(), col})

		default:
			start := i
			var b strings.Builder
			for i < len(runes) && !isDelimiter(runes[i]) {
				b.WriteRune(runes[i])
				i++
			}
			if b.Len() == 0 {
				return nil, fault.Query{Query: text, Col: col, Reason: fmt.Sprintf("unexpected %q", string(r))}
			}
			out = append(out, token{kindField, b.String(), start + 1})
		}
	}
	return out, nil
}

func isDelimiter(r rune) bool {
	return unicode.IsSpace(r) || r == '&' || r == '|' || r == '(' || r == ')' ||
		r == '=' || r == '~' || r == '!' || r == '"' || r == '\''
}

type parser struct {
	tokens []token
	pos    int
	text   string
}

func (p *parser) done() bool { return p.pos >= len(p.tokens) }

func (p *parser) peek() token {
	if p.done() {
		return token{kind: kindClose, text: "end of query", col: len([]rune(p.text)) + 1}
	}
	return p.tokens[p.pos]
}

func (p *parser) col() int { return p.peek().col }

func (p *parser) parseOr() (node, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for !p.done() && p.peek().kind == kindOr {
		p.pos++
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = orNode{left: left, right: right}
	}
	return left, nil
}

func (p *parser) parseAnd() (node, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for !p.done() && p.peek().kind == kindAnd {
		p.pos++
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = andNode{left: left, right: right}
	}
	return left, nil
}

func (p *parser) parseUnary() (node, error) {
	if p.done() {
		return nil, fault.Query{Query: p.text, Col: p.col(), Reason: "the query ends early"}
	}

	switch p.peek().kind {
	case kindNot:
		p.pos++
		inner, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return notNode{inner: inner}, nil

	case kindOpen:
		p.pos++
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.done() || p.peek().kind != kindClose {
			return nil, fault.Query{Query: p.text, Col: p.col(), Reason: "unclosed ("}
		}
		p.pos++
		return inner, nil

	default:
		return p.parseTerm()
	}
}

func (p *parser) parseTerm() (node, error) {
	name := p.peek()
	if name.kind != kindField {
		return nil, fault.Query{Query: p.text, Col: name.col, Reason: "expected a field name"}
	}
	p.pos++

	spec, known := fields[strings.ToLower(name.text)]
	if !known {
		// An unknown field is always an error, never a term that quietly
		// matches nothing. The candidates are listed so the fix is a copy.
		return nil, fault.Query{Query: p.text, Col: name.col,
			Reason: "unknown field " + strconv.Quote(name.text) + "\n  fields: " + strings.Join(Fields(), ", ")}
	}
	_ = spec

	if p.done() || p.peek().kind != kindOp {
		return nil, fault.Query{Query: p.text, Col: p.col(), Reason: "expected =, !=, or ~ after " + name.text}
	}
	op := Op(p.peek().text)
	p.pos++

	if p.done() || (p.peek().kind != kindValue && p.peek().kind != kindField) {
		return nil, fault.Query{Query: p.text, Col: p.col(), Reason: "expected a value after " + string(op)}
	}
	value := p.peek().text
	p.pos++

	return termNode{field: strings.ToLower(name.text), op: op, value: value}, nil
}
