package model

import (
	"slices"
	"strings"
)

// The words `orc(...)` and `tool(...)` can be written with.
//
// A path clause explains itself: somebody who knows the tree knows what
// `read(Anno/**)` covers, and if they are wrong the tree tells them. A verb clause
// does not. `orc(policy)` looks exactly as reasonable as `orc(assign)` and does
// nothing at all, because there is no verb by that name — and a clause that does
// nothing in a permission that is *supposed* to narrow reads as a control while
// being an absence.
//
// So the words are listed, here, once. `orc help` prints them, `orc status --json`
// carries them so cq's browser can offer them, and a test walks the tree to check
// that every verb which actually consults the gate is in this list and nothing
// else is. A list of privileges that quietly falls behind the code is the one
// people trust.

// OrcVerb is one verb `orc(...)` may name.
type OrcVerb struct {
	Verb string
	// Does is one line, in the imperative, for a cheat sheet.
	Does string
}

// OrcVerbs lists every verb that consults the `orc(...)` gate, in the order a
// reader wants them: making things, directing them, then taking them away.
//
// Only verbs that *change* something are here. `status`, `list`, `introspect`,
// `verify`, `doctor`, `tend`, and `budget` never consult the gate — reading a
// fleet you are already in is not a privilege, and `tend` runs implicitly under
// almost every other command — so naming one in a clause would read like a
// control and be nothing.
func OrcVerbs() []OrcVerb {
	return []OrcVerb{
		{"new", "create an identity, a role, or a permission"},
		{"assign", "give a role its authority, its permissions, or an identity its role"},
		{"edit", "rewrite a permission in place, for every holder at once"},
		{"move", "change who an identity works for"},
		{"employ", "put an agent on the work list, and spend budget on it"},
		{"fire", "take it off"},
		{"attach", "open a running session"},
		{"poke", "say something to a running agent"},
		{"refresh", "rewrite a session's settings from the fleet"},
		{"wake", "nudge sessions that have gone quiet"},
		{"pace", "set how often the fleet is woken and tended"},
		{"tariff", "change what thinking costs, for every budget at once"},
		{"model", "change what an identity runs on"},
		{"workspace", "change where an identity works"},
		{"instruct", "write the standing instructions agents run under"},
		{"grant", "hand a permission to an identity, temporarily"},
		{"revoke", "end a grant early"},
		{"remove", "delete an identity, a role, or a permission"},
	}
}

// OrcVerbNames is the same list, as words.
func OrcVerbNames() []string {
	out := make([]string, 0, len(OrcVerbs()))
	for _, v := range OrcVerbs() {
		out = append(out, v.Verb)
	}
	return out
}

// KnownOrcVerb reports whether a word is a verb the gate actually checks.
//
// Nothing refuses a clause naming an unknown verb — a fleet may be older or newer
// than the binary reading it, and refusing to parse a permission because this
// build has not heard of a verb would make an upgrade a fleet-wide outage. It is
// what `orc doctor` and cq's editor use to say "this allows nothing".
func KnownOrcVerb(word string) bool {
	return slices.Contains(OrcVerbNames(), word)
}

// Tool is one capability `tool(...)` may name.
type Tool struct {
	Name string
	// Does is one line, and In names the tool that checks it, because the whole
	// point of a tool clause is that the thing it governs is somewhere else.
	Does string
	In   string
}

// Tools lists the named capabilities other Orc tools ask about.
//
// Each one is a marker: `tool(upgrade)` is covered by `tool(upgrade)` and by no
// path glob, which is what keeps a floor-70 permission from reaching a floor-90
// capability. See the note on KindTool.
func Tools() []Tool {
	return []Tool{
		{"upgrade", "rebuild and restart every Orc tool, on every machine", "cq"},
	}
}

// ToolNames is the same list, as words.
func ToolNames() []string {
	out := make([]string, 0, len(Tools()))
	for _, t := range Tools() {
		out = append(out, t.Name)
	}
	return out
}

// KnownTool reports whether a word is a capability some tool actually asks about.
// Like KnownOrcVerb, it informs rather than refuses.
func KnownTool(word string) bool {
	return slices.Contains(ToolNames(), word)
}

// --- the shell ------------------------------------------------------------

