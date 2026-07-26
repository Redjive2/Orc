package protocol

import (
	"slices"

	"orc/cq/internal/fault"
)

// The fleet: Orc's side of the mirror.
//
// cq already mirrors the mailbox and the task pool. This is the third thing on an
// agent machine worth seeing from a phone — who exists, what they may do, and what
// is running — and the one it was previously impossible to change without a
// terminal on the machine itself.
//
// Everything here is *derived* state as Orc reports it. Nothing effective is
// stored in Orc: an identity's authority is the lower of its role's and its
// boss's, and its permissions are its role's intersected with its boss's. So the
// mirror carries what `orc status --json` computed at snapshot time, and the
// browser never recomputes it — a second derivation would be a second opinion
// about who may do what, and the wrong one would be the one on screen.

// Fleet is the whole of one machine's Orc store, as of the snapshot.
type Fleet struct {
	Root        string      `json:"root"`
	Operator    string      `json:"operator"`
	Identities  []FleetID   `json:"identities,omitempty"`
	Roles       []FleetRole `json:"roles,omitempty"`
	Permissions []FleetPerm `json:"permissions,omitempty"`
	Problems    []string    `json:"problems,omitempty"`
	// Unreachable says why there is no fleet here, when there is a reason worth
	// telling apart from "this machine runs no agents". An orc that is not
	// installed and an orc that refused are different problems with different
	// fixes, and a blank panel says neither.
	Unreachable string `json:"unreachable,omitempty"`
}

// FleetID is one identity, with its derived authority and what it is running.
type FleetID struct {
	Name         string        `json:"name"`
	Operator     bool          `json:"operator"`
	Boss         string        `json:"boss,omitempty"`
	Role         string        `json:"role,omitempty"`
	Authority    int           `json:"authority"`
	AskedFor     int           `json:"asked_for"`
	Capped       bool          `json:"capped"`
	Chain        []string      `json:"chain,omitempty"`
	Subordinates []string      `json:"subordinates,omitempty"`
	Clauses      []FleetClause `json:"clauses,omitempty"`
	Grants       []FleetGrant  `json:"grants,omitempty"`
	Workspace    string        `json:"workspace,omitempty"`
	Budget       int           `json:"spawn_budget"`
	HasBudget    bool          `json:"has_spawn_budget"`

	// The worklist half, and then what is actually running. Separate because the
	// states where they disagree are the ones worth mirroring: employed with no
	// session is an agent something keeps killing.
	Employed bool   `json:"employed"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
	Load     int    `json:"load"`

	Populated bool   `json:"populated"`
	Session   string `json:"session,omitempty"`
	Restarts  int    `json:"restarts,omitempty"`
	LastExit  string `json:"last_exit,omitempty"`
}

// FleetClause is one effective permission clause, already narrowed by the boss
// chain. `Capped` and `Asked` are both carried because an agent told it may write
// one directory while its role says another will otherwise file a bug.
type FleetClause struct {
	Kind       string `json:"kind"`
	Arg        string `json:"arg"`
	Permission string `json:"permission,omitempty"`
	Source     string `json:"source,omitempty"`
	Capped     bool   `json:"capped,omitempty"`
	Asked      string `json:"asked,omitempty"`
	Lapses     string `json:"lapses,omitempty"`
}

// FleetGrant is one temporary permission, live or lapsed. Lapsed ones are kept
// rather than filtered: "I granted that and it stopped working" is a question the
// panel should answer, and a row that has vanished answers nothing.
type FleetGrant struct {
	Permission string `json:"permission"`
	By         string `json:"by,omitempty"`
	Until      string `json:"until,omitempty"`
	Session    string `json:"session,omitempty"`
	Live       bool   `json:"live"`
}

// FleetRole is one job.
type FleetRole struct {
	Name        string   `json:"name"`
	Authority   int      `json:"authority"`
	Description string   `json:"description,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
	HeldBy      []string `json:"held_by,omitempty"`
}

// FleetPerm is one named clause set.
type FleetPerm struct {
	Name     string   `json:"name"`
	Floor    int      `json:"floor"`
	Patterns []string `json:"patterns,omitempty"`
}

// Validate checks the fleet is one cq can draw.
//
// Deliberately shallow. Orc owns what a fleet may contain and has already refused
// anything malformed before printing it; re-deriving those rules here would put a
// second, weaker copy of Orc's model in cq, and the copy that drifts is the one
// that never sees a real store. What is checked is what cq itself depends on:
// names it will put in URLs, and numbers it will draw bars from.
func (f Fleet) Validate() error {
	if f.Unreachable != "" {
		// A fleet that could not be read carries nothing else, and saying why is
		// the whole of what it is for.
		return checkText("Fleet", "unreachable", f.Unreachable, MaxNoteRunes, false)
	}
	if f.Operator != "" {
		if err := checkName("Fleet", "operator", f.Operator); err != nil {
			return err
		}
	}
	if len(f.Identities) > MaxListItems {
		return fault.Field("Fleet", "identities", "%d identities exceeds the limit of %d",
			len(f.Identities), MaxListItems)
	}
	for i, id := range f.Identities {
		if err := checkName("FleetID", "name", id.Name); err != nil {
			return fault.Field("Fleet", "identities", "[%d]: %v", i, err)
		}
		// 0 to 100: the operator is 100 and everyone else 1 to 99, and a zero is
		// an identity with no role yet rather than an error.
		if err := inRange("FleetID", "authority", id.Authority, 0, 100); err != nil {
			return err
		}
	}
	for i, r := range f.Roles {
		if err := checkName("FleetRole", "name", r.Name); err != nil {
			return fault.Field("Fleet", "roles", "[%d]: %v", i, err)
		}
	}
	for i, p := range f.Permissions {
		if err := checkName("FleetPerm", "name", p.Name); err != nil {
			return fault.Field("Fleet", "permissions", "[%d]: %v", i, err)
		}
	}
	return nil
}

