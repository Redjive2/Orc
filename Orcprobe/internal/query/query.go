// Package query selects messages across every mailbox at once.
//
// The grammar is Mailman's, deliberately: `from="boss" & subject~"work"`, with
// `&`, `|`, `!`, and `()`. One language for selecting mail, not two — an
// operator who has learned Mailman's queries has learned these.
//
// What differs is the *scope*, and the difference is the whole point of the
// view. Mailman evaluates a query against one mailbox, so `unread` and `id`
// mean "unread by you" and "your puid". Orcprobe evaluates against the store,
// where a message is unread by some people and read by others — so those
// fields mean "unread by anybody" and "any recipient's puid", and the table
// shows which. That is stated here, in the help, and in the reference, because
// a field that quietly means something else in a second tool is worse than a
// field that does not exist.
//
// An unknown field is always an error, never a term that silently matches
// nothing. That rule is Mailman's too, and it is what stops a typo in a query
// reading as "no results".
package query

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"orc/orcprobe/internal/clock"
	"orc/orcprobe/internal/fault"
	"orc/orcprobe/internal/read"
)

// Fields returns every selectable field, for help text and error messages.
func Fields() []string {
	out := make([]string, 0, len(fields))
	for name := range fields {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Op is a comparison.
type Op string

// The three operators, as Mailman defines them.
const (
	// Equal is exact; for to and cc it means "is a recipient".
	Equal Op = "="
	// NotEqual is the negation of Equal.
	NotEqual Op = "!="
	// Contains is a case-insensitive substring.
	Contains Op = "~"
)

// Query is a parsed selection.
type Query struct {
	root node
	text string
}

// String returns the query as written.
func (q Query) String() string { return q.text }

// Match reports whether a message is selected.
func (q Query) Match(m read.Message, now time.Time) bool {
	if q.root == nil {
		return true
	}
	return q.root.match(m, now)
}

// Select filters messages, oldest first, preserving the order it was given.
func (q Query) Select(messages []read.Message, now time.Time) []read.Message {
	out := make([]read.Message, 0, len(messages))
	for _, m := range messages {
		if q.Match(m, now) {
			out = append(out, m)
		}
	}
	return out
}

// Everything is the query that matches every message.
func Everything() Query { return Query{} }

type node interface {
	match(read.Message, time.Time) bool
}

type andNode struct{ left, right node }
type orNode struct{ left, right node }
type notNode struct{ inner node }

func (n andNode) match(m read.Message, now time.Time) bool {
	return n.left.match(m, now) && n.right.match(m, now)
}
func (n orNode) match(m read.Message, now time.Time) bool {
	return n.left.match(m, now) || n.right.match(m, now)
}
func (n notNode) match(m read.Message, now time.Time) bool { return !n.inner.match(m, now) }

type termNode struct {
	field string
	op    Op
	value string
}

// field describes how one selectable field is evaluated.
//
// Each returns the strings a term compares against. A field that yields several
// — every recipient, every puid — matches if *any* of them does, which is what
// makes `to="alice"` mean "alice is a recipient" rather than "alice is the only
// recipient".
type field struct {
	// values yields what to compare against.
	values func(read.Message) []string
	// numeric marks a field compared as a number rather than as text.
	numeric bool
	// temporal marks a field compared as an instant.
	temporal bool
	// boolean marks a field whose value is true or false.
	boolean bool
	// help is the one-line description in the error a typo produces.
	help string
}

var fields = map[string]field{
	"mid":     {values: func(m read.Message) []string { return []string{m.MID} }, help: "the message's store id"},
	"kind":    {values: func(m read.Message) []string { return []string{m.Kind} }, help: "mail, cc, and so on"},
	"from":    {values: func(m read.Message) []string { return []string{m.From} }, help: "the sender"},
	"to":      {values: func(m read.Message) []string { return m.To }, help: "is a direct recipient"},
	"cc":      {values: func(m read.Message) []string { return m.CC }, help: "is a copied recipient"},
	"subject": {values: func(m read.Message) []string { return []string{m.Subject} }, help: "the subject line"},
	"body":    {values: func(m read.Message) []string { return []string{string(m.Body)} }, help: "the message text"},
	"convo":   {values: func(m read.Message) []string { return []string{m.Convo} }, help: "the conversation id"},
	"any": {values: func(m read.Message) []string {
		return append([]string{m.From}, m.Recipients()...)
	}, help: "sender or any recipient"},
	"read": {values: func(m read.Message) []string { return m.Readers }, help: "has read it"},

	"index": {values: func(m read.Message) []string { return []string{strconv.Itoa(m.Index)} },
		numeric: true, help: "position within a conversation"},
	"bytes": {values: func(m read.Message) []string { return []string{strconv.Itoa(m.Size)} },
		numeric: true, help: "body size"},
	"id": {values: func(m read.Message) []string {
		// Every recipient's puid, because a puid is per-mailbox and this view
		// is not in one.
		out := make([]string, 0, len(m.PUID))
		for _, puid := range m.PUID {
			out = append(out, strconv.Itoa(puid))
		}
		return out
	}, numeric: true, help: "any recipient's puid (per-mailbox in mailman)"},

	"unread": {values: func(m read.Message) []string { return []string{boolText(m.UnreadBy())} },
		boolean: true, help: "unread by anybody (per-mailbox in mailman)"},
	"archived": {values: func(m read.Message) []string {
		for _, archived := range m.Archived {
			if archived {
				return []string{"true"}
			}
		}
		return []string{"false"}
	}, boolean: true, help: "archived by anybody"},

	"before": {values: func(m read.Message) []string { return []string{clock.Format(m.Sent)} },
		temporal: true, help: "sent before an instant"},
	"after": {values: func(m read.Message) []string { return []string{clock.Format(m.Sent)} },
		temporal: true, help: "sent after an instant"},
}

func (t termNode) match(m read.Message, now time.Time) bool {
	spec := fields[t.field]
	values := spec.values(m)

	switch {
	case spec.temporal:
		at, err := clock.Parse(values[0])
		if err != nil {
			return false
		}
		want, err := parseInstant(t.value, now)
		if err != nil {
			return false
		}
		if t.field == "before" {
			return at.Before(want)
		}
		return at.After(want)

	default:
		// A field can yield several values — every recipient, every puid — and
		// the term is evaluated positively against all of them. `!=` is then
		// the negation of that, which is what makes `to!="alice"` mean "alice
		// is not a recipient" rather than "some recipient is not alice".
		op := t.op
		if op == NotEqual {
			op = Equal
		}
		hit := false
		for _, v := range values {
			if compare(v, t.value, op, spec.numeric) {
				hit = true
				break
			}
		}
		if t.op == NotEqual {
			return !hit
		}
		return hit
	}
}

func compare(got, want string, op Op, numeric bool) bool {
	if numeric {
		// "contains" on a number is not a question anyone means to ask, so it
		// is treated as equality rather than as a substring of the digits.
		g, gerr := strconv.Atoi(got)
		w, werr := strconv.Atoi(want)
		return gerr == nil && werr == nil && g == w
	}
	switch op {
	case Contains:
		return strings.Contains(strings.ToLower(got), strings.ToLower(want))
	default:
		return strings.EqualFold(got, want)
	}
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// parseInstant reads a time for before/after: a stored timestamp, a date, or a
// span like "2h" meaning "two hours ago".
func parseInstant(text string, now time.Time) (time.Time, error) {
	if at, err := clock.Parse(text); err == nil {
		return at, nil
	}
	if at, err := time.Parse("2006-01-02", text); err == nil {
		return at.UTC(), nil
	}
	if d, err := time.ParseDuration(text); err == nil {
		return now.Add(-d), nil
	}
	return time.Time{}, fault.Query{Query: text, Reason: "not a timestamp, a date, or a span like 2h"}
}
