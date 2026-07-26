package model

import "slices"

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

// Vocabulary is both lists together, for a caller that has to hand them on —
// `orc status --json` does, so that cq's browser can offer the words without
// keeping its own copy that drifts.
type Vocabulary struct {
	Verbs []OrcVerb
	Tools []Tool
}

// Words returns the vocabulary this build knows.
func Words() Vocabulary { return Vocabulary{Verbs: OrcVerbs(), Tools: Tools()} }