// The fleet verbs, one per Orc command that changes something.
//
// Namespaced `orc.` for the same reason the task verbs are namespaced `task.`:
// `create`, `delete`, and `assign` already mean something else in cq, and an
// operation whose meaning depends on which fields happen to be set is one the
// queue cannot report on honestly.
//
// What is deliberately absent is as considered as what is here:
//
//   - `bootstrap` makes the fleet, and there is nothing to mirror before it runs.
//   - `attach` hands over a terminal. A queue that runs minutes later cannot.
//   - `env` and `owner env` print a credential. Nothing in cq will carry one.
//   - `owner rename` and `owner reset` act on the operator itself — the account
//     the mirror authenticates as. A sheet in a browser, over state minutes old,
//     is the wrong place to rename or destroy that; both stay at the terminal.
//   - `status`, `list`, `introspect`, `verify`, `doctor`, `check-control` are
//     reads. The first two arrive in every snapshot; the rest answer about the
//     agent machine, where there is nobody to read the answer.
const (
	OpOrcNewIdentity     Op = "orc.new.identity"      // orc new identity <name>
	OpOrcNewRole         Op = "orc.new.role"          // orc new role <name> <authority> <what>
	OpOrcNewPermission   Op = "orc.new.permission"    // orc new permission <name> <floor> <patterns…>
	OpOrcAssignRole      Op = "orc.assign.role"       // orc assign role <identity> <role>
	OpOrcAssignAuthority Op = "orc.assign.authority"  // orc assign authority <role> <authority>
	OpOrcAssignPerm      Op = "orc.assign.permission" // orc assign permission <role> <permission>
	OpOrcRemoveIdentity  Op = "orc.remove.identity"   // orc remove identity <name> --yes
	OpOrcRemoveRole      Op = "orc.remove.role"       // orc remove role <name> --yes
	OpOrcRemovePerm      Op = "orc.remove.permission" // orc remove permission <name> [--from <role>] --yes
	OpOrcGrant           Op = "orc.grant"             // orc grant permission <who> <perm> [--until <d>]
	OpOrcRevoke          Op = "orc.revoke"            // orc revoke permission <who> <perm>
	OpOrcMove            Op = "orc.move"              // orc move <identity> <boss>
	OpOrcEmploy          Op = "orc.employ"            // orc employ <identity> [--model m] [--effort e]
	OpOrcFire            Op = "orc.fire"              // orc fire <identity> --yes
	OpOrcBudget          Op = "orc.budget"            // orc budget <role> <load>
	OpOrcPoke            Op = "orc.poke"              // orc poke <identity> [message]
	OpOrcRefresh         Op = "orc.refresh"           // orc refresh <identity>
	OpOrcTend            Op = "orc.tend"              // orc tend
)

// OpUpgrade rebuilds and restarts every Orc tool on the machine it reaches.
//
// It is not a fleet verb — it goes through no other tool — but it lives here
// because it is the other half of the same request. The server upgrades itself
// directly, since that is local; the agent machines cannot be reached, so each
// gets one of these and does the work on its next sync.
//
// That is also what makes it survive the restart in the middle. The action is on
// disk before the server goes down, so an agent that synced during the gap simply
// fails and retries, and an agent that had not synced yet finds it waiting.
const OpUpgrade Op = "system.upgrade"

// FleetOps are the verbs that go through Orc rather than Mailman or Macmuffin.
var FleetOps = []Op{
	OpOrcNewIdentity, OpOrcNewRole, OpOrcNewPermission,
	OpOrcAssignRole, OpOrcAssignAuthority, OpOrcAssignPerm,
	OpOrcRemoveIdentity, OpOrcRemoveRole, OpOrcRemovePerm,
	OpOrcGrant, OpOrcRevoke, OpOrcMove,
	OpOrcEmploy, OpOrcFire, OpOrcBudget,
	OpOrcPoke, OpOrcRefresh, OpOrcTend,
}

// TouchesFleet reports whether an operation changes the Orc store.
func (o Op) TouchesFleet() bool { return slices.Contains(FleetOps, o) }

