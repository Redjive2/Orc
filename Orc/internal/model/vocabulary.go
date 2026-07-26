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
// agent can do at a prompt. It is short on purpose, and one test decides
// membership: **could this command, with any arguments, change anything or tell
// the agent something it does not already have?** If the answer is anything but
// a flat no, it is not on the list.
//
// So `echo` and `printf` are here — they write to a stream the agent already
// owns. `pwd`, `basename`, `dirname` are string arithmetic on a path the agent
// already knows. `true` and `false` are control flow.
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
//
// Nothing here takes a path, so nothing here can be turned into a file read by
// choosing a clever argument.
func Innocuous() []string {
	return []string{"basename", "dirname", "echo", "false", "printf", "pwd", "true"}
}

// InnocuousCommand reports whether a command name needs no clause at all.
func InnocuousCommand(name string) bool {
	return slices.Contains(Innocuous(), strings.ToLower(strings.TrimSpace(name)))
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
