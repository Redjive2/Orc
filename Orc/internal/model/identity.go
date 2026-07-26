package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
)

// MaxGrants bounds the direct grants one identity can hold at once. Grants are
// meant to be exceptional and temporary (§2.5 of the plan); a hundred of them is
// a role nobody got round to creating.
const MaxGrants = 32

// Identity is a persistent, single agent: an account, a place in the tree, at
// most one role, and whatever has been granted to it directly.
//
// Two things are deliberately *not* here. There is no effective authority and no
// effective permission set, because both depend on the boss chain and belong to
// internal/authz — a value that looked like an identity's authority but was only
// true relative to a boss is the mistake this split exists to prevent. And there
// is no session, because liveness is not state: an identity with no Claude
// session is an ordinary identity that is not thinking right now.
type Identity struct {
	name    user.Name
	id      string
	created time.Time

	// boss is zero for exactly one identity: the operator, who is the root of
	// the tree. Everyone else has one, and every chain terminates there.
	boss user.Name

	role   Name
	grants []Grant

	// employed is worklist membership, which *is* state: "it holds all currently
	// employed identities, and automatically populates them with their requested
	// models and efforts". So employment survives a crash and a reboot, and the
	// session it implies does not.
	//
	// model and effort are that request. They outlive a `fire`, so re-employing an
	// identity remembers what it was running rather than resetting it to the
	// default — an agent that was deliberately put on opus should not quietly come
	// back as a sonnet.
	employed bool
	model    Model
	effort   Effort
}

// NewIdentity builds an identity as `orc new identity` creates it.
//
// boss is the caller: Reference.md's `new identity <name>` takes no parent, and
// Auth_Perm_Role.md says every agent "is a subagent of either the user or another
// subagent", so whoever ran the command is the boss. A zero boss is the
// operator, which only bootstrap has any reason to build.
func NewIdentity(name user.Name, id string, boss user.Name, at time.Time) (Identity, error) {
	if name.Zero() {
		return Identity{}, fault.Internal{Where: "model.NewIdentity", Detail: "no name given"}
	}
	if err := CheckID(id); err != nil {
		return Identity{}, err
	}
	if !boss.Zero() && boss.String() == name.String() {
		return Identity{}, fault.Usage{Reason: fmt.Sprintf("%s cannot be its own boss", name)}
	}

	i := Identity{name: name, id: id, created: clock.Normalise(at), boss: boss}
	if err := i.validate(); err != nil {
		return Identity{}, err
	}
	return i, nil
}

// NewID mints an identity id: microseconds since the epoch in hex, then eight
// random hex digits.
//
// The shape is Mailman's message id, for the same two reasons: the time part
// makes a directory listing sort into creation order, and the random part means
// two identities created in the same microsecond on the same machine still
// differ. entropy is injected so a test can pin an id.
func NewID(at time.Time, entropy io.Reader) (string, error) {
	if entropy == nil {
		entropy = rand.Reader
	}
	raw := make([]byte, 4)
	if _, err := io.ReadFull(entropy, raw); err != nil {
		return "", fault.IO{Op: "read entropy for", Path: "a new identity id", Err: err}
	}
	micros := clock.Normalise(at).UnixMicro()
	if micros <= 0 {
		return "", fault.Internal{Where: "model.NewID", Detail: "timestamp is before the epoch"}
	}
	id := fmt.Sprintf("%x-%s", micros, hex.EncodeToString(raw))
	if err := CheckID(id); err != nil {
		return "", fault.Internal{Where: "model.NewID", Detail: "minted an id that fails validation: " + err.Error()}
	}
	return id, nil
}

// CheckID validates an id's shape. It is checked on the way in and on the way
// out, so a hand-edited record cannot introduce an id that is also a path.
func CheckID(id string) error {
	stamp, random, found := strings.Cut(id, "-")
	if !found || stamp == "" || len(random) != 8 {
		return fault.Parse{Reason: fmt.Sprintf("identity id %q is not <hex-micros>-<8 hex digits>", id)}
	}
	for _, part := range []string{stamp, random} {
		for _, r := range part {
			if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
				return fault.Parse{Reason: fmt.Sprintf("identity id %q is not hexadecimal", id)}
			}
		}
	}
	return nil
}

