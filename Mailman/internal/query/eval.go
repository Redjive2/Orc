package query

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"orc/common/clock"
	"orc/common/fault"
)

// timeValue is the instant a relative predicate is measured from. It is a
// distinct type so Now cannot be built from a bare time.Time by accident.
type timeValue struct {
	at time.Time
}

// At builds the reference instant for matching.
func At(t time.Time) Now { return Now{At: timeValue{at: clock.Normalise(t)}} }

// Zero reports whether the reference instant was never set.
func (n Now) Zero() bool { return n.At.at.IsZero() }

// Subject is the message-shaped value a query is evaluated against.
//
// It is a plain struct of already-projected values rather than a message,
// because a query must be able to ask about things a message does not know:
// whether *this* reader has read it, what puid *this* reader assigned, and what
// the conversation is called. Building one is the view package's job.
type Subject struct {
	PUID     int
	MID      string
	Kind     string
	From     string
	To       []string
	CC       []string
	Subject  string
	Body     string
	Convo    string
	Title    string
	Index    int
	Unread   bool
	Archived bool
	Sent     time.Time
}

// Validate checks that a subject is complete enough to match against.
//
// It is exported and called by the view package on every subject it builds. A
// half-filled subject would not fail — it would quietly not match, which for
// `archive` means mail silently left behind and for `read` means a receipt
// never written.
func (s Subject) Validate() error {
	const where = "query.Subject"
	if err := fault.Check(s.MID != "", where, "subject has no message id"); err != nil {
		return err
	}
	if err := fault.Check(s.From != "", where, "message %s has no sender", s.MID); err != nil {
		return err
	}
	if err := fault.Check(len(s.To) > 0, where, "message %s has no recipients", s.MID); err != nil {
		return err
	}
	if err := fault.Check(s.Kind != "", where, "message %s has no kind", s.MID); err != nil {
		return err
	}
	if err := fault.Check(s.PUID >= 0, where, "message %s has puid %d", s.MID, s.PUID); err != nil {
		return err
	}
	if err := fault.Check(!s.Sent.IsZero(), where, "message %s has no send time", s.MID); err != nil {
		return err
	}
	// A conversation reference is all-or-nothing here for the same reason it is
	// in a stored message: an index with nothing to index into cannot be
	// rendered or ordered.
	if err := fault.Check((s.Convo == "") == (s.Index == 0), where,
		"message %s has convo %q and index %d", s.MID, s.Convo, s.Index); err != nil {
		return err
	}
	return nil
}

// Match reports whether the query selects s.
//
// now is required: a query may contain a relative predicate such as before=7d,
// and resolving that against a clock read inside this function would make
// matching non-deterministic and untestable.
func (q Query) Match(s Subject, now Now) (bool, error) {
	if q.root == nil {
		return false, fault.Internal{Where: "query.Match", Detail: "the zero Query matches nothing; it was never parsed"}
	}
	if err := s.Validate(); err != nil {
		return false, err
	}
	if now.Zero() {
		return false, fault.Internal{Where: "query.Match", Detail: "no reference instant was given"}
	}
	return q.root.eval(s, now)
}

func (n andNode) eval(s Subject, now Now) (bool, error) {
	left, err := n.left.eval(s, now)
	if err != nil || !left {
		return false, err
	}
	return n.right.eval(s, now)
}

func (n orNode) eval(s Subject, now Now) (bool, error) {
	left, err := n.left.eval(s, now)
	if err != nil {
		return false, err
	}
	if left {
		return true, nil
	}
	return n.right.eval(s, now)
}

func (n notNode) eval(s Subject, now Now) (bool, error) {
	inner, err := n.inner.eval(s, now)
	if err != nil {
		return false, err
	}
	return !inner, nil
}

func (n groupNode) eval(s Subject, now Now) (bool, error) { return n.inner.eval(s, now) }

