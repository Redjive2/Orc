package model

import (
	"fmt"
	"slices"
	"time"

	"orc/common/clock"
	"orc/common/fault"
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
