package query

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/mailman/internal/mail"
)

// shape is what a field's values look like, which decides both how a value is
// validated at parse time and which operators are meaningful on it.
type shape int

const (
	shapeText shape = iota
	shapeUser
	shapeUserList
	shapeNumber
	shapeFlag
	shapeTime
	shapeKind
	shapeID
)

// op is a comparison.
type op int

const (
	opEQ op = iota
	opNE
	opContains
)

func (o op) String() string {
	switch o {
	case opEQ:
		return "="
	case opNE:
		return "!="
	case opContains:
		return "~"
	default:
		return fmt.Sprintf("op(%d)", int(o))
	}
}

// field describes one queryable attribute of a message.
type field struct {
	name  string
	shape shape
	help  string
}

// fields is the complete, ordered vocabulary. It is ordered so error messages
// list the options the same way every time.
var fields = []field{
	{"id", shapeNumber, "the persistent identifier shown in the inbox"},
	{"mid", shapeID, "the full message identifier"},
	{"kind", shapeKind, `"mail" or "cc"`},
	{"from", shapeUser, "the sender"},
	{"to", shapeUserList, "a direct recipient"},
	{"cc", shapeUserList, "a copied recipient"},
	{"any", shapeUserList, "the sender or any recipient"},
	{"subject", shapeText, "the subject line"},
	{"body", shapeText, "the message body"},
	{"convo", shapeID, "the conversation identifier"},
	{"title", shapeText, "the conversation title"},
	{"index", shapeNumber, "position within the conversation"},
	{"unread", shapeFlag, `"true" or "false"`},
	{"archived", shapeFlag, `"true" or "false"`},
	{"before", shapeTime, "a timestamp or an age such as 7d"},
	{"after", shapeTime, "a timestamp or an age such as 7d"},
}

func lookupField(name string) (field, bool) {
	folded := strings.ToLower(name)
	for _, f := range fields {
		if f.name == folded {
			return f, true
		}
	}
	return field{}, false
}

// FieldNames lists the vocabulary, for help text and error messages.
func FieldNames() []string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = f.name
	}
	return out
}

// allows reports whether an operator is meaningful on this shape.
//
// Rejecting `index~3` at parse time rather than evaluating it to false is the
// difference between a caller being told their query is wrong and a caller
// believing that nothing matched.
func (s shape) allows(o op) bool {
	switch s {
	case shapeText:
		return true // all three
	case shapeUser, shapeUserList, shapeID:
		return o == opEQ || o == opNE || o == opContains
	case shapeNumber, shapeKind:
		return o == opEQ || o == opNE
	case shapeFlag, shapeTime:
		return o == opEQ
	default:
		return false
	}
}

func (s shape) describe() string {
	switch s {
	case shapeNumber:
		return "a whole number"
	case shapeFlag:
		return `"true" or "false"`
	case shapeTime:
		return "a timestamp or an age such as 7d"
	case shapeKind:
		return `"mail" or "cc"`
	case shapeID:
		return "an identifier"
	default:
		return "text"
	}
}

// value is a parsed, validated predicate operand. Exactly one of its members is
// meaningful, chosen by the field's shape — validating at parse time means
// evaluation can never fail on a malformed value.
type value struct {
	text   string
	number int
	flag   bool
	at     time.Time
	span   time.Duration
	// relative distinguishes "before=7d" (an age, resolved against the clock at
	// match time) from "before=2026-01-01T…" (a fixed instant).
	relative bool
}

// parseValue validates raw against the field's shape.
func parseValue(raw string, f field, q string, col int) (value, error) {
	bad := func(format string, args ...any) (value, error) {
		return value{}, fault.Query{Query: q, Col: col, Reason: fmt.Sprintf(format, args...)}
	}

	if len(raw) > MaxValue {
		return bad("value is %d bytes, limit is %d", len(raw), MaxValue)
	}

	switch f.shape {
	case shapeText:
		if raw == "" {
			return bad("%s needs a value; write %s=\"\" is not meaningful", f.name, f.name)
		}
		return value{text: raw}, nil

	case shapeUser, shapeUserList:
		// A user-valued field is compared against normalised names, so the query
		// value is normalised the same way. Under `~` the value is a fragment
		// rather than a name, so only the folding applies.
		n, err := user.Parse(raw)
		if err != nil {
			return value{text: strings.ToLower(strings.TrimSpace(raw))}, nil
		}
		return value{text: n.String()}, nil

	case shapeID:
		if raw == "" {
			return bad("%s needs an identifier", f.name)
		}
		return value{text: strings.ToLower(raw)}, nil

	case shapeNumber:
		n, err := parseNumber(raw)
		if err != nil {
			return bad("%s needs %s, not %q", f.name, f.shape.describe(), raw)
		}
		return value{number: n}, nil

	case shapeFlag:
		switch strings.ToLower(raw) {
		case "true", "yes", "1":
			return value{flag: true}, nil
		case "false", "no", "0":
			return value{flag: false}, nil
		default:
			return bad("%s needs %s, not %q", f.name, f.shape.describe(), raw)
		}

	case shapeKind:
		k, err := mail.ParseKind(strings.ToLower(raw))
		if err != nil {
			return bad("%s needs %s, not %q", f.name, f.shape.describe(), raw)
		}
		return value{text: k.String()}, nil

	case shapeTime:
		if at, err := clock.Parse(raw); err == nil {
			return value{at: at}, nil
		}
		if span, err := clock.ParseSpan(raw); err == nil {
			return value{span: span, relative: true}, nil
		}
		return bad("%s needs %s, not %q", f.name, f.shape.describe(), raw)

	default:
		return value{}, fault.Internal{Where: "query.parseValue", Detail: fmt.Sprintf("field %q has shape %d", f.name, int(f.shape))}
	}
}

// parseNumber reads a non-negative whole number without accepting the forms
// strconv would tolerate — a leading plus, surrounding space, or an underscore
// separator — since each of those in a query is a mistake rather than a style.
func parseNumber(raw string) (int, error) {
	if raw == "" {
		return 0, fault.Query{Reason: "empty number"}
	}
	n := 0
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c < '0' || c > '9' {
			return 0, fault.Query{Reason: "not a number"}
		}
		n = n*10 + int(c-'0')
		if n > 1<<40 {
			return 0, fault.Query{Reason: "number is too large"}
		}
	}
	return n, nil
}

// suggest offers field names close to an unknown one. An unknown field is
// always an error, never a predicate that quietly matches nothing — that is how
// a `prune` typo becomes a no-op and a `read` typo becomes a lie.
func suggest(unknown string) []string {
	folded := strings.ToLower(unknown)
	var near []string
	for _, f := range fields {
		if strings.HasPrefix(f.name, folded) || strings.HasPrefix(folded, f.name) || editClose(folded, f.name) {
			near = append(near, f.name)
		}
	}
	slices.Sort(near)
	return slices.Compact(near)
}

// editClose reports whether two names are within one edit of each other. It is
// a deliberately cheap check: the suggestion list is a courtesy, and the full
// vocabulary is printed alongside it either way.
func editClose(a, b string) bool {
	if a == b {
		return true
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	if len(b)-len(a) > 1 {
		return false
	}
	if len(a) == len(b) {
		diff := 0
		for i := range a {
			if a[i] != b[i] {
				diff++
			}
		}
		return diff == 1
	}
	// b is exactly one rune longer: check it is a is with one insertion.
	for i := range len(a) + 1 {
		if a[:i] == b[:i] && a[i:] == b[i+1:] {
			return true
		}
	}
	return false
}
