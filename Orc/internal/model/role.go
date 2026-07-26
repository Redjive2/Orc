package model

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
)

// MaxDescription bounds a role's description. Long enough for a sentence about
// what the job is, short enough that a card stays a card.
const MaxDescription = 200

// MaxRolePermissions bounds one role's permission set.
const MaxRolePermissions = 64

// Role is a job: an authority level, a description, and the permissions that go
// with it (Auth_Perm_Role.md, "Permissions are assigned to roles, which lets
// anyone with that role perform those actions").
//
// A role is mutable — `assign authority` and `assign permission` both change one
// — so it is a creation record with a journal folded onto it, exactly as a task
// is in Macmuffin. Every field below is therefore either fixed at creation
// (name, created) or the result of the fold.
type Role struct {
	name        Name
	created     time.Time
	authority   Authority
	description string
	permissions []Name
}

// NewRole builds a role as `orc new role` creates it.
func NewRole(name Name, authority Authority, description string, at time.Time) (Role, error) {
	if name.Zero() {
		return Role{}, fault.Internal{Where: "model.NewRole", Detail: "no name given"}
	}
	if authority.Zero() {
		return Role{}, fault.Internal{Where: "model.NewRole", Detail: "no authority given"}
	}
	if authority.IsOperator() {
		return Role{}, fault.Usage{Reason: fmt.Sprintf(
			"authority %d belongs to the operator alone; a role may go up to %d", Operator, MaxAuthority)}
	}
	description, err := CheckDescription(description)
	if err != nil {
		return Role{}, err
	}

	r := Role{name: name, created: clock.Normalise(at), authority: authority, description: description}
	if err := r.validate(); err != nil {
		return Role{}, err
	}
	return r, nil
}

// CheckDescription validates and trims a role's description.
//
// A description is required rather than optional: Reference.md takes one as a
// positional argument, and a fleet of roles called `engineer-2` with nothing
// saying what they are for is a fleet nobody can audit six months later.
func CheckDescription(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	switch {
	case !utf8.ValidString(s):
		return "", fault.Usage{Reason: "description is not valid UTF-8"}
	case s == "":
		return "", fault.Usage{Reason: "a role needs a description saying what the job is"}
	case utf8.RuneCountInString(s) > MaxDescription:
		return "", fault.Usage{Reason: fmt.Sprintf(
			"description is %d characters, over the %d limit", utf8.RuneCountInString(s), MaxDescription)}
	}
	for _, r := range s {
		// A newline would break the journal's one-event-per-line rule, and a
		// control character would let a description rewrite the screen it is
		// rendered into.
		if r == '\n' || r == '\r' || r < 0x20 {
			return "", fault.Usage{Reason: "description contains a control character"}
		}
	}
	return s, nil
}

func (r Role) validate() error {
	const where = "model.Role"
	if err := fault.Check(!r.name.Zero(), where, "name is unset"); err != nil {
		return err
	}
	if err := fault.Check(!r.authority.Zero(), where, "role %s has no authority", r.name); err != nil {
		return err
	}
	if err := fault.Check(!r.authority.IsOperator(), where, "role %s claims operator authority", r.name); err != nil {
		return err
	}
	if err := fault.Check(r.description != "", where, "role %s has no description", r.name); err != nil {
		return err
	}
	if err := fault.Check(len(r.permissions) <= MaxRolePermissions, where,
		"role %s holds %d permissions", r.name, len(r.permissions)); err != nil {
		return err
	}
	return fault.Check(!r.created.IsZero(), where, "created time is zero")
}

// Name returns the role's name.
func (r Role) Name() Name { return r.name }

// Created returns when the role was made.
func (r Role) Created() time.Time { return r.created }

// Authority returns the level this role asks for. It is a request, not a fact:
// an identity holding this role gets the lower of this and its boss's, which is
// derived in internal/authz.
func (r Role) Authority() Authority { return r.authority }

// Description returns what the job is.
func (r Role) Description() string { return r.description }

// Permissions returns the permission names this role grants, in name order.
func (r Role) Permissions() []Name { return slices.Clone(r.permissions) }

// Holds reports whether the role grants a permission.
func (r Role) Holds(name Name) bool { return ContainsName(r.permissions, name) }

// Zero reports whether the role was never constructed.
func (r Role) Zero() bool { return r.name.Zero() }

// RoleOp is one kind of change to a role.
type RoleOp string

// The role journal's vocabulary. A journal that meets an op outside this set is
// refused rather than skipped: guessing at what a newer Orc meant by an event is
// how an authority level silently reverts.
const (
	OpAuthority RoleOp = "authority"
	OpDescribe  RoleOp = "describe"
	OpPermit    RoleOp = "permit"
	OpUnpermit  RoleOp = "unpermit"
)

// Valid reports whether the op is one this build knows.
func (o RoleOp) Valid() bool {
	switch o {
	case OpAuthority, OpDescribe, OpPermit, OpUnpermit:
		return true
	default:
		return false
	}
}