func (i Identity) validate() error {
	const where = "model.Identity"
	if err := fault.Check(!i.name.Zero(), where, "name is unset"); err != nil {
		return err
	}
	if err := fault.Check(CheckID(i.id) == nil, where, "identity %s has a bad id %q", i.name, i.id); err != nil {
		return err
	}
	if err := fault.Check(!i.created.IsZero(), where, "created time is zero"); err != nil {
		return err
	}
	if err := fault.Check(i.boss.Zero() || i.boss.String() != i.name.String(), where,
		"identity %s is its own boss", i.name); err != nil {
		return err
	}
	return fault.Check(len(i.grants) <= MaxGrants, where, "identity %s holds %d grants", i.name, len(i.grants))
}

// Name returns whose identity this is. It is also the mailbox name, which is why
// it is a user.Name rather than a model.Name.
func (i Identity) Name() user.Name { return i.name }

// ID returns the identity's immutable id.
func (i Identity) ID() string { return i.id }

// Created returns when the identity was made.
func (i Identity) Created() time.Time { return i.created }

// Boss returns the identity above this one. It is zero for the operator.
func (i Identity) Boss() user.Name { return i.boss }

// IsOperator reports whether this is the root of the tree.
func (i Identity) IsOperator() bool { return i.boss.Zero() }

// Role returns the identity's role, which is zero until one is assigned. An
// identity with no role holds no permissions at all — hired, with no job yet.
func (i Identity) Role() Name { return i.role }

// Grants returns the direct grants, live or lapsed, in permission-name order.
// Whether one is still in force is a question about now, so it is Live's.
func (i Identity) Grants() []Grant { return slices.Clone(i.grants) }

// LiveGrants returns the grants still in force at the given instant, for an
// identity whose current session is session (empty when it is not populated).
func (i Identity) LiveGrants(now time.Time, session string) []Grant {
	out := make([]Grant, 0, len(i.grants))
	for _, g := range i.grants {
		if g.Live(now, session) {
			out = append(out, g)
		}
	}
	return out
}

// Zero reports whether the identity was never constructed.
func (i Identity) Zero() bool { return i.name.Zero() }

// Employed reports whether the identity is on the worklist. It is not the same
// question as "is it populated": a crashed session leaves an identity employed and
// unpopulated, which is exactly the state `orc tend` exists to fix.
func (i Identity) Employed() bool { return i.employed }

// Model and Effort are the load the identity was employed at, remembered across a
// `fire` so that re-employing does not silently downgrade it. Both are unset for
// an identity that has never been employed.
func (i Identity) Model() Model   { return i.model }
func (i Identity) Effort() Effort { return i.effort }

// Load is what this identity's session costs, or zero when it is not employed.
//
// An identity that is employed but whose session has died still costs its load:
// the worklist says it should be thinking, and `tend` is going to restart it. A
// budget that emptied itself every time a session crashed would let a flapping
// fleet employ without limit.
func (i Identity) Load() int {
	if !i.employed {
		return 0
	}
	return SessionLoad(i.model, i.effort)
}

// IdentityOp is one kind of change to an identity.
type IdentityOp string

// The identity journal's vocabulary.
//
// Employment is here; *population* is not, and the difference is the design.
// Being on the worklist is a decision somebody made and it belongs in the journal;
// having a live session is a fact about a process, and it lives in
// session/session.json where a crash can take it away without rewriting history.
const (
	OpRole   IdentityOp = "role"
	OpMove   IdentityOp = "move"
	OpGrant  IdentityOp = "grant"
	OpRevoke IdentityOp = "revoke"
	OpEmploy IdentityOp = "employ"
	OpFire   IdentityOp = "fire"
	// OpModel changes what an identity thinks with, without employing or firing
	// it. It is separate from OpEmploy because retuning an identity that is not
	// on the worklist must not put it there — "run this on opus next time" and
	// "start this now" are different intents, and one op for both would make the
	// first quietly do the second.
	OpModel IdentityOp = "model"
)

// Valid reports whether the op is one this build knows.
func (o IdentityOp) Valid() bool {
	switch o {
	case OpRole, OpMove, OpGrant, OpRevoke, OpEmploy, OpFire, OpModel:
		return true
	default:
		return false
	}
}

