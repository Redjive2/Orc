package store

import (
	"orc/common/fault"
	"orc/orc/internal/model"
)

// The toolkit: the permissions every fleet has on the day it is made.
//
// "Toolkit" rather than "builtin" on purpose. Auth_Perm_Role.md already uses
// "the builtin permissions" for the pattern *kinds* — read, write, spawn — and two
// meanings for one phrase in one system is how a conversation about permissions
// stops being about the same thing.
//
// A fresh fleet used to have none, which meant the first thing anybody did was
// invent a vocabulary — and invent it differently each time, so two fleets could
// not be talked about in the same words. These are the ones almost every fleet
// needs: read the tree, write the docs, look at the fleet without touching it,
// direct agents, hand out policy, and upgrade the machines.
//
// They are **ordinary permissions**, not special cases. Assignable to a role,
// grantable, listed by `orc list permissions`, refused to anybody below the floor,
// and removable with `orc remove permission` if a fleet wants its own vocabulary
// instead. Nothing in the derivation knows they exist. The only thing that makes
// them builtin is that `orc bootstrap` creates them.
//
// Two rules shaped the set:
//
// **The floor is the policy.** Authority is 1–99 for agents and 100 for the
// operator, so a floor says who may hold a thing at all. Reading is 1 because an
// agent that cannot read cannot work; writing everything is 70 because it is most
// of a machine; policy is 85 because handing out authority is how authority
// leaks; and `upgrade` is 90 because it replaces every binary on every machine.
//
// **Containment is by clause, not by name** — see authz.Fleet.Holds. That is right
// for paths: an identity that may write everything may hand on a permission to
// write one directory. It is wrong for a capability that is a *marker*, which is
// why `upgrade` is `tool(upgrade)` and not a path glob: a role with `write(**)` at
// floor 70 must not reach a permission whose floor is 90, and with a path clause
// it would.

// The floors, named so the reasoning is in one place rather than in a table of
// numbers nobody can audit.
const (
	// FloorRead is the bottom. An agent that cannot read cannot work, so this
	// gates nothing and exists to be uniform with the rest.
	FloorRead = 1
	// FloorWriteDocs is low on purpose: prose is reviewable and revertible, and
	// an agent that may not write anything cannot do the job it was hired for.
	FloorWriteDocs = 20
	// FloorWriteAll is most of a machine.
	FloorWriteAll = 70
	// FloorAgents is directing other agents — hiring, employing, firing. It costs
	// money and it spends the fleet's budget.
	FloorAgents = 60
	// FloorInstruct is writing what an agent is told at the start of every
	// session. It is not authority — a prompt asks and a permission enforces —
	// but it decides how an agent thinks, so it sits above directing one.
	FloorInstruct = 70
	// FloorPolicy is handing out authority, which is how authority leaks.
	FloorPolicy = 85
	// FloorUpgrade replaces every binary on every machine in the fleet.
	FloorUpgrade = 90

	// FloorShellRead is running commands that look but do not touch — `ls`, `git
	// status`, `wc`. Low, because an agent that cannot run them is an agent that
	// works blind in a tree it is allowed to read anyway.
	FloorShellRead = 10
	// FloorShellBuild is the toolchain: compilers, formatters, test runners. They
	// write, and what they write is build output rather than somebody's work.
	FloorShellBuild = 40
	// FloorShellAll is a shell with nothing taken out of it, which is every
	// capability the machine has. It sits beside FloorWriteAll because it is
	// strictly more than writing everything: a command can also reach the
	// network, and the network is how a workspace stops being contained.
	FloorShellAll = 75
)

// UpgradeFloor is kept as the name cq's documentation refers to.
const UpgradeFloor = FloorUpgrade

// ToolkitPermission is one permission the toolkit provides.
type ToolkitPermission struct {
	Name     model.Name
	Floor    model.Authority
	Patterns []model.Pattern
	// Why is one line, for `orc doctor` and for anybody reading the list who has
	// to decide whether to hand it out.
	Why string
}

