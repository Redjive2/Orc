// Package task holds Macmuffin's domain values: the name of a task, the two
// scores it carries, the health signal it reports, and the subtasks it breaks
// into.
//
// Everything here is a value with unexported fields and a validating
// constructor. A zero Name, Score, or Status cannot be produced outside this
// package, so no other package has to ask whether the thing it was handed makes
// sense — the type is the answer.
//
// Nothing in this package touches the filesystem, the clock, or the network.
package task

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"orc/common/fault"
)

// Bounds on a name. The upper bound is well under any filesystem's, since a
// name becomes one path element among several.
const (
	MinNameLen = 1
	MaxNameLen = 80
)

// reserved names would be ambiguous as a path element or as an argument.
//
// `pool` and `all` are commands and flags a caller could plausibly type where a
// name is expected; the dots are path traversal; `none` is what an empty value
// prints as, and a task that answered to it could never be told apart from one
// that was not there.
var reserved = map[string]bool{
	".": true, "..": true,
	"all": true, "pool": true, "none": true, "any": true,
}

// Name is a validated, normalised task handle. The zero value is not usable;
// construct one with ParseName.
type Name struct {
	s string
}

// ParseName normalises and validates a task name.
//
// Normalisation is trim, lowercase, then every run of whitespace, underscores,
// and dashes collapsed to a single dash. So `muff info "Fix The Parser"` finds
// `fix-the-parser`, which is what makes a name typeable from memory rather than
// copied from a listing.
//
// The plan asks for NFC normalisation as well. That needs golang.org/x/text,
// and every Orc tool is stdlib only, so instead names are restricted to ASCII —
// which makes normalisation total rather than approximate, and sidesteps the
// question of whether two names that look identical are the same task. A
// rejected name is a message the caller can act on; a name that silently became
// a different task is not.
func ParseName(raw string) (Name, error) {
	if !utf8.ValidString(raw) {
		return Name{}, fault.Usage{Reason: "task name is not valid UTF-8"}
	}

	// Checked against the raw spelling, before normalisation, because
	// normalisation would strip the leading dashes and turn `--force` into the
	// perfectly valid name `force`. A mistyped or misplaced flag must be a
	// refusal, never a lookup that quietly succeeds against the wrong task.
	if strings.HasPrefix(strings.TrimSpace(raw), "-") {
		return Name{}, fault.Usage{Reason: fmt.Sprintf(
			"%q looks like an option, not a task name; task names start with a letter or digit", raw)}
	}

	s := normalise(raw)

	switch {
	case s == "":
		return Name{}, fault.Usage{Reason: fmt.Sprintf("task name %q has nothing usable in it", raw)}
	case len(s) < MinNameLen:
		return Name{}, fault.Usage{Reason: fmt.Sprintf("task name %q is shorter than %d characters", raw, MinNameLen)}
	case len(s) > MaxNameLen:
		return Name{}, fault.Usage{Reason: fmt.Sprintf(
			"task name %q normalises to %d characters, over the %d limit", raw, len(s), MaxNameLen)}
	case reserved[s]:
		return Name{}, fault.Usage{Reason: fmt.Sprintf("task name %q is reserved", s)}
	}

	for i, r := range s {
		if !allowed(r) {
			return Name{}, fault.Usage{Reason: fmt.Sprintf(
				"task name %q contains %q at position %d; use letters, digits, dots, and dashes", raw, r, i+1)}
		}
	}
	if !isAlphanumeric(rune(s[0])) {
		// This also rejects anything that would be read as a flag, since a name
		// that survived normalisation starting with `-` could not be told from
		// an option at the command line.
		return Name{}, fault.Usage{Reason: fmt.Sprintf("task name %q must start with a letter or digit", raw)}
	}

	n := Name{s: s}
	if err := n.validate(); err != nil {
		return Name{}, err
	}
	return n, nil
}

// normalise lowercases, and folds every separator run into one dash.
//
// Underscores, spaces, and dashes are all treated as the same thing, because a
// caller who types one of them meant a word break and should not have to
// remember which the task was created with.
func normalise(raw string) string {
	var b strings.Builder
	b.Grow(len(raw))

	pendingSep := false
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		if isSeparator(r) {
			// Held rather than written, so a run collapses and a trailing run
			// disappears instead of leaving a dangling dash.
			pendingSep = b.Len() > 0
			continue
		}
		if pendingSep {
			b.WriteByte('-')
			pendingSep = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isSeparator(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '_' || r == '-'
}

func allowed(r rune) bool { return isAlphanumeric(r) || r == '.' || r == '-' }

func isAlphanumeric(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

// validate re-derives what ParseName is expected to guarantee. It runs on every
// constructed Name so a defect in ParseName surfaces here rather than as a path
// traversal much later.
func (n Name) validate() error {
	const where = "task.Name"
	if err := fault.Check(n.s != "", where, "name is empty"); err != nil {
		return err
	}
	if err := fault.Check(len(n.s) <= MaxNameLen, where, "name %q is %d bytes", n.s, len(n.s)); err != nil {
		return err
	}
	if err := fault.Check(!reserved[n.s], where, "name %q is reserved", n.s); err != nil {
		return err
	}
	if err := fault.Check(n.s == strings.ToLower(n.s), where, "name %q is not lowercased", n.s); err != nil {
		return err
	}
	for _, r := range n.s {
		if err := fault.Check(allowed(r), where, "name %q contains %q", n.s, r); err != nil {
			return err
		}
	}
	return fault.Check(isAlphanumeric(rune(n.s[0])), where, "name %q does not start alphanumerically", n.s)
}

// String returns the normalised name.
func (n Name) String() string { return n.s }

// Zero reports whether the name was never constructed.
func (n Name) Zero() bool { return n.s == "" }

// Compare orders names, so a rendered board is stable.
func (n Name) Compare(other Name) int { return strings.Compare(n.s, other.s) }

// Equal reports whether two names denote the same task.
func (n Name) Equal(other Name) bool { return n.s == other.s }

// Renamed reports whether normalisation changed the caller's spelling.
//
// A command echoes this back in its header when it is true, so the mapping from
// what was typed to what was found is never invisible: an agent that asked for
// "Fix The Parser" and got "fix-the-parser" should be able to see why.
func (n Name) Renamed(raw string) bool { return n.s != raw }