// Innocuous is what an agent may run with no `shell(...)` clause at all.
//
// `shell` is deny by default, so this list is the whole of what an unprivileged
// agent can do at a prompt. It is short on purpose, and a command earns a place
// on it one of two ways.
//
// **It cannot do anything.** `echo` and `printf` write to a stream the agent
// already owns. `pwd`, `basename`, `dirname` are string arithmetic on a path it
// already knows. `true` and `false` are control flow. None of them takes a path,
// so none can be turned into a file read by choosing a clever argument.
//
// **It decides for itself, against the same identity.** `mailman` authenticates
// every command against the caller's own key and shows it its own mailbox and no
// other. A `shell(mailman)` clause would not be narrowing anything — mailman has
// already asked who is calling and answered accordingly — so requiring one would
// only mean an agent could be provisioned with a mailbox it was not allowed to
// open. Mail is how an agent is told what to do and how it says it is done; a
// fleet where that needs a grant is a fleet where a new identity is deaf until
// somebody notices.
//
// **The rest of the toolkit is on the list for the same reason.** `orc` and `muff`
// authenticate every command against `$ORC_USER`/`$ORC_KEY` and then apply their own
// rules to it: `orc` refuses a verb the caller may not run and an identity it does
// not control, `muff` refuses a task the caller does not own. A `shell(orc)` clause
// would narrow nothing they have not already decided — and without them a new agent
// cannot run `orc introspect`, which is the command that tells it what it may do.
// An agent that cannot ask what it is allowed to do is one that has to guess.
//
// `anno` and `dock` earn it a third way: they name the file they are about, right
// there in the command line, so the hook checks it against the identity's read and
// write clauses exactly as it would a Read or an Edit — see toolReads and annoWrites
// in the hook. That is what keeps them off the `cat` objection below. They are the
// token-efficient way to read a tree, and requiring a shell clause to run them while
// `read(**)` was already granted was the gate refusing something the clauses had
// already allowed.
//
// The exceptions are the parts that do *not* authenticate, guarded below so that
// running them needs a clause like anything else:
//
//   - `mailman admin`, which bootstraps a store with no identities in it and can
//     hand its caller the whole fleet's mail;
//   - `orc bootstrap`, which is the one orc command that runs without an identity
//     because it is what makes one;
//   - `orc env`, which prints an identity's key — `orc help env` says so in as many
//     words. Reading it for somebody else is how a fleet loses its keyring.
//
// And the near misses, because the reasons are the interesting part:
//
//   - `ls` discloses what is on a disk the agent may not read, so it is a grant,
//     not a default. It is the first thing most fleets will put in a clause.
//   - `cat`, `head`, `tail` read files, which is what `read(...)` governs — a
//     second path to the same thing, past the clause that was supposed to
//     decide it.
//   - `which` and `env` map the machine, which is reconnaissance.
//   - `sleep` cannot change anything and is still absent: it spends the one
//     resource a budget is denominated in.
func Innocuous() []string {
	return []string{
		"anno", "basename", "dirname", "dock", "echo", "false",
		"mailman", "muff", "orc", "printf", "pwd", "true",
	}
}

// guarded names the subcommands of a default command that the default does not
// reach, keyed by command.
//
// The shape recurs, which is why it is a table: a tool earns its place on the list
// by checking its own caller, and the parts of it that *cannot* check anything are
// the ones that provision the tool or hand out its credentials. Whatever is added
// next will have the same seam in the same place.
var guarded = map[string][]string{
	"mailman": {"admin"},
	// `bootstrap` makes a fleet and so runs before there is an identity to check;
	// `env` prints a key.
	"orc": {"bootstrap", "env"},
}

// GuardedSubcommands returns the subcommands a default command does not cover.
//
// Here rather than in each caller so that a refusal, the help, and cq's permission
// editor all say the same thing about which form of a default command still needs a
// clause — it is a fact that decides access, and three copies of it would be three
// chances to disagree.
func GuardedSubcommands(name string) []string {
	return guarded[strings.ToLower(strings.TrimSpace(name))]
}

// GuardedSubcommand is the first guarded subcommand, or the empty string.
func GuardedSubcommand(name string) string {
	if got := GuardedSubcommands(name); len(got) > 0 {
		return got[0]
	}
	return ""
}

// InnocuousWords is the default set as it should be shown to a person, with the
// carve-outs spelled out.
//
// Every place that prints the list uses this rather than Innocuous, because a
// list that says `mailman` while `mailman admin` is refused is worse than no
// list: the reader concludes the gate is broken and runs it again. There are
// three such places — `orc help permissions`, the hook's refusal, and the
// vocabulary `orc status --json` hands cq — and they must not each decide how to
// say it.
func InnocuousWords() []string {
	out := make([]string, 0, len(Innocuous()))
	for _, name := range Innocuous() {
		if subs := GuardedSubcommands(name); len(subs) > 0 {
			out = append(out, name+" (not "+name+" "+strings.Join(subs, ", not "+name+" ")+")")
			continue
		}
		out = append(out, name)
	}
	return out
}

// InnocuousRun reports whether an invocation needs no clause at all.
//
// It takes the arguments, not just the name, because `mailman` and `mailman
// admin` are not the same privilege. There is deliberately no name-only version
// exported: a caller holding one would have to remember to check the subcommand
// separately, and the failure mode of forgetting is that `mailman admin` runs
// unpermitted.
func InnocuousRun(name string, args []string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if !slices.Contains(Innocuous(), name) {
		return false
	}
	subs, ok := guarded[name]
	if !ok {
		return true
	}
	// The word anywhere in the arguments is enough, rather than the word in the
	// subcommand's position. Finding the position would mean knowing which global
	// flags take a separate value — `mailman --key x admin` puts `x` where the
	// subcommand goes — and a gate that has to track another tool's flags is a
	// gate that opens the day that tool gains one.
	//
	// So it over-matches: `mailman send --to admin` needs a clause it does not
	// really need. That is the right way round. A false positive costs somebody a
	// rephrase; a false negative hands out the whole fleet's mail.
	for _, arg := range args {
		for _, sub := range subs {
			if strings.EqualFold(strings.TrimSpace(arg), sub) {
				return false
			}
		}
	}
	return true
}

// Vocabulary is the lists together, for a caller that has to hand them on —
// `orc status --json` does, so that cq's browser can offer the words without
// keeping its own copy that drifts.
type Vocabulary struct {
	Verbs []OrcVerb
	Tools []Tool
	// Innocuous is what `shell` allows with no clause. The browser shows it
	// beside a shell clause, because a permission list that omits what everybody
	// already has is a list that reads as more restrictive than it is.
	Innocuous []string
}

// Words returns the vocabulary this build knows.
func Words() Vocabulary {
	return Vocabulary{Verbs: OrcVerbs(), Tools: Tools(), Innocuous: Innocuous()}
}