// the toolkit, written as text and parsed once. Text rather than constructed
// values because this is the list somebody reads to learn the vocabulary, and a
// list of ParsePattern calls is not that.
var toolkit = []struct {
	name     string
	floor    int
	patterns []string
	why      string
}{
	// Reading.
	{"read-all", FloorRead, []string{"read(**)"},
		"read every file in the workspace"},
	{"read-docs", FloorRead, []string{"read(Docs/**)"},
		"read the specifications and nothing else"},

	// Writing. `write-code` is expressible now that a clause can subtract —
	// `write(** except Docs/**)` — but it is not here, because "code" is a
	// different set in every tree and a builtin that guesses wrong is worse than
	// no builtin. The syntax is in the help; a fleet writes its own.
	{"write-docs", FloorWriteDocs, []string{"read(Docs/**)", "write(Docs/**)"},
		"edit the specifications"},
	{"write-all", FloorWriteAll, []string{"read(**)", "write(**)"},
		"edit anything in the workspace"},

	// Orc's own verbs. These **narrow**: an identity with no orc-kind clause is
	// governed by the structural rules alone, and one with any is additionally
	// held to them.
	//
	// Only the verbs that *change* something consult that gate — `status`, `list`,
	// `introspect`, `verify`, `doctor`, `tend`, and `budget` never do, because
	// reading a fleet you are already in is not a privilege and `tend` is run
	// implicitly by almost every other command. So every clause below names a
	// verb that is actually checked; naming an unchecked one would read like a
	// control and be nothing.
	//
	// Which makes `orc-read` the odd one, and worth understanding before handing
	// it out: its clause allows nothing anybody lacked, and its *effect* is the
	// narrowing. Holding it bars every orc verb that changes anything.
	{"orc-read", FloorRead, []string{"orc(introspect)"},
		"confine to reading — holding this bars every orc verb that changes anything"},
	{"orc-agents", FloorAgents, []string{
		"orc(new move employ fire attach poke refresh wake model)",
	}, "hire agents, direct them, and put them on the work list"},
	{"orc-policy", FloorPolicy, []string{
		"orc(assign edit grant revoke remove)",
	}, "hand out roles, permissions, and authority"},

	// The shell. Unlike every other kind, `shell` refuses by default — an agent
	// with none of these may run only model.Innocuous — so these are what make a
	// fresh fleet able to do anything at a prompt at all.
	//
	// They are named for what somebody is *for* rather than for the commands in
	// them, because the list is long and the reason is short. A fleet that wants
	// a different set writes one; that is what `orc new permission` is.
	{"shell-read", FloorShellRead, []string{
		"shell(ls find grep rg cat head tail wc file stat du df git which)",
	}, "run the commands that look at a tree without changing it"},
	{"shell-build", FloorShellBuild, []string{
		"shell(go gofmt make cargo npm pnpm yarn node python python3 pytest sh bash)",
	}, "run the toolchain: compile, format, and test"},
	{"shell-all", FloorShellAll, []string{"shell(**)"},
		"run any command at all, including ones nothing can read in advance"},

	// Capabilities that live in another tool. `tool(...)` rather than a path
	// glob so that no broad permission confers one by containment — see the note
	// at the top of this file.
	{"upgrade", FloorUpgrade, []string{"tool(upgrade)"},
		"rebuild and restart every Orc tool, on every machine in the fleet"},
	// Standing instructions. `tool(...)` for the same containment reason: a role
	// with `write(**)` must not acquire the ability to rewrite what every agent is
	// told by being broad.
	//
	// Floor 70 rather than 85, because instructing an agent you already control is
	// closer to directing it than to handing out authority. The layer that reaches
	// *everybody* — the fleet's own — is fenced off separately by being the
	// operator's alone, and no permission grants it.
	{"instruct", FloorInstruct, []string{"tool(instruct)"},
		"write the standing instructions agents run under"},
}

// Toolkit returns the set, parsed.
//
// A malformed entry is a defect in the table above rather than anything a caller
// did, so it comes back as an error instead of a panic — the no-panic rule holds
// here as everywhere, and a fleet missing a builtin is something `orc doctor`
// reports rather than a crash on startup.
func Toolkit() ([]ToolkitPermission, error) {
	out := make([]ToolkitPermission, 0, len(toolkit))
	for _, want := range toolkit {
		name, err := model.ParseName(want.name)
		if err != nil {
			return nil, fault.Internal{Where: "store.Toolkit", Detail: want.name + ": " + err.Error()}
		}
		floor, err := model.NewAuthority(want.floor)
		if err != nil {
			return nil, fault.Internal{Where: "store.Toolkit", Detail: want.name + ": " + err.Error()}
		}
		patterns := make([]model.Pattern, 0, len(want.patterns))
		for _, raw := range want.patterns {
			p, err := model.ParsePattern(raw)
			if err != nil {
				return nil, fault.Internal{Where: "store.Toolkit", Detail: want.name + ": " + err.Error()}
			}
			patterns = append(patterns, p)
		}
		out = append(out, ToolkitPermission{Name: name, Floor: floor, Patterns: patterns, Why: want.why})
	}
	return out, nil
}

// EnsureToolkit creates any toolkit permission the store does not have.
//
// Idempotent, and safe on a fleet that predates a new one: an existing permission
// of the same name is left exactly as it is. Orc never rewrites a permission — see
// permissionRecord — and a fleet where somebody has deliberately redefined
// `write-docs` is not one this should quietly overwrite.
//
// That is also what makes `orc bootstrap` the way to top up an older fleet. It is
// documented as safe to run twice, and this is what makes it useful rather than
// merely harmless.
func (s *Store) EnsureToolkit() error {
	want, err := Toolkit()
	if err != nil {
		return err
	}
	for _, p := range want {
		if _, err := s.Permission(p.Name); err == nil {
			continue
		}
		if _, err := s.CreatePermission(p.Name, p.Floor, p.Patterns); err != nil {
			return err
		}
	}
	return nil
}
