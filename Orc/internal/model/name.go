// Package model holds Orc's domain values: the name of a role or a permission,
// an authority level, a command pattern, and the three records that make up the
// fleet — permissions, roles, and identities.
//
// Everything here is a value with unexported fields and a validating
// constructor. A zero Name, Authority, or Pattern cannot be produced outside
// this package, so no other package has to ask whether what it was handed makes
// sense — the type is the answer.
//
// Nothing in this package touches the filesystem, the clock, or the network. In
// particular it holds no *effective* authority or permission: those are derived
// from the whole tree and live in internal/authz, because a value that looks
// like a fact but is only true relative to a boss is the one thing this model
// must not offer.
package model

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
	MaxNameLen = 64
)

// reserved names would be ambiguous as a path element or as an argument.
//
// The dots are path traversal. `all`, `any`, and `none` are what an empty or
// wildcard value prints as, and a role that answered to one of them could never
// be told apart from the absence of a role. `identity`, `role`, and `permission`
// are the nouns `orc new` takes, so a role called `role` would make
// `orc remove role role` unreadable.
var reserved = map[string]bool{
	".": true, "..": true,
	"all": true, "any": true, "none": true,
	"identity": true, "role": true, "permission": true, "authority": true,
}

// Name is a validated, normalised role or permission name. The zero value is
// not usable; construct one with ParseName.
//
// Identities are *not* named with this type: they are named with
// orc/common/user.Name, because an identity's name is also its mailbox name, and
// a name Orc accepted that Mailman would refuse is an identity that cannot be
// written to. One validation, shared, is what stops that.
type Name struct {
	s string
}

// ParseName normalises and validates a role or permission name.
//
// Normalisation is trim then lowercase — deliberately less than Macmuffin does
// to a task name, which also folds separators. A task name is typed from memory
// and wants to be forgiving; a role name is written once and then referenced by
// other records, so `Engineer` and `engineer` being the same role is worth
// having and `engineer-2` quietly becoming `engineer.2` is not.
func ParseName(raw string) (Name, error) {
	if !utf8.ValidString(raw) {
		return Name{}, fault.Usage{Reason: "name is not valid UTF-8"}
	}

	// Checked against the raw spelling, before normalisation, so a misplaced
	// flag is a refusal rather than a lookup that quietly succeeds.
	if strings.HasPrefix(strings.TrimSpace(raw), "-") {
		return Name{}, fault.Usage{Reason: fmt.Sprintf(
			"%q looks like an option, not a name; names start with a letter or digit", raw)}
	}

	s := strings.ToLower(strings.TrimSpace(raw))

	switch {
	case s == "":
		return Name{}, fault.Usage{Reason: "name is empty"}
	case len(s) < MinNameLen:
		return Name{}, fault.Usage{Reason: fmt.Sprintf("name %q is shorter than %d characters", raw, MinNameLen)}
	case len(s) > MaxNameLen:
		return Name{}, fault.Usage{Reason: fmt.Sprintf("name %q is longer than %d characters", raw, MaxNameLen)}
	case reserved[s]:
		return Name{}, fault.Usage{Reason: fmt.Sprintf("name %q is reserved", s)}
	}

	for i, r := range s {
		if !allowed(r) {
			return Name{}, fault.Usage{Reason: fmt.Sprintf(
				"name %q contains %q at position %d; use letters, digits, and . _ -", raw, r, i+1)}
		}
	}
	if !isAlphanumeric(rune(s[0])) {
		return Name{}, fault.Usage{Reason: fmt.Sprintf("name %q must start with a letter or digit", raw)}
	}

	n := Name{s: s}
	if err := n.validate(); err != nil {
		return Name{}, err
	}
	return n, nil
}

func allowed(r rune) bool {
	return isAlphanumeric(r) || r == '.' || r == '_' || r == '-'
}

func isAlphanumeric(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

// validate re-derives what ParseName is expected to guarantee. It runs on every
// constructed Name so a defect in ParseName surfaces here rather than as a path
// traversal much later.
func (n Name) validate() error {
	const where = "model.Name"
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

// Compare orders names, so a rendered list is stable.
func (n Name) Compare(other Name) int { return strings.Compare(n.s, other.s) }

// Equal reports whether two names denote the same thing.
func (n Name) Equal(other Name) bool { return n.s == other.s }

// Names renders a list for storage or display.
func Names(list []Name) []string {
	out := make([]string, len(list))
	for i, n := range list {
		out[i] = n.s
	}
	return out
}

// ContainsName reports whether list includes name.
func ContainsName(list []Name, name Name) bool {
	for _, n := range list {
		if n.s == name.s {
			return true
		}
	}
	return false
}