// IdentityOps lists the vocabulary, for tests that must be total.
func IdentityOps() []IdentityOp {
	return []IdentityOp{OpRole, OpMove, OpGrant, OpRevoke, OpEmploy, OpFire, OpModel}
}

// IdentityEvent is one line of an identity's journal.
type IdentityEvent struct {
	op     IdentityOp
	by     user.Name
	at     time.Time
	role   Name
	boss   user.Name
	grant  Grant
	model  Model
	effort Effort
}

// AssignRole is `orc assign role <identity> <role>`. It replaces: an identity
// holds exactly one role, so that its authority is a number rather than a
// maximum over a set.
func AssignRole(by user.Name, at time.Time, role Name) (IdentityEvent, error) {
	if role.Zero() {
		return IdentityEvent{}, fault.Internal{Where: "model.AssignRole", Detail: "no role named"}
	}
	return newIdentityEvent(IdentityEvent{op: OpRole, by: by, at: at, role: role})
}

// Move is `orc move <identity> <boss>`.
func Move(by user.Name, at time.Time, boss user.Name) (IdentityEvent, error) {
	if boss.Zero() {
		return IdentityEvent{}, fault.Internal{Where: "model.Move", Detail: "no boss named"}
	}
	return newIdentityEvent(IdentityEvent{op: OpMove, by: by, at: at, boss: boss})
}

// GrantPermission is `orc grant permission <identity> <permission>`.
func GrantPermission(by user.Name, at time.Time, g Grant) (IdentityEvent, error) {
	if g.Zero() {
		return IdentityEvent{}, fault.Internal{Where: "model.GrantPermission", Detail: "no grant given"}
	}
	return newIdentityEvent(IdentityEvent{op: OpGrant, by: by, at: at, grant: g})
}

// RevokePermission is `orc revoke permission <identity> <permission>`.
func RevokePermission(by user.Name, at time.Time, permission Name) (IdentityEvent, error) {
	if permission.Zero() {
		return IdentityEvent{}, fault.Internal{Where: "model.RevokePermission", Detail: "no permission named"}
	}
	return newIdentityEvent(IdentityEvent{op: OpRevoke, by: by, at: at, grant: Grant{permission: permission}})
}

// Employ is `orc employ <identity>`: put it on the worklist at a given load.
//
// The model and effort are part of the event rather than read from elsewhere at
// populate time, so the journal records what was asked for. An identity whose load
// changed is an identity whose journal says when and by whom.
func Employ(by user.Name, at time.Time, m Model, e Effort) (IdentityEvent, error) {
	if !m.Valid() {
		return IdentityEvent{}, fault.Usage{Reason: fmt.Sprintf(
			"employ needs a model orc can budget: haiku, sonnet, or opus (got %s)", m)}
	}
	if !e.Valid() {
		return IdentityEvent{}, fault.Usage{Reason: fmt.Sprintf(
			"employ needs an effort level: low, medium, high, xhigh, or max (got %s)", e)}
	}
	return newIdentityEvent(IdentityEvent{op: OpEmploy, by: by, at: at, model: m, effort: e})
}

// Retune is `orc model <identity> <model>`: change what it thinks with.
//
// Both halves are always carried, even when only one was asked for, because load is
// the product of the two and an event that recorded half of it would leave the
// journal unable to say what a session cost.
func Retune(by user.Name, at time.Time, m Model, e Effort) (IdentityEvent, error) {
	if !m.Valid() {
		return IdentityEvent{}, fault.Usage{Reason: fmt.Sprintf(
			"a model orc can budget: haiku, sonnet, or opus (got %s)", m)}
	}
	if !e.Valid() {
		return IdentityEvent{}, fault.Usage{Reason: fmt.Sprintf(
			"an effort level: low, medium, high, xhigh, or max (got %s)", e)}
	}
	return newIdentityEvent(IdentityEvent{op: OpModel, by: by, at: at, model: m, effort: e})
}

// Fire is `orc fire <identity>`: take it off the worklist.
func Fire(by user.Name, at time.Time) (IdentityEvent, error) {
	return newIdentityEvent(IdentityEvent{op: OpFire, by: by, at: at})
}

