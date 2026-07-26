package model

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
)

// MaxPatterns bounds one permission. A permission with more clauses than this is
// not a permission anybody can reason about, and the limit turns a runaway
// script into a message.
const MaxPatterns = 64

// Permission is a named, composable set of clauses with a floor: "only those at
// or above its permission level can have that permission" (Auth_Perm_Role.md).
//
// A Permission is **immutable**. Nothing in Docs/Orc/Reference.md changes one
// after creation — a permission is created, assigned, and removed — so there is
// no journal beside it and no way for its meaning to drift under the roles that
// hold it. Widening a permission is `orc new permission` under a new name, which
// is a change somebody has to make deliberately and which shows up in every card
// that lists it.
type Permission struct {
	name     Name
	floor    Authority
	patterns []Pattern
	created  time.Time
}

// NewPermission builds a permission.
//
// The patterns are sorted and copied, so two permissions built from the same
// clauses in different orders are the same permission, and no caller can mutate
// one afterwards by holding on to the slice it passed in.
func NewPermission(name Name, floor Authority, patterns []Pattern, at time.Time) (Permission, error) {
	if name.Zero() {
		return Permission{}, fault.Internal{Where: "model.NewPermission", Detail: "no name given"}
	}
	if floor.Zero() {
		return Permission{}, fault.Internal{Where: "model.NewPermission", Detail: "no floor given"}
	}
	if floor.IsOperator() {
		return Permission{}, fault.Usage{Reason: fmt.Sprintf(
			"a floor of %d would put %s out of reach of every role; use %d at most",
			Operator, name, MaxAuthority)}
	}
	if len(patterns) == 0 {
		return Permission{}, fault.Usage{Reason: fmt.Sprintf(
			"permission %s needs at least one pattern, as in read(Anno/**)", name)}
	}
	if len(patterns) > MaxPatterns {
		return Permission{}, fault.Usage{Reason: fmt.Sprintf(
			"permission %s has %d patterns, over the %d limit", name, len(patterns), MaxPatterns)}
	}

	sorted := slices.Clone(patterns)
	slices.SortFunc(sorted, Pattern.Compare)
	for i, p := range sorted {
		if p.Zero() {
			return Permission{}, fault.Internal{Where: "model.NewPermission", Detail: "unconstructed pattern"}
		}
		if i > 0 && sorted[i-1].Equal(p) {
			return Permission{}, fault.Usage{Reason: fmt.Sprintf("permission %s repeats %s", name, p)}
		}
	}

	p := Permission{name: name, floor: floor, patterns: sorted, created: clock.Normalise(at)}
	if err := p.validate(); err != nil {
		return Permission{}, err
	}
	return p, nil
}

func (p Permission) validate() error {
	const where = "model.Permission"
	if err := fault.Check(!p.name.Zero(), where, "name is unset"); err != nil {
		return err
	}
	if err := fault.Check(!p.floor.Zero(), where, "floor is unset"); err != nil {
		return err
	}
	if err := fault.Check(len(p.patterns) > 0, where, "permission %s has no patterns", p.name); err != nil {
		return err
	}
	return fault.Check(!p.created.IsZero(), where, "created time is zero")
}

// Name returns the permission's name.
func (p Permission) Name() Name { return p.name }

// Floor returns the minimum authority a holder must have.
func (p Permission) Floor() Authority { return p.floor }

// Created returns when the permission was made.
func (p Permission) Created() time.Time { return p.created }

// Zero reports whether the permission was never constructed.
func (p Permission) Zero() bool { return p.name.Zero() }

// Patterns returns the clauses, in render order. The slice is a copy: a
// permission is immutable, and a caller holding the backing array would be able
// to widen one after every check that has already passed.
func (p Permission) Patterns() []Pattern { return slices.Clone(p.patterns) }

// String renders the permission the way a card lists it.
func (p Permission) String() string { return p.name.String() }

