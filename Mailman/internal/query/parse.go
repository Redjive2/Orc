package query

import (
	"fmt"
	"strings"

	"orc/common/fault"
)

// node is one element of the parsed expression. The tree is immutable once
// Parse returns it: nothing exported from this package can reach a node, and
// the parser's own builder is confined to parser.
type node interface {
	// eval reports whether the node matches, given a subject and the instant to
	// resolve relative times against.
	eval(s Subject, now Now) (bool, error)
	// String renders the node canonically, which is what lets a query be echoed
	// back in `prune`'s confirmation and in an ambiguity report.
	String() string
}

// Now carries the instant a relative predicate such as before=7d is measured
// from. It is a named type rather than a bare time.Time so a call site cannot
// pass a message's own timestamp by accident.
type Now struct {
	At timeValue
}

// Query is a parsed expression. The zero value matches nothing and reports
// itself as such rather than matching everything, because a query that
// silently became empty must not archive a mailbox.
type Query struct {
	root node
	raw  string
}

// Raw returns the query exactly as it was written, for error messages.
func (q Query) Raw() string { return q.raw }

// Zero reports whether the query was never parsed.
func (q Query) Zero() bool { return q.root == nil }

// String renders the query canonically, with the precedence it was actually
// parsed with made explicit. `prune` prints this before deleting anything, so a
// caller who meant something else can see that they did.
func (q Query) String() string {
	if q.root == nil {
		return "<empty>"
	}
	return q.root.String()
}

// Parse reads a query.
func Parse(raw string) (Query, error) {
	if strings.TrimSpace(raw) == "" {
		return Query{}, fault.Query{Query: raw, Reason: "query is empty"}
	}

	tokens, err := lex(raw)
	if err != nil {
		return Query{}, err
	}

	p := &parser{raw: raw, tokens: tokens}
	root, err := p.parseOr()
	if err != nil {
		return Query{}, err
	}
	if got := p.peek(); got.kind != tokEOF {
		// A stray closer at the top level is worth naming specifically: the
		// generic "unexpected token" reading is true but leaves the caller
		// counting brackets.
		if got.kind == tokClose {
			return Query{}, p.fail(got, "%s closes a group that was never opened", got.kind)
		}
		return Query{}, p.fail(got, "unexpected %s after a complete query", got.kind)
	}
	if root == nil {
		return Query{}, fault.Query{Query: raw, Reason: "query is empty"}
	}

	return Query{root: root, raw: raw}, nil
}

// parser is the mutable state of one parse, confined to this file and thrown
// away when Parse returns.
type parser struct {
	raw    string
	tokens []token
	at     int
	depth  int
}

func (p *parser) peek() token { return p.tokens[p.at] }

func (p *parser) next() token {
	t := p.tokens[p.at]
	if t.kind != tokEOF {
		p.at++
	}
	return t
}

func (p *parser) fail(t token, format string, args ...any) error {
	return fault.Query{Query: p.raw, Col: t.col, Reason: fmt.Sprintf(format, args...)}
}

// parseOr handles the loosest binding: a | b | c.
func (p *parser) parseOr() (node, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tokOr {
		t := p.next()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		if right == nil {
			return nil, p.fail(t, "%s needs an expression on its right", t.kind)
		}
		left = orNode{left: left, right: right}
	}
	return left, nil
}

// parseAnd handles a & b & c, which binds tighter than |.
func (p *parser) parseAnd() (node, error) {
	left, err := p.parseTerm()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tokAnd {
		t := p.next()
		right, err := p.parseTerm()
		if err != nil {
			return nil, err
		}
		if right == nil {
			return nil, p.fail(t, "%s needs an expression on its right", t.kind)
		}
		left = andNode{left: left, right: right}
	}
	return left, nil
}

