package protocol

import (
	"path"
	"slices"
	"strings"
	"time"

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
// FleetPrompt is one standing instruction, mirrored.
//
// Four kinds and two mechanisms: `system`, `role`, and `identity` are prompt layers
// that compose additively into what an agent is told at the start of a session;
// `wake` on any of them is the message sent to an agent that has gone quiet, and the
// most specific of those wins outright. The browser has to keep them apart because
// they are edited in the same place and mean opposite things.
// FleetSession is what an agent has been doing, as `orc view --json` reports it.
//
// It travels with the snapshot rather than being fetched, because it has to: the
// server can never reach an agent machine, so the browser can only ever show what
// the last sync carried. That is the same bargain as the rest of this file, and it
// means a session on the website is as stale as the mirror says it is — which the
// panel states rather than hides.
//
// Only *live* sessions are carried, and only their tail. A fleet of twelve agents
// with a full transcript each would be a snapshot measured in megabytes, sent every
// five minutes, to show a panel somebody looks at occasionally.
type FleetSession struct {
	Identity string `json:"identity"`
	Role     string `json:"role,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
	Live     bool   `json:"live"`
	// Waiting is the fact the panel is mostly consulted for: an agent that has
	// stopped and is waiting to be spoken to looks exactly like one that is
	// thinking, and only one of them needs a poke.
	Waiting bool   `json:"waiting"`
	Turn    int    `json:"turn"`
	Started string `json:"started,omitempty"`

	Prose          []SessionLine `json:"prose,omitempty"`
	ProseAvailable bool          `json:"prose_available"`
	Rows           []SessionRow  `json:"rows,omitempty"`
	// Note is why this is thinner than it should be, when it is: a feed that would
	// not parse, a transcript that could not be read. An empty panel reads as an
	// idle agent, which is the one thing it must not be mistaken for.
	Note string `json:"note,omitempty"`
}

// SessionLine is one thing somebody said.
type SessionLine struct {
	Who  string `json:"who"`
	Text string `json:"text"`
}

// SessionRow is one event from orc's own journal.
type SessionRow struct {
	At      string `json:"at"`
	Turn    int    `json:"turn"`
	Kind    string `json:"kind"`
	Tool    string `json:"tool,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Verdict string `json:"verdict,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Blocked bool   `json:"blocked,omitempty"`
}

// Validate checks a session is addressable and bounded.
func (s FleetSession) Validate() error {
	if err := checkName("FleetSession", "identity", s.Identity); err != nil {
		return err
	}
	if s.Turn < 0 {
		return fault.Field("FleetSession", "turn", "turn %d is negative", s.Turn)
	}
	return nil
}

type FleetPrompt struct {
	Kind string `json:"kind"`
	// Name is the role or identity it belongs to, empty for the fleet's own.
	Name string `json:"name,omitempty"`
	Wake bool   `json:"wake,omitempty"`
	Text string `json:"text"`
	Size int    `json:"size"`
	// Changed, By, and Digest are orc's last word about this layer. Digest is what
	// makes an edit from a stale snapshot refusable, the way Base does for a file.
	Changed string `json:"changed,omitempty"`
	By      string `json:"by,omitempty"`
	Digest  string `json:"digest,omitempty"`
}

type Fleet struct {
	Root        string      `json:"root"`
	Operator    string      `json:"operator"`
	Identities  []FleetID   `json:"identities,omitempty"`
	Roles       []FleetRole `json:"roles,omitempty"`
	Permissions []FleetPerm `json:"permissions,omitempty"`
	// Toolkit is the set of permissions every fleet is made with, as the agent
	// machine's orc defines them, and whether this fleet has each one.
	//
	// It travels for the same reason the vocabulary does: the toolkit is a table
	// inside orc's binary, and a browser keeping its own copy is one that goes
	// stale silently. It matters because a fleet made before a toolkit permission
	// existed simply does not have it, and the only symptom is a list missing rows
	// nobody knew to expect.
	Toolkit    []FleetToolkit  `json:"toolkit,omitempty"`
	Vocabulary FleetVocabulary `json:"vocabulary,omitzero"`
	// Pace is the fleet's own layer — the default every identity falls back to,
	// and what a browser edits when it means "all of them" rather than "this one".
	// Distinct from each identity's resolved pace above, which is the answer
	// rather than the setting.
	Pace FleetPace `json:"pace,omitzero"`
	// Tariff is what this fleet charges per model and per effort, with what orc's
	// own measurement suggests instead.
	//
	// The suggestion travels rather than being computed here from the same
	// buckets: a second implementation of the normalisation would be a second
	// opinion about what a fleet should charge, and the two would drift the first
	// time either rounded differently.
	Tariff []FleetTariff `json:"tariff,omitempty"`
	// Prompts are the standing instructions agents run under: the fleet's layer,
	// each role's, each identity's, and every wake message. The text travels with
	// them because the tab is an editor rather than a listing, and one that had to
	// fetch a layer separately could open a prompt somebody changed since the
	// snapshot.
	Prompts []FleetPrompt `json:"prompts,omitempty"`
	// Sessions are what the live agents have been doing — `orc view` for each,
	// carried so the browser can show one without reaching the machine.
	Sessions []FleetSession `json:"sessions,omitempty"`
	Problems []string       `json:"problems,omitempty"`
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
	// What it is doing, in one word, with the turn and the last few events.
	//
	// Derived by Orc rather than worked out here, for the reason the derived
	// clauses are: a browser computing `generating` from a session id and a
	// timestamp would be a second opinion about what an agent is doing, wrong in
	// exactly the cases somebody opened the tab for. Empty from an older orc,
	// which the screen reads as "cannot say" rather than as idle.
	Activity string     `json:"activity,omitempty"`
	Turn     int        `json:"turn,omitempty"`
	Since    string     `json:"since,omitempty"`
	Why      string     `json:"why,omitempty"`
	Doing    []FleetRow `json:"doing,omitempty"`
	FeedRead bool       `json:"feed_read,omitempty"`
	// Buckets is what this identity has cost and touched, by the hour, for as far
	// back as a snapshot carries. The server keeps the long series; this is the
	// window that keeps it current — see store.MergeActivity.
	Buckets []ActivityBucket `json:"buckets,omitempty"`
	// Pace is what this identity's cycles resolve to, and which layer said so.
	//
	// Resolved by orc rather than layered here: the fleet's, the role's and the
	// identity's are three records, and a browser that had them all and picked
	// would be a second implementation of the precedence — wrong in exactly the
	// case somebody set an exception and wants to see it take effect.
	Pace      FleetPace `json:"pace,omitzero"`
	Budget    int       `json:"spawn_budget"`
	HasBudget bool      `json:"has_spawn_budget"`

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

// FleetPace is how often something is woken and tended, as orc resolved it.
//
// A value and the layer it came from, because those are two different things a
// person needs: "woken after 20m" answers what will happen, and "from the role"
// answers why, which is the question anybody who has just set an identity's own
// and seen no change is actually asking.
//
// Off is a value rather than an absent interval. A cycle nobody has set and a cycle
// somebody has stopped look alike from the outside and are not alike at all: one is
// waiting for a default and the other is a decision.
type FleetPace struct {
	WakeAfter string `json:"wake_after,omitempty"`
	WakeEvery string `json:"wake_every,omitempty"`
	TendWatch string `json:"tend_watch,omitempty"`
	WakeOff   bool   `json:"wake_off,omitempty"`
	TendOff   bool   `json:"tend_off,omitempty"`
	// From says which layer set each value: `system`, `role`, or `identity`.
	// Absent where nothing set it and the built-in stands.
	From map[string]string `json:"from,omitempty"`
}

// FleetTariff is one line of the price list: what a setting costs, and what the
// fleet's own measurement would charge for it instead.
type FleetTariff struct {
	Setting string `json:"setting"`
	Weight  int    `json:"weight"`
	// Suggested and Measured are absent where nothing was observed, which is not
	// the same as a suggestion of zero: a combination nobody ran proposes nothing.
	Suggested int     `json:"suggested,omitempty"`
	Measured  float64 `json:"measured,omitempty"`
	Turns     int     `json:"turns,omitempty"`
}

// ActivityBucket is one hour of one identity's work, on one model at one effort.
//
// The numbers are totals and they only ever grow, which is the property the whole
// mirroring rests on: receiving the same bucket twice writes the same value, and a
// machine that missed six syncs delivers six buckets whose order does not matter.
// Nothing here has to arrive exactly once.
type ActivityBucket struct {
	At     string         `json:"at"`
	Model  string         `json:"model,omitempty"`
	Effort string         `json:"effort,omitempty"`
	Turns  int            `json:"turns,omitempty"`
	Tokens ActivityTokens `json:"tokens,omitzero"`
	Files  ActivityFiles  `json:"files,omitzero"`
}

// ActivityTokens is what a bucket cost. Four numbers rather than one, because on a
// real session they differ by five orders of magnitude and a single figure would be
// a cache-read figure wearing a general name.
type ActivityTokens struct {
	Input       int64 `json:"input,omitempty"`
	Output      int64 `json:"output,omitempty"`
	CacheCreate int64 `json:"cache_create,omitempty"`
	CacheRead   int64 `json:"cache_read,omitempty"`
	WebCalls    int64 `json:"web_calls,omitempty"`
}

// ActivityFiles is what a bucket touched.
type ActivityFiles struct {
	Reads      int   `json:"reads,omitempty"`
	Edits      int   `json:"edits,omitempty"`
	Writes     int   `json:"writes,omitempty"`
	ReadLines  int64 `json:"read_lines,omitempty"`
	Added      int64 `json:"added,omitempty"`
	Removed    int64 `json:"removed,omitempty"`
	WriteLines int64 `json:"write_lines,omitempty"`
	// Touched is distinct paths *within this bucket*. Summing it across buckets
	// over-counts a file worked on in two hours, and a screen showing a window has
	// to say so rather than print it as a fact.
	Touched int `json:"touched,omitempty"`
}

// New is what the turns caused to be produced: everything but the cache reads.
func (b ActivityBucket) New() int64 {
	return b.Tokens.Input + b.Tokens.Output + b.Tokens.CacheCreate
}

// Key is what makes two buckets the same bucket, and is what a merge writes on.
func (b ActivityBucket) Key() string { return b.At + "\x00" + b.Model + "\x00" + b.Effort }

// FleetRow is one line of a session's event feed.
//
// The path and never the content: a mirror carrying the text of every edit would be
// a second copy of the repository on a machine with no business holding one.
type FleetRow struct {
	At      string `json:"at"`
	Turn    int    `json:"turn,omitempty"`
	Kind    string `json:"kind"`
	Tool    string `json:"tool,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Blocked bool   `json:"blocked,omitempty"`
	Reason  string `json:"reason,omitempty"`
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

// FleetToolkit is one permission from the toolkit, and whether the fleet has it.
//
// The definition travels even when the fleet has it, so a browser can show what a
// missing one *would* be rather than naming an absence it cannot describe. Where the
// fleet has a permission of that name, what it actually contains is in Permissions —
// orc never rewrites one, so a fleet that redefined `upgrade` keeps its own.
type FleetToolkit struct {
	Name     string   `json:"name"`
	Floor    int      `json:"floor"`
	Patterns []string `json:"patterns,omitempty"`
	Why      string   `json:"why,omitempty"`
	Have     bool     `json:"have"`
}

// FleetWord is one word a clause may name: an orc verb, or a capability in
// another tool.
type FleetWord struct {
	Word string `json:"word"`
	Does string `json:"does,omitempty"`
	// In names the tool that checks it, and is empty for an orc verb.
	In string `json:"in,omitempty"`
}

// FleetVocabulary is what a clause may be written with.
//
// It rides along with the fleet so that the browser's clause editor can offer the
// words rather than keep its own copy. A copy of a privilege list goes stale
// silently — offering a verb the fleet stopped checking, or omitting one it
// started — and the fleet somebody is looking at is the authority on what it
// accepts. A fleet from an older Orc carries none, and the editor falls back to
// its syntax without the words.
type FleetVocabulary struct {
	Verbs []FleetWord `json:"verbs,omitempty"`
	Tools []FleetWord `json:"tools,omitempty"`
	// Innocuous is what `shell(…)` allows with no clause at all.
	//
	// The opposite of the two above: those are words a clause may use, this is
	// what an identity already has without one. It travels because a permission
	// list that omits it reads as more restrictive than the fleet is — the
	// commands nobody had to ask for being exactly the ones nothing mentions.
	Innocuous []string `json:"innocuous,omitempty"`
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
	if len(f.Sessions) > MaxListItems {
		return fault.Field("Fleet", "sessions", "%d sessions exceeds the limit of %d",
			len(f.Sessions), MaxListItems)
	}
	for i, sess := range f.Sessions {
		if err := sess.Validate(); err != nil {
			return fault.Field("Fleet", "sessions", "[%d]: %v", i, err)
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
	OpOrcEditPermission  Op = "orc.edit.permission"   // orc edit permission <name> [--floor n] [clauses…]
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
	// OpOrcWorkspace changes where an identity works: `orc workspace <identity>
	// <path> [--adopt]`.
	//
	// It carries `From` — where the operator saw the identity working — for the
	// same reason the library verbs carry `Base`. A snapshot is minutes old by the
	// time somebody acts on it, and a workspace is the one fleet value whose old
	// location still exists on disk afterwards: without the check, moving an agent
	// from the browser could quietly overturn a move somebody made on the machine
	// while it was in flight.
	OpOrcWorkspace Op = "orc.workspace" // orc workspace <identity> <path> [--adopt]
	// The standing instructions. Two operations rather than one with an empty
	// text, because clearing a layer and setting it to nothing are the same
	// outcome reached by different intents — and a queue whose report of what it
	// is about depends on whether a field is empty is one nobody can read.
	OpOrcInstructSet   Op = "orc.instruct.set"   // orc instruct <target> --set -
	OpOrcInstructClear Op = "orc.instruct.clear" // orc instruct <target> --clear
	OpOrcTend          Op = "orc.tend"           // orc tend
	// OpOrcToolkit installs the permissions every fleet is made with, on a fleet
	// that does not have all of them.
	//
	// `orc bootstrap` is the command, because that is where the toolkit is
	// installed and it is documented as safe to run again: an existing permission
	// of the same name is left exactly as it is, so a fleet that redefined one
	// keeps its own. It is here rather than as twelve queued `new permission`
	// actions because that is twelve chances to get one of them wrong, and
	// because orc's own table is the definition — cq repeating it would be a copy
	// that goes stale.
	OpOrcToolkit Op = "orc.toolkit" // orc bootstrap --as <operator>

	// OpOrcPace sets how often a fleet is woken and tended.
	//
	// One op for both cycles and all three layers, because it is one command with
	// operands: a verb per cycle per layer would be six that differ only in what
	// they name. `cycle` says which, `identity` or `role` says whose — neither
	// means the fleet's own — and the rest is the setting.
	OpOrcPace Op = "orc.pace" // orc pace <cycle> [<who>] [--after|--every|--watch|--off|--on|--clear]

	// OpOrcTariff changes what thinking costs, for every budget at once.
	//
	// One setting per action rather than the whole price list, because that is how
	// it is changed and because a whole-list write from a stale form would revert
	// whatever somebody else had set in between.
	OpOrcTariff Op = "orc.tariff" // orc tariff <setting> <n> --yes
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

// OpLibraryRoot points this machine at a different repository to mirror.
//
// It is the one operation that carries a path naming a *machine setting* rather
// than a file inside one, which the note on argRules for OpUpgrade says should
// not happen. The distinction that makes it allowable is what receives the path:
// the upgrade's would be handed to a build script, which is arbitrary code
// execution, and this one is handed to `filepath.EvalSymlinks` and a directory
// walk. It never becomes a command.
//
// What it does decide is the boundary the library verbs write inside, so the
// agent checks it hard before accepting it — see checkLibraryRoot. The checks run
// on the machine because that is the only place the path means anything: the
// server has no idea what exists there.
const OpLibraryRoot Op = "system.library"

// FleetOps are the verbs that go through Orc rather than Mailman or Macmuffin.
var FleetOps = []Op{
	OpOrcNewIdentity, OpOrcNewRole, OpOrcNewPermission, OpOrcEditPermission,
	OpOrcAssignRole, OpOrcAssignAuthority, OpOrcAssignPerm,
	OpOrcRemoveIdentity, OpOrcRemoveRole, OpOrcRemovePerm,
	OpOrcGrant, OpOrcRevoke, OpOrcMove,
	OpOrcEmploy, OpOrcFire, OpOrcBudget,
	OpOrcPoke, OpOrcRefresh, OpOrcTend, OpOrcToolkit, OpOrcPace, OpOrcTariff, OpOrcWorkspace,
	OpOrcInstructSet, OpOrcInstructClear,
}

// TouchesFleet reports whether an operation changes the Orc store.
func (o Op) TouchesFleet() bool { return slices.Contains(FleetOps, o) }

// fleetRules is the operand contract for each fleet verb, folded into argRules by
// protocol.go's initialiser.
var fleetRules = map[Op]argRule{
	OpOrcNewIdentity:   {identity: true},
	OpOrcNewRole:       {role: true, authority: true, description: true},
	OpOrcNewPermission: {permission: true, floor: true, patterns: true},
	// An edit carries the whole permission, because that is what it replaces:
	// a form that posted only the half somebody touched would wipe the other.
	OpOrcEditPermission:  {permission: true, floor: true, patterns: true},
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
	// Every operand is optional but the cycle: a form that clears a layer sends
	// nothing else, and one that only turns waking off sends no interval.
	OpOrcPace: {cycle: true, optIdentity: true, optRole: true, optPace: true},
	// The weight travels in `load`, which is already "a number a budget is about".
	// A second integer field meaning the same kind of thing would be one more to
	// keep in step.
	OpOrcTariff: {setting: true, load: true},
	// The operator's name, so the action does not depend on which OS user the
	// sync happens to run as.
	OpOrcToolkit: {identity: true},
	// `from` is required, not optional: a client that cannot say what it was
	// looking at is a client that cannot be protected from acting on a stale view.
	OpOrcWorkspace: {identity: true, workspace: true, from: true, optAdopt: true},
	// `prompt` carries which layer — the kind, and the name where there is one —
	// and `text` is what it becomes. The name is optional because the fleet's own
	// layer belongs to nobody.
	OpOrcInstructSet:   {prompt: true, text: true},
	OpOrcInstructClear: {prompt: true},
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
		// An identity is required by most verbs and optional for `pace`, whose
		// layer may be an identity, a role, or the fleet's own.
		optional := c.field == "identity" && rule.optIdentity
		switch {
		case c.want && c.value == "":
			return fault.Field("Action", "args."+c.field, "%s requires %s", a.Op, c.field)
		case !c.want && !optional && c.value != "":
			return unexpected(a.Op, c.field)
		}
		if optional && c.value != "" {
			if err := checkName("Action", "args."+c.field, c.value); err != nil {
				return err
			}
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
	if err := a.checkWorkspaceArgs(rule); err != nil {
		return err
	}
	return a.checkInstructArgs(rule)
}

// checkInstructArgs validates a standing instruction on its way into the queue.
//
// §6's bounds are enforced *here* as well as in Orc, and that is the point: an
// oversized prompt refused at the browser is a mistake somebody sees and fixes,
// while the same prompt refused after a sync is a failure sitting in a queue on a
// machine nobody is watching.
func (a Action) checkInstructArgs(rule argRule) error {
	if err := checkPace(a, rule); err != nil {
		return err
	}
	if err := checkSetting(a, rule); err != nil {
		return err
	}

	if !rule.prompt {
		if a.Args.Prompt != "" || a.Args.PromptName != "" || a.Args.Wake {
			return unexpected(a.Op, "prompt")
		}
		if !rule.text && a.Args.Text != "" {
			return unexpected(a.Op, "text")
		}
		return nil
	}

	switch a.Args.Prompt {
	case "system":
		if a.Args.PromptName != "" {
			return fault.Field("Action", "args.prompt_name", "the fleet's own layer belongs to nobody")
		}
	case "role", "identity":
		if a.Args.PromptName == "" {
			return fault.Field("Action", "args.prompt_name", "%s needs a name", a.Args.Prompt)
		}
		if err := checkName("Action", "args.prompt_name", a.Args.PromptName); err != nil {
			return err
		}
	default:
		return fault.Field("Action", "args.prompt", "%q is not a layer: it is system, role, or identity",
			a.Args.Prompt)
	}

	if !rule.text {
		if a.Args.Text != "" {
			return unexpected(a.Op, "text")
		}
		return nil
	}
	if a.Args.Text == "" {
		return fault.Field("Action", "args.text",
			"setting a layer to nothing is %s, which says what it means", OpOrcInstructClear)
	}

	// A wake message is a sentence and a layer is a document, so they are bounded
	// differently — the same two numbers Orc uses, checked before this is queued.
	limit := MaxPromptBytes
	what := "a prompt"
	if a.Args.Wake {
		limit, what = MaxWakeBytes, "a wake message"
	}
	if len(a.Args.Text) > limit {
		return fault.Field("Action", "args.text",
			"%s is %d bytes and the limit is %d; it would be refused on the agent machine after a sync",
			what, len(a.Args.Text), limit)
	}
	return nil
}

// absAnywhere reports whether a path is absolute on *some* platform.
//
// It cannot ask `path` or `path/filepath`, and this is the same trap `leaf` in
// the supervisor was written for: both answer for the host, and the host here is
// the server, which is not the machine the path is for. A cq server on Linux
// validating a queued action for a Windows agent would read `C:\srv\Orc` as
// relative and refuse a path that is perfectly absolute where it is going.
//
// So it accepts all three shapes and leaves the rest to the machine, which is the
// only place the question can really be settled:
//
//   - `/srv/Orc` — a unix path.
//   - `C:\srv\Orc` or `C:/srv/Orc` — a drive letter, either slash.
//   - `\\host\share` — a UNC path.
//
// The looseness is safe because this is not the check that decides anything. It
// rejects the obvious mistake early, on the machine somebody is typing at; the
// agent resolves the path for real and refuses what it will not accept.
func absAnywhere(p string) bool {
	if path.IsAbs(p) || strings.HasPrefix(p, `\\`) {
		return true
	}
	// A drive letter, then a colon, then a separator. `C:x` is relative to the
	// current directory *on* drive C, which is not an absolute path.
	if len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
		c := p[0] | 0x20 // lowercase, for ASCII letters
		return c >= 'a' && c <= 'z'
	}
	return false
}

// checkWorkspaceArgs validates `orc workspace`'s three operands.
//
// Both paths are required and both must be absolute. A relative one would mean a
// different directory depending on where the agent's sync happened to run, which is
// the one thing a queued action cannot be allowed to depend on — the machine that
// applies it is not the machine that wrote it.
func (a Action) checkWorkspaceArgs(rule argRule) error {
	for _, c := range []struct {
		want  bool
		field string
		value string
	}{
		{rule.workspace, "workspace", a.Args.Workspace},
		{rule.from, "from", a.Args.From},
	} {
		if !c.want {
			if c.value != "" {
				return unexpected(a.Op, c.field)
			}
			continue
		}
		if c.value == "" {
			return fault.Field("Action", "args."+c.field, "%s requires %s", a.Op, c.field)
		}
		if err := checkText("Action", "args."+c.field, c.value, MaxPatternRunes, false); err != nil {
			return err
		}
		if !absAnywhere(c.value) {
			return fault.Field("Action", "args."+c.field,
				"%s must be an absolute path; the machine that applies this is not the one that wrote it", c.field)
		}
	}
	if !rule.optAdopt && a.Args.Adopt {
		return unexpected(a.Op, "adopt")
	}
	return nil
}

// checkPace is `orc pace`'s operand contract.
//
// Everything but the cycle is optional, because the forms genuinely differ: one
// clears a layer and sends nothing else, one turns waking off and sends no
// interval, one sets both intervals at once. What is *not* allowed is a change
// that says nothing — a queued action that would run `orc pace wake` and report
// success for having done nothing is the worst kind of no-op, because the operator
// watched it succeed.
func checkPace(a Action, rule argRule) error {
	if !rule.cycle {
		if a.Args.Cycle != "" || a.Args.After != "" || a.Args.Every != "" ||
			a.Args.Watch != "" || a.Args.PaceOff || a.Args.PaceOn || a.Args.PaceClear {
			return unexpected(a.Op, "pace")
		}
		return nil
	}

	switch a.Args.Cycle {
	case "wake", "tend":
	default:
		return fault.Field("Action", "args.cycle",
			"%q is not a cycle; it is wake or tend", a.Args.Cycle)
	}
	if a.Args.Identity != "" && a.Args.Role != "" {
		return fault.Field("Action", "args.identity",
			"a layer belongs to an identity or to a role, not to both")
	}
	if a.Args.PaceOff && a.Args.PaceOn {
		return fault.Field("Action", "args.pace_off", "off and on are opposites; send one")
	}

	for _, got := range []struct {
		field string
		value string
		cycle string
	}{
		{"after", a.Args.After, "wake"},
		{"every", a.Args.Every, "wake"},
		{"watch", a.Args.Watch, "tend"},
	} {
		if got.value == "" {
			continue
		}
		if got.cycle != a.Args.Cycle {
			return fault.Field("Action", "args."+got.field,
				"%s belongs to %s, not to %s", got.field, got.cycle, a.Args.Cycle)
		}
		if err := checkDuration("Action", "args."+got.field, got.value); err != nil {
			return err
		}
	}

	if a.Args.After == "" && a.Args.Every == "" && a.Args.Watch == "" &&
		!a.Args.PaceOff && !a.Args.PaceOn && !a.Args.PaceClear {
		return fault.Field("Action", "args.cycle", "a pace that changes nothing is not a change")
	}
	return nil
}

// checkDuration refuses a value orc would refuse, so a queued action fails here —
// where somebody is looking at the form — rather than minutes later on a machine
// they cannot see.
func checkDuration(where, field, raw string) error {
	got, err := time.ParseDuration(raw)
	if err != nil {
		return fault.Field(where, field, "%q is not a duration like 20m", raw)
	}
	if got <= 0 {
		return fault.Field(where, field, "%q has nothing in it", raw)
	}
	if got > MaxPace {
		return fault.Field(where, field, "%q is longer than %s, which is not a cycle", raw, MaxPace)
	}
	return nil
}

// MaxPace is the longest interval worth calling one. A cycle that runs once a
// fortnight is a cycle nobody is relying on, and a typo — 20m written 20000h — is
// far more likely than the intention.
const MaxPace = 7 * 24 * time.Hour

// MinSyncPace is the tightest a mirror will sync.
//
// A mirror that syncs every second is a machine that spends its time telling
// another machine what it has not done. Ten seconds is short enough that a phone
// feels current and long enough that neither end is doing nothing else.
const MinSyncPace = 10 * time.Second

// checkSetting is `orc tariff`'s operand contract.
//
// The names are Orc's own and are checked here as well as there, so a weight the
// fleet would refuse never becomes a queued action that fails hours later on a
// machine nobody is watching. The *range* is Orc's to enforce: this end knows the
// vocabulary, and the far end knows what it currently charges.
func checkSetting(a Action, rule argRule) error {
	if !rule.setting {
		if a.Args.Setting != "" {
			return unexpected(a.Op, "setting")
		}
		return nil
	}
	if a.Args.Setting == "" {
		return fault.Field("Action", "args.setting", "%s requires a setting", a.Op)
	}
	if !slices.Contains(TariffSettings, a.Args.Setting) {
		return fault.Field("Action", "args.setting",
			"%q is not something a tariff prices; it is %s",
			a.Args.Setting, strings.Join(TariffSettings, ", "))
	}
	if a.Args.Load < 1 {
		return fault.Field("Action", "args.load",
			"a weight of %d is a session no budget can refuse", a.Args.Load)
	}
	return nil
}

// TariffSettings is what a tariff prices, in Orc's own words.
//
// A copy, and the drift is answered by checking rather than by hoping: cq cannot
// import Orc, and a browser offering a setting the fleet has never heard of would
// queue an action that fails on the far machine. The list is short and it changes
// when a model is added, which is exactly when somebody is already editing both.
var TariffSettings = []string{
	"haiku", "sonnet", "opus",
	"low", "medium", "high", "xhigh", "max",
	"crowd-base", "crowd-scale",
}