// Load returns the largest spawn budget this permission carries, and reports
// whether it carries one at all.
func (p Permission) Load() (int, bool) {
	best, found := 0, false
	for _, pattern := range p.patterns {
		if pattern.Kind() == KindSpawn {
			found = true
			if pattern.Load() > best {
				best = pattern.Load()
			}
		}
	}
	return best, found
}

// PermissionOp is what an event does to a permission.
//
// There is one, and the plan said there would be none: §13 decided a permission
// was immutable because nothing mutated it, so a journal would be "a file that is
// always empty and a fold that can never run". `orc edit permission` invalidates
// that premise rather than the reasoning — the moment something mutates one, the
// journal §3 reserved for every entity is what it should have.
type PermissionOp string

// OpAmend replaces a permission's floor and clauses in one step.
//
// One op rather than two, because a permission is edited as a whole: the form
// that changes it submits both halves, and two events would let a floor and the
// clauses it guards disagree for the width of a crash.
const OpAmend PermissionOp = "amend"

// PermissionEvent is one change to a permission.
type PermissionEvent struct {
	op       PermissionOp
	by       user.Name
	at       time.Time
	floor    Authority
	patterns []Pattern
}

// Amend is `orc edit permission`.
func Amend(by user.Name, at time.Time, floor Authority, patterns []Pattern) (PermissionEvent, error) {
	if floor.Zero() {
		return PermissionEvent{}, fault.Usage{Reason: "a permission needs a floor between 1 and 100"}
	}
	if len(patterns) == 0 {
		return PermissionEvent{}, fault.Usage{Reason: "a permission with no clauses permits nothing; delete it instead"}
	}
	if len(patterns) > MaxPatterns {
		return PermissionEvent{}, fault.Usage{Reason: fmt.Sprintf(
			"a permission may carry %d clauses, not %d", MaxPatterns, len(patterns))}
	}
	if by.Zero() {
		return PermissionEvent{}, fault.Internal{Where: "model.Amend", Detail: "no actor named"}
	}
	if at.IsZero() {
		return PermissionEvent{}, fault.Internal{Where: "model.Amend", Detail: "no timestamp"}
	}
	return PermissionEvent{op: OpAmend, by: by, at: at, floor: floor, patterns: slices.Clone(patterns)}, nil
}

// Op returns what the event does.
func (e PermissionEvent) Op() PermissionOp { return e.op }

// By returns who made the change.
func (e PermissionEvent) By() user.Name { return e.by }

// At returns when.
func (e PermissionEvent) At() time.Time { return e.at }

// Floor returns the new floor.
func (e PermissionEvent) Floor() Authority { return e.floor }

// Patterns returns a copy of the new clauses.
func (e PermissionEvent) Patterns() []Pattern { return slices.Clone(e.patterns) }

// Zero reports whether the event is the empty one, which a decision returns when
// it decided nothing needed doing.
func (e PermissionEvent) Zero() bool { return e.op == "" }

// With folds an event onto a permission.
//
// The name and the creation time never change: renaming would break every role
// that holds it and every card that lists it, which is what `orc new permission`
// under another name is for.
func (p Permission) With(e PermissionEvent) (Permission, error) {
	if e.Zero() {
		return p, nil
	}
	switch e.op {
	case OpAmend:
		next := Permission{name: p.name, floor: e.floor, patterns: slices.Clone(e.patterns), created: p.created}
		slices.SortFunc(next.patterns, func(a, b Pattern) int { return strings.Compare(a.String(), b.String()) })
		next.patterns = slices.CompactFunc(next.patterns, func(a, b Pattern) bool { return a.String() == b.String() })
		if err := next.validate(); err != nil {
			return Permission{}, err
		}
		return next, nil
	default:
		return Permission{}, fault.Internal{
			Where: "model.Permission.With", Detail: fmt.Sprintf("unknown op %q", e.op)}
	}
}