// RoleOps lists the vocabulary, for tests that must be total.
func RoleOps() []RoleOp { return []RoleOp{OpAuthority, OpDescribe, OpPermit, OpUnpermit} }

// RoleEvent is one line of a role's journal: what changed, who changed it, and
// when. The zero value is not usable; construct one with the functions below.
type RoleEvent struct {
	op          RoleOp
	by          user.Name
	at          time.Time
	authority   Authority
	description string
	permission  Name
}

// SetAuthority is `orc assign authority <role> <authority>`.
func SetAuthority(by user.Name, at time.Time, authority Authority) (RoleEvent, error) {
	if authority.Zero() || authority.IsOperator() {
		return RoleEvent{}, fault.Usage{Reason: fmt.Sprintf(
			"a role's authority runs %d to %d", MinAuthority, MaxAuthority)}
	}
	return newRoleEvent(RoleEvent{op: OpAuthority, by: by, at: at, authority: authority})
}

// Describe rewrites a role's description.
func Describe(by user.Name, at time.Time, description string) (RoleEvent, error) {
	description, err := CheckDescription(description)
	if err != nil {
		return RoleEvent{}, err
	}
	return newRoleEvent(RoleEvent{op: OpDescribe, by: by, at: at, description: description})
}

// Permit is `orc assign permission <role> <permission>`.
func Permit(by user.Name, at time.Time, permission Name) (RoleEvent, error) {
	if permission.Zero() {
		return RoleEvent{}, fault.Internal{Where: "model.Permit", Detail: "no permission named"}
	}
	return newRoleEvent(RoleEvent{op: OpPermit, by: by, at: at, permission: permission})
}

// Unpermit takes a permission back off a role.
func Unpermit(by user.Name, at time.Time, permission Name) (RoleEvent, error) {
	if permission.Zero() {
		return RoleEvent{}, fault.Internal{Where: "model.Unpermit", Detail: "no permission named"}
	}
	return newRoleEvent(RoleEvent{op: OpUnpermit, by: by, at: at, permission: permission})
}

func newRoleEvent(e RoleEvent) (RoleEvent, error) {
	if e.by.Zero() {
		return RoleEvent{}, fault.Internal{Where: "model.RoleEvent", Detail: "no actor named"}
	}
	if e.at.IsZero() {
		return RoleEvent{}, fault.Internal{Where: "model.RoleEvent", Detail: "no timestamp"}
	}
	if !e.op.Valid() {
		return RoleEvent{}, fault.Internal{Where: "model.RoleEvent", Detail: "unknown op " + string(e.op)}
	}
	e.at = clock.Normalise(e.at)
	return e, nil
}

// Accessors. The journal codec in internal/store reads these; nothing else does.

func (e RoleEvent) Op() RoleOp           { return e.op }
func (e RoleEvent) By() user.Name        { return e.by }
func (e RoleEvent) At() time.Time        { return e.at }
func (e RoleEvent) Authority() Authority { return e.authority }
func (e RoleEvent) Description() string  { return e.description }
func (e RoleEvent) Permission() Name     { return e.permission }
func (e RoleEvent) Zero() bool           { return e.op == "" }

// With applies an event, returning the role as it stands afterwards.
//
// It is a pure function of the role and the event, so the fold's rules can be
// tested and fuzzed without a filesystem. An event that cannot apply is an
// error rather than a silent no-op: the journal is meant to contain only
// transitions that happened.
func (r Role) With(e RoleEvent) (Role, error) {
	if r.Zero() {
		return Role{}, fault.Internal{Where: "model.Role.With", Detail: "role is unconstructed"}
	}
	if e.Zero() {
		return Role{}, fault.Internal{Where: "model.Role.With", Detail: "event is unconstructed"}
	}

	switch e.op {
	case OpAuthority:
		r.authority = e.authority

	case OpDescribe:
		r.description = e.description

	case OpPermit:
		if r.Holds(e.permission) {
			// Idempotent rather than an error: two agents assigning the same
			// permission is a race with an agreed outcome, and refusing the
			// second would report a failure where nothing is wrong.
			return r, nil
		}
		if len(r.permissions) >= MaxRolePermissions {
			return Role{}, fault.Conflict{Path: r.name.String(), Reason: fmt.Sprintf(
				"a role may hold %d permissions", MaxRolePermissions)}
		}
		r.permissions = append(slices.Clone(r.permissions), e.permission)
		slices.SortFunc(r.permissions, Name.Compare)

	case OpUnpermit:
		if !r.Holds(e.permission) {
			return r, nil
		}
		kept := make([]Name, 0, len(r.permissions))
		for _, p := range r.permissions {
			if !p.Equal(e.permission) {
				kept = append(kept, p)
			}
		}
		r.permissions = kept

	default:
		return Role{}, fault.Internal{Where: "model.Role.With", Detail: "unhandled op " + string(e.op)}
	}

	if err := r.validate(); err != nil {
		return Role{}, err
	}
	return r, nil
}