// parseTerm handles grouping, negation, and predicates.
func (p *parser) parseTerm() (node, error) {
	t := p.peek()

	switch t.kind {
	case tokOpen:
		p.next()
		p.depth++
		if p.depth > MaxDepth {
			return nil, p.fail(t, "query is nested more than %d deep", MaxDepth)
		}
		// Checked before descending, so "()" is reported as what it is rather
		// than as the unmatched closer the recursion would trip over first.
		if p.peek().kind == tokClose {
			return nil, p.fail(t, "empty parentheses")
		}
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if inner == nil {
			return nil, p.fail(t, "empty parentheses")
		}
		closing := p.peek()
		if closing.kind != tokClose {
			return nil, p.fail(closing, "expected %s to close the group opened at column %d, found %s",
				tokClose, t.col, closing.kind)
		}
		p.next()
		p.depth--
		return groupNode{inner: inner}, nil

	case tokNot:
		p.next()
		// Checked before descending, so the complaint names the negation rather
		// than the end of the query the recursion would reach first.
		if k := p.peek().kind; k == tokEOF || k == tokClose || k == tokAnd || k == tokOr {
			return nil, p.fail(t, "%s needs an expression to negate", t.kind)
		}
		inner, err := p.parseTerm()
		if err != nil {
			return nil, err
		}
		if inner == nil {
			return nil, p.fail(t, "%s needs an expression to negate", t.kind)
		}
		return notNode{inner: inner}, nil

	case tokIdent:
		return p.parsePredicate()

	case tokValue:
		return nil, p.fail(t, "a bare value needs a field, as in subject=%q", t.text)

	case tokEOF:
		return nil, p.fail(t, "query ends where an expression was expected")

	case tokClose:
		return nil, p.fail(t, "%s closes a group that was never opened", t.kind)

	default:
		return nil, p.fail(t, "expected a field name, found %s", t.kind)
	}
}

// parsePredicate reads field op value.
func (p *parser) parsePredicate() (node, error) {
	nameTok := p.next()

	f, ok := lookupField(nameTok.text)
	if !ok {
		reason := fmt.Sprintf("unknown field %q", nameTok.text)
		if near := suggest(nameTok.text); len(near) > 0 {
			reason += fmt.Sprintf("; did you mean %s?", strings.Join(quoteAll(near), " or "))
		}
		reason += "\n  fields: " + strings.Join(FieldNames(), ", ")
		return nil, p.fail(nameTok, "%s", reason)
	}

	opTok := p.next()
	var o op
	switch opTok.kind {
	case tokEQ:
		o = opEQ
	case tokNE:
		o = opNE
	case tokContains:
		o = opContains
	default:
		return nil, p.fail(opTok, "expected =, !=, or ~ after %q, found %s", f.name, opTok.kind)
	}

	if !f.shape.allows(o) {
		return nil, p.fail(opTok, "%s takes %s, so %s is not meaningful on it", f.name, f.shape.describe(), o)
	}

	valTok := p.next()
	if valTok.kind != tokIdent && valTok.kind != tokValue {
		return nil, p.fail(valTok, "expected a value after %q%s, found %s", f.name, o, valTok.kind)
	}

	v, err := parseValue(valTok.text, f, p.raw, valTok.col)
	if err != nil {
		return nil, err
	}
	return predNode{field: f, op: o, value: v, raw: valTok.text}, nil
}

func quoteAll(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = fmt.Sprintf("%q", s)
	}
	return out
}

// The node types. Each renders itself with explicit parentheses where it had
// them, so String round-trips through Parse to an equivalent query.

type andNode struct{ left, right node }

func (n andNode) String() string { return n.left.String() + " & " + n.right.String() }

type orNode struct{ left, right node }

func (n orNode) String() string { return n.left.String() + " | " + n.right.String() }

type notNode struct{ inner node }

func (n notNode) String() string { return "!" + n.inner.String() }

type groupNode struct{ inner node }

func (n groupNode) String() string { return "(" + n.inner.String() + ")" }

type predNode struct {
	field field
	op    op
	value value
	raw   string
}

func (n predNode) String() string {
	return fmt.Sprintf("%s%s%q", n.field.name, n.op, n.raw)
}