func (n predNode) eval(s Subject, now Now) (bool, error) {
	switch n.field.shape {
	case shapeText:
		return n.compareText(n.textOf(s)), nil
	case shapeID:
		return n.compareID(n.idOf(s)), nil
	case shapeUser:
		return n.compareList([]string{s.From}), nil
	case shapeUserList:
		return n.compareList(n.usersOf(s)), nil
	case shapeNumber:
		return n.compareNumber(n.numberOf(s)), nil
	case shapeFlag:
		return n.flagOf(s) == n.value.flag, nil
	case shapeKind:
		return n.compareID(s.Kind), nil
	case shapeTime:
		return n.compareTime(s.Sent, now)
	default:
		return false, fault.Internal{Where: "query.predNode.eval", Detail: fmt.Sprintf("field %q has shape %d", n.field.name, int(n.field.shape))}
	}
}

func (n predNode) textOf(s Subject) string {
	switch n.field.name {
	case "subject":
		return s.Subject
	case "body":
		return s.Body
	case "title":
		return s.Title
	default:
		return ""
	}
}

func (n predNode) idOf(s Subject) string {
	switch n.field.name {
	case "mid":
		return s.MID
	case "convo":
		return s.Convo
	default:
		return ""
	}
}

func (n predNode) usersOf(s Subject) []string {
	switch n.field.name {
	case "to":
		return s.To
	case "cc":
		return s.CC
	case "any":
		out := make([]string, 0, 1+len(s.To)+len(s.CC))
		out = append(out, s.From)
		out = append(out, s.To...)
		return append(out, s.CC...)
	default:
		return nil
	}
}

func (n predNode) numberOf(s Subject) int {
	switch n.field.name {
	case "id":
		return s.PUID
	case "index":
		return s.Index
	default:
		return -1
	}
}

func (n predNode) flagOf(s Subject) bool {
	switch n.field.name {
	case "unread":
		return s.Unread
	case "archived":
		return s.Archived
	default:
		return false
	}
}

// compareText applies the operator to a single text value. Equality is
// case-sensitive because a subject is written deliberately; `~` folds case
// because it is the operator for "I half remember what this said".
func (n predNode) compareText(got string) bool {
	switch n.op {
	case opEQ:
		return got == n.value.text
	case opNE:
		return got != n.value.text
	case opContains:
		return strings.Contains(strings.ToLower(got), strings.ToLower(n.value.text))
	default:
		return false
	}
}

// compareID folds case, since identifiers are hex and kinds are lowercase
// words; neither has a meaningful uppercase form.
func (n predNode) compareID(got string) bool {
	got = strings.ToLower(got)
	switch n.op {
	case opEQ:
		return got == n.value.text
	case opNE:
		return got != n.value.text
	case opContains:
		return strings.Contains(got, n.value.text)
	default:
		return false
	}
}

// compareList applies the operator across a set of names.
//
// The sense of != is the one that surprises people if it is got wrong: `to!=bob`
// means "bob is not a recipient", not "some recipient is not bob". The latter
// would match nearly every message with more than one recipient, which is never
// what anybody wants.
func (n predNode) compareList(got []string) bool {
	hit := slices.ContainsFunc(got, func(name string) bool {
		folded := strings.ToLower(name)
		if n.op == opContains {
			return strings.Contains(folded, strings.ToLower(n.value.text))
		}
		return folded == n.value.text
	})
	if n.op == opNE {
		return !hit
	}
	return hit
}

func (n predNode) compareNumber(got int) bool {
	if n.op == opNE {
		return got != n.value.number
	}
	return got == n.value.number
}

// compareTime resolves before/after.
//
// A relative value is an age: before=7d selects mail older than seven days,
// after=7d selects mail from the last seven days. An absolute value is an
// instant, compared directly. Both are strict, so before=X and after=X
// partition the mailbox except for the exact instant X.
func (n predNode) compareTime(sent time.Time, now Now) (bool, error) {
	cut := n.value.at
	if n.value.relative {
		cut = now.At.at.Add(-n.value.span)
	}
	if cut.IsZero() {
		return false, fault.Internal{Where: "query.compareTime", Detail: "predicate has no instant to compare against"}
	}

	switch n.field.name {
	case "before":
		return sent.Before(cut), nil
	case "after":
		return sent.After(cut), nil
	default:
		return false, fault.Internal{Where: "query.compareTime", Detail: "field " + n.field.name + " is not a time"}
	}
}