// fleetRules is the operand contract for each fleet verb, folded into argRules by
// protocol.go's initialiser.
var fleetRules = map[Op]argRule{
	OpOrcNewIdentity:     {identity: true},
	OpOrcNewRole:         {role: true, authority: true, description: true},
	OpOrcNewPermission:   {permission: true, floor: true, patterns: true},
	OpOrcAssignRole:      {identity: true, role: true},
	OpOrcAssignAuthority: {role: true, authority: true},
	OpOrcAssignPerm:      {role: true, permission: true},
	OpOrcRemoveIdentity:  {identity: true},
	OpOrcRemoveRole:      {role: true},
	// `--from <role>` narrows one role instead of deleting the permission, so the
	// role is optional and the two are genuinely different commands.
	OpOrcRemovePerm: {permission: true, optRole: true},
	OpOrcGrant:      {identity: true, permission: true, optUntil: true},
	OpOrcRevoke:     {identity: true, permission: true},
	OpOrcMove:       {identity: true, boss: true},
	OpOrcEmploy:     {identity: true, optSession: true},
	OpOrcFire:       {identity: true},
	OpOrcBudget:     {role: true, load: true},
	OpOrcPoke:       {identity: true, optMessage: true},
	OpOrcRefresh:    {identity: true},
	OpOrcTend:       {},
}

// validateFleetArgs checks the Orc operands.
//
// The ranges are Orc's own and are checked here as well as there, so a value the
// fleet would refuse never becomes a queued action that fails hours later on a
// machine nobody is watching.
func (a Action) validateFleetArgs(rule argRule) error {
	for _, c := range []struct {
		want  bool
		field string
		value string
	}{
		{rule.identity, "identity", a.Args.Identity},
		{rule.boss, "boss", a.Args.Boss},
		{rule.permission, "permission", a.Args.Permission},
	} {
		switch {
		case c.want && c.value == "":
			return fault.Field("Action", "args."+c.field, "%s requires %s", a.Op, c.field)
		case !c.want && c.value != "":
			return unexpected(a.Op, c.field)
		}
		if c.want {
			if err := checkName("Action", "args."+c.field, c.value); err != nil {
				return err
			}
		}
	}

	// The role is required by some verbs and optional for one, so it cannot go in
	// the loop above.
	switch {
	case rule.role && a.Args.Role == "":
		return fault.Field("Action", "args.role", "%s requires role", a.Op)
	case !rule.role && !rule.optRole && a.Args.Role != "":
		return unexpected(a.Op, "role")
	}
	if a.Args.Role != "" {
		if err := checkName("Action", "args.role", a.Args.Role); err != nil {
			return err
		}
	}

	for _, c := range []struct {
		want   bool
		field  string
		got    int
		lo, hi int
	}{
		// A role's authority runs 1 to 99: 100 is the operator, and the operator is
		// a position rather than a level anybody can be assigned.
		{rule.authority, "authority", a.Args.Authority, 1, 99},
		{rule.floor, "floor", a.Args.Floor, 1, 100},
		{rule.load, "load", a.Args.Load, 0, MaxSpawnLoad},
	} {
		if !c.want {
			if c.got != 0 {
				return unexpected(a.Op, c.field)
			}
			continue
		}
		// A load of zero is a real budget — it refuses every employ — so only the
		// two that cannot be zero are required to be present.
		if c.got == 0 && c.field != "load" {
			return fault.Field("Action", "args."+c.field, "%s requires %s", a.Op, c.field)
		}
		if err := inRange("Action", "args."+c.field, c.got, c.lo, c.hi); err != nil {
			return err
		}
	}

	if rule.patterns {
		if len(a.Args.Patterns) == 0 {
			return fault.Field("Action", "args.patterns", "%s requires patterns", a.Op)
		}
		if len(a.Args.Patterns) > MaxListItems {
			return fault.Field("Action", "args.patterns", "%d patterns exceeds the limit of %d",
				len(a.Args.Patterns), MaxListItems)
		}
		for i, p := range a.Args.Patterns {
			if err := checkText("Action", "args.patterns", p, MaxPatternRunes, false); err != nil {
				return fault.Field("Action", "args.patterns", "[%d]: %v", i, err)
			}
		}
	} else if len(a.Args.Patterns) > 0 {
		return unexpected(a.Op, "patterns")
	}

	for _, c := range []struct {
		want  bool
		field string
		value string
		max   int
	}{
		{rule.description, "description", a.Args.Description, MaxDescriptionRunes},
		{rule.optUntil, "until", a.Args.Until, 32},
		{rule.optMessage, "message", a.Args.Message, MaxBodyBytes},
		{rule.optSession, "model", a.Args.Model, 32},
		{rule.optSession, "effort", a.Args.Effort, 32},
	} {
		if !c.want && c.value != "" {
			return unexpected(a.Op, c.field)
		}
		if c.want && c.value != "" {
			if err := checkText("Action", "args."+c.field, c.value, c.max, false); err != nil {
				return err
			}
		}
	}
	// A description is the one of those that is required rather than optional: a
	// role with no description is a role nobody can tell apart from another.
	if rule.description && a.Args.Description == "" {
		return fault.Field("Action", "args.description", "%s requires description", a.Op)
	}
	return nil
}