func newIdentityEvent(e IdentityEvent) (IdentityEvent, error) {
	if e.by.Zero() {
		return IdentityEvent{}, fault.Internal{Where: "model.IdentityEvent", Detail: "no actor named"}
	}
	if e.at.IsZero() {
		return IdentityEvent{}, fault.Internal{Where: "model.IdentityEvent", Detail: "no timestamp"}
	}
	if !e.op.Valid() {
		return IdentityEvent{}, fault.Internal{Where: "model.IdentityEvent", Detail: "unknown op " + string(e.op)}
	}
	e.at = clock.Normalise(e.at)
	return e, nil
}

// Accessors, for the journal codec in internal/store.

func (e IdentityEvent) Op() IdentityOp  { return e.op }
func (e IdentityEvent) By() user.Name   { return e.by }
func (e IdentityEvent) At() time.Time   { return e.at }
func (e IdentityEvent) Role() Name      { return e.role }
func (e IdentityEvent) Boss() user.Name { return e.boss }
func (e IdentityEvent) Grant() Grant    { return e.grant }
func (e IdentityEvent) Model() Model    { return e.model }
func (e IdentityEvent) Effort() Effort  { return e.effort }
func (e IdentityEvent) Zero() bool      { return e.op == "" }

// With applies an event, returning the identity as it stands afterwards.
//
// Pure, like Role.With, so the rules can be fuzzed without a filesystem. The one
// rule it cannot enforce is the interesting one: a `move` that would create a
// cycle is invisible from inside a single identity, so it is checked against the
// whole fleet in internal/authz before the event is ever written.
func (i Identity) With(e IdentityEvent) (Identity, error) {
	if i.Zero() {
		return Identity{}, fault.Internal{Where: "model.Identity.With", Detail: "identity is unconstructed"}
	}
	if e.Zero() {
		return Identity{}, fault.Internal{Where: "model.Identity.With", Detail: "event is unconstructed"}
	}

	switch e.op {
	case OpRole:
		i.role = e.role

	case OpMove:
		if i.IsOperator() {
			return Identity{}, fault.Denied{Actor: e.by.String(), Action: "move", Target: i.name.String(),
				Reason: "the operator is the root of the tree and has no boss"}
		}
		if e.boss.String() == i.name.String() {
			return Identity{}, fault.Usage{Reason: fmt.Sprintf("%s cannot be its own boss", i.name)}
		}
		i.boss = e.boss

	case OpGrant:
		g := e.grant
		if g.Zero() {
			return Identity{}, fault.Internal{Where: "model.Identity.With", Detail: "grant event carries no grant"}
		}
		// A second grant of the same permission replaces the first rather than
		// stacking: two expiries on one permission would leave "when does this
		// lapse?" with two answers.
		kept := make([]Grant, 0, len(i.grants)+1)
		for _, old := range i.grants {
			if !old.permission.Equal(g.permission) {
				kept = append(kept, old)
			}
		}
		if len(kept) >= MaxGrants {
			return Identity{}, fault.Conflict{Path: i.name.String(), Reason: fmt.Sprintf(
				"an identity may hold %d grants at once", MaxGrants)}
		}
		kept = append(kept, g)
		slices.SortFunc(kept, func(a, b Grant) int { return a.permission.Compare(b.permission) })
		i.grants = kept

	case OpEmploy:
		i.employed = true
		i.model, i.effort = e.model, e.effort

	case OpModel:
		// Employment is untouched: this says what the identity thinks with, not
		// whether it is thinking. An employed identity's load changes with it,
		// which is why the caller has to have afforded it first.
		i.model, i.effort = e.model, e.effort

	case OpFire:
		// The model and effort stay. Re-employing an identity should remember what
		// it was running, and a journal that forgot would make `fire` a quiet
		// downgrade rather than a pause.
		i.employed = false

	case OpRevoke:
		kept := make([]Grant, 0, len(i.grants))
		for _, old := range i.grants {
			if !old.permission.Equal(e.grant.permission) {
				kept = append(kept, old)
			}
		}
		// Revoking what was never granted is not an error: the caller's intent —
		// that this identity not hold it — is satisfied either way, and a
		// refusal would make `revoke` unsafe to run twice.
		i.grants = kept

	default:
		return Identity{}, fault.Internal{Where: "model.Identity.With", Detail: "unhandled op " + string(e.op)}
	}

	if err := i.validate(); err != nil {
		return Identity{}, err
	}
	return i, nil
}
