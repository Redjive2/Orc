package cli

import (
	"fmt"
	"strings"

	"orc/common/fault"
	"orc/common/guess"

	"orc/orc/internal/model"
	"orc/orc/internal/store"
	"orc/orc/internal/style"
	"orc/theme"
)

// usage is the help text.
//
// It is painted rather than printed flat, because it is the screen a new operator
// reads first and the one an agent reads when it has no idea what it may do. Two
// things are in it that a bare command list would leave out: the model, in four
// lines, because every refusal Orc gives refers to it; and the environment, because
// an agent with no credential can still run `orc help`.
//
// It used to carry a third — a list of verbs not built yet — and that list is now
// empty, so it is gone. A help screen is the one place a reader trusts without
// checking, which makes a stale claim in it worse than the same claim anywhere else.
func usage(p style.Palette) string {
	var b strings.Builder

	line := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\n", args...)
	}

	// Padding is computed on the *plain* text and appended after the paint, never
	// by giving a painted string to %-44s. Escape sequences have no width but do
	// have length, so padding a painted string lays the help out one way with
	// colour and another way without — and the test that strips the escapes and
	// compares byte for byte is what found it.
	column := func(form, does string, paintForm, paintDoes func(string) string) {
		gap := 45 - theme.Width(form)
		if gap < 1 {
			gap = 1
		}
		fmt.Fprintf(&b, "  %s%s%s\n", paintForm(form), strings.Repeat(" ", gap), paintDoes(does))
	}
	verb := func(form, does string) {
		column(form, does, p.Command, func(s string) string { return s })
	}
	// There was a `soon` helper here, for the verbs a later milestone would bring. It
	// is gone because the list it served is empty: every verb in Docs/Orc/Reference.md
	// is built. A help screen that still said "not built yet" would be the one place a
	// reader trusts most, lying.

	line("%s — a minimal agentic orchestrator", p.Tool("orc"))
	line("")
	line("%s", p.Header("the fleet"))
	verb("orc bootstrap [--as <name>]", "make the fleet and its operator")
	verb("orc new identity <name>", "hire an agent, under you")
	verb("orc new role <name> <authority> <what it is>", "create a job")
	verb("orc new permission <name> <floor> <patterns…>", "create a named set of clauses")
	verb("orc remove identity|role|permission <name>", "delete one; --from <role> narrows instead")
	line("")
	line("%s", p.Header("who may what"))
	verb("orc assign role <identity> <role>", "give an agent its job (replaces)")
	verb("orc assign authority <role> <authority>", "change what a role asks for")
	verb("orc assign permission <role> <permission>", "add a permission to a role")
	verb("orc edit permission <name> [--floor n] [clauses…]", "change one in place, for every holder")
	verb("orc grant permission <who> <perm> [--until <d>]", "hand one over temporarily")
	verb("orc revoke permission <who> <perm>", "end a grant early")
	verb("orc move <identity> <boss>", "re-parent; the subtree is re-capped")
	line("")
	line("%s", p.Header("running them"))
	verb("orc budget", "what each identity may employ, and what it spends")
	verb("orc budget <role> <load>", "set the load a role may keep employed")
	verb("orc model <identity> [<model>] [--effort e]", "what it thinks with; --now restarts it")
	verb("orc workspace <identity> [<path>] [--adopt]", "where it works; --now restarts it there")
	verb("orc instruct [<target>] [--set f|--edit|--clear]", "the standing instructions agents run under")
	verb("orc instruct show <identity> [--diff]", "what an agent is actually told, composed")
	verb("orc employ <identity> [--model m] [--effort e]", "put it on the work list and start it")
	verb("orc fire <identity> [--yes]", "take it off; --yes if a session is live")
	verb("orc tend [--watch <dur>]", "reconcile the work list with what is running")
	verb("orc view <identity>", "what it has been doing, without joining it")
	verb("orc attach <identity>", "join it: type, ^S sends, ^Q leaves")
	verb("orc attach <identity> --direct", "hand your terminal over; Ctrl-\\ then d leaves")
	verb("orc poke <identity> [message]", "type into it without attaching")
	verb("orc wake [<identity>…] [--every <dur>]", "poke whatever has gone quiet, on a cycle")
	verb("orc refresh <identity>", "new session, fresh context, same identity")
	line("")
	line("%s", p.Header("reading it"))
	verb("orc status [<identity>] [--json]", "the fleet, or one card")
	verb("orc activity [<identity>] [--since <dur>]", "what it is doing, and what it has cost")
	verb("orc pace wake [<who>] [--after <dur>] [--every <dur>]", "how long silence lasts, and how often to look")
	verb("orc pace tend [<who>] [--watch <dur>]", "how often the worklist is reconciled")
	verb("orc tariff [<setting> <n>]", "what thinking costs, for every budget at once")
	verb("orc tariff --calibrate", "what measurement suggests those weights should be")
	verb("orc tariff --clear --yes", "back to the built-in prices")
	verb("orc list identities|roles|permissions|grants", "the rosters; --json for any of them")
	verb("orc introspect [--only <field>] [--json]", "who am I, and what may I do")
	verb("orc check-control <agent>", "exit 0 if you control it, 8 if not")
	verb("orc check-permission <name>", "exit 0 if you hold it, 8 if not")
	verb("orc env <identity>", "the export block — discloses a key")
	verb("orc verify", "walk the store and report damage")
	verb("orc prose <path…>", "measure writing against the house rule")
	verb("orc doctor", "which guards are in force, and which are not")
	line("")
	line("%s", p.Header("yours"))
	verb("orc owner", "who the operator is, and how orc knows it is you")
	verb("orc owner env", "the operator's export block, found for you")
	verb("orc owner rename <name> --yes", "rename the operator; keeps its key and memories")
	verb("orc owner reset --yes", "destroy the fleet and bootstrap a fresh one")
	line("")
	line("  %s", p.Muted("with no $ORC_USER and no $ORC_KEY, orc reads the operator's credential from"))
	line("  %s", p.Muted("the fleet itself — it is your directory, at 0700, so exporting a key it can"))
	line("  %s", p.Muted("already read would be friction rather than security. an agent always presents"))
	line("  %s", p.Muted("its own, and a half-set environment stays an error."))
	line("")

	line("%s", p.Header("the model"))
	line("  authority is a number on a %s; the operator has %s and everyone else 1-99.",
		p.Value("role"), p.Authority("100"))
	line("  a %s is a named set of clauses with a floor: only an identity at or above",
		p.Value("permission"))
	line("  that floor can hold it. an identity holds exactly one role, plus whatever has")
	line("  been %s to it directly — and every grant lapses.", p.Value("granted"))
	line("  nothing effective is stored: an identity's authority is the lower of its role's")
	line("  and its boss's, and its permissions are its role's intersected with its boss's,")
	line("  so %s re-caps a whole subtree by appending one line.", p.Command("orc move"))
	line("")

	line("%s", p.Header("patterns"))
	// Padded from the widest example rather than by hand: the column is what makes
	// five one-line definitions readable as a table, and a hand-counted gap goes
	// wrong the first time an example changes.
	for _, row := range [][2]string{
		{"read(Anno/**)", "a path glob; ** crosses directories"},
		{"write(Anno/internal/**)", "the same, for editing"},
		{"spawn(24)", "how much thinking may be employed at once (see below)"},
		{"orc(assign)", "narrows which orc verbs a role may run"},
		{"shell(ls cat)", "which commands may be run — shut without one"},
		{"tool(upgrade)", "a named capability in another tool"},
	} {
		line("  %s%s%s", p.Value(row[0]), strings.Repeat(" ", 25-theme.Width(row[0])), row[1])
	}
	line("  kinds: %s", p.Muted(kindList()))
	line("")
	line("  one clause, several things: %s", p.Value("read(Anno/** Dock/**)"))
	line("  and what it does not cover:  %s", p.Value("write(** except Docs/**)"))
	line("  every kind but %s takes both, and every term is a glob — %s and",
		p.Value("spawn"), p.Value("orc(re*)"))
	line("  %s are patterns in the sense %s is. quote a clause with a", p.Value("tool(**)"), p.Value("read(Anno/**)"))
	line("  space in it, or the shell hands it over in halves.")
	line("")
	line("  %s %s", p.Setting("orc verbs:"), p.Muted(strings.Join(model.OrcVerbNames(), " ")))
	line("  %s %s", p.Setting("tools:    "), p.Muted(strings.Join(model.ToolNames(), " ")))
	// The one kind that refuses by default, so the one whose absence has to be
	// printed: every other list here says what a clause could add.
	line("  %s %s", p.Setting("no clause:"), p.Muted(strings.Join(model.InnocuousWords(), " ")))
	line("  %s runs nothing but those. a line that hides what it runs —", p.Value("shell"))
	line("  %s, %s, %s — needs %s.",
		p.Value("$(…)"), p.Value("sh -c"), p.Value("xargs"), p.Value("shell(**)"))
	line("  a clause naming anything else parses and controls nothing, which is what")
	line("  %s is built on: its clause allows nothing, and holding it bars", p.Value("orc-read"))
	line("  every verb above.")
	line("")

	line("%s", p.Header("the toolkit"))
	line("  every fleet is made with these. ordinary permissions — assign them, grant")
	line("  them, remove them; the floor is who may hold one at all.")
	for _, row := range toolkitRows() {
		gap := 14 - theme.Width(row[0])
		if gap < 1 {
			gap = 1
		}
		fmt.Fprintf(&b, "  %s%s%s%s\n", p.Permission(row[0]), strings.Repeat(" ", gap),
			p.Authority(row[1]), p.Muted("  "+row[2]))
	}
	line("  %s", p.Muted("`orc list permissions` shows their clauses and who holds them"))
	line("")

	line("%s", p.Header("load"))
	line("  a session's load is model × effort: %s · %s",
		p.Value("haiku 1 sonnet 2 opus 3"), p.Value("low 1 medium 2 high 3 xhigh 4 max 6"))
	line("  a fleet is charged for being a fleet: total = ⌈ Σ load × (9 + count) / 10 ⌉")
	line("  so four sonnet/medium agents cost 21, not 16. %s spends it, over everything",
		p.Command("orc employ"))
	line("  you employ transitively, so employing through a subordinate is not a way round.")
	line("")

	line("%s", p.Header("environment"))
	for _, row := range [][2]string{
		{"$ORC_USER", "the identity to act as"},
		{"$ORC_KEY", "the key that proves it — orc verifies this, every command"},
		{"$ORC_HOME", "the fleet's store; else $XDG_DATA_HOME/orc, else ~/.orc"},
		{"$ORC_THEME", "macchiato|mocha|frappe|latte|none"},
		{"$NO_COLOR, $ORC_AGENT", "no colour at all"},
	} {
		gap := 25 - theme.Width(row[0])
		if gap < 1 {
			gap = 1
		}
		fmt.Fprintf(&b, "  %s%s%s\n", p.Setting(row[0]), strings.Repeat(" ", gap), row[1])
	}
	line("")
	line("%s  0 ok · 1 usage · 2 not found · 4 parse · 5 i/o · 6 conflict · 7 auth", p.Header("exit"))
	line("        8 denied · 11 escape · 70 internal")
	line("")
	line("%s says who you are. %s is the whole fleet.",
		p.Command("orc introspect"), p.Command("orc status"))
	line("%s takes: %s", p.Command("introspect --only"), p.Muted(fieldList()))

	return strings.TrimRight(b.String(), "\n")
}

// toolkitRows is the toolkit, for the help screen: name, floor, and one line.
//
// Read from the store's own table rather than written out again, so a permission
// added to the toolkit appears here without anybody remembering to — the screen
// somebody trusts is the one most worth keeping honest.
func toolkitRows() [][3]string {
	got, err := store.Toolkit()
	if err != nil {
		// Unreachable: the table is constants, checked by a test. An empty list is
		// a help screen missing a section, which is better than a crash.
		return nil
	}
	rows := make([][3]string, 0, len(got))
	for _, b := range got {
		rows = append(rows, [3]string{b.Name.String(), fmt.Sprintf("%3d", b.Floor.Int()), b.Why})
	}
	return rows
}

// fieldList is what `orc introspect --only` accepts, for the help and for the
// error that names them. It reads from the same place the resolver does, so the
// two cannot drift.
func fieldList() string { return strings.Join(fields(), " ") }

// kindList names the pattern kinds, for the same reason.
func kindList() string {
	out := make([]string, 0, len(model.Kinds()))
	for _, k := range model.Kinds() {
		out = append(out, k.String())
	}
	return strings.Join(out, " ")
}

// brief is what `orc` on its own prints: the verbs, grouped, and nothing else.
//
// It is separate from usage() rather than a prefix of it because they answer
// different questions. Somebody who typed `orc` wants to know what the verbs are;
// somebody who typed `orc help` has already decided to read. Printing the second
// answer to the first question is how a help screen becomes something people
// scroll past — and Orc's is a hundred lines, most of it the model.
func brief(p style.Palette) string {
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	group := func(name string, verbs ...string) {
		painted := make([]string, len(verbs))
		for i, v := range verbs {
			painted[i] = p.Command(v)
		}
		// Padding on the plain name, appended after the paint — the same rule the
		// rest of this file follows, and for the same reason.
		gap := 14 - theme.Width(name)
		if gap < 1 {
			gap = 1
		}
		line("  %s%s%s", p.Header(name), strings.Repeat(" ", gap), strings.Join(painted, p.Muted(" · ")))
	}

	line("%s — a minimal agentic orchestrator", p.Tool("orc"))
	line("")
	group("the fleet", "bootstrap", "new", "remove")
	group("who may what", "assign", "grant", "revoke", "move")
	group("running them", "employ", "fire", "tend", "pace", "tariff", "budget", "view", "attach", "poke", "refresh")
	group("reading it", "status", "list", "activity", "introspect", "check-control", "check-permission",
		"env", "verify", "doctor", "prose")
	group("yours", "owner")
	line("")
	line("  %s", p.Muted("orc help for all of it: every form, the model, the patterns, the load table"))
	return strings.TrimRight(b.String(), "\n")
}

// verbs is every command orc answers to, for suggesting the one that was meant.
// It is written out rather than derived from the dispatch switch because a Go
// switch is not introspectable; the colour test walks both and fails if they
// disagree.
func verbs() []string {
	return []string{
		"bootstrap", "new", "assign", "remove", "grant", "revoke", "move",
		"status", "list", "activity", "budget", "model", "introspect", "check-control", "check-permission", "env", "verify",
		"doctor", "owner", "employ", "fire", "tend", "view", "attach", "poke", "wake", "refresh",
		"workspace", "instruct", "pace", "tariff", "help",
	}
}

// topic is what `orc help <command>` shows: the forms, and what the command is *for*.
//
// The forms are repeated from usage() rather than shared with it, because usage is a
// hand-set page in five groups and threading a table through it would cost more than
// it saved. A test keeps the repetition honest: every verb has a page, every page's
// canonical form is on the screen, and neither list may change without the other.
type topic struct {
	forms    []string
	does     string
	detail   string
	examples []string
}

var topics = map[string]topic{
	"bootstrap": {
		forms: []string{"orc bootstrap [--as <name>]"},
		does:  "make the fleet and its operator",
		detail: "The one command that runs without a fleet, because it is what makes one:\n" +
			"the store, the operator identity at authority 100, and a mailbox in\n" +
			"Mailman provisioned with the same key.\n\n" +
			"The key is printed once and cannot be recovered. That is the whole of\n" +
			"setup; everything else is `orc new identity`.",
		examples: []string{"orc bootstrap", "orc bootstrap --as redjive2"},
	},
	"new": {
		forms: []string{
			"orc new identity <name>",
			"orc new role <name> <authority> <what it is>",
			"orc new permission <name> <floor> <patterns…>",
		},
		does: "hire an agent, create a job, or name a set of clauses",
		detail: "An identity is hired *under you*: the tree is who reports to whom, and\n" +
			"it is what every permission is capped by. A role is a job — authority\n" +
			"and a set of permissions — and an identity holds exactly one.\n\n" +
			"A permission is a named set of clauses with an authority floor, so only\n" +
			"an agent at or above it can hold one.",
		examples: []string{
			"orc new identity ember",
			"orc new role engineer 60 'writes code in one package'",
			"orc new permission edit-anno 40 'write(Anno/**)'",
		},
	},
	"check-permission": {
		forms: []string{"orc check-permission <name>"},
		does:  "exit 0 if you hold it, 8 if not",
		detail: "The sibling of `check-control`, for the other kind of question. It\n" +
			"exists so another tool can ask Orc about authority instead of\n" +
			"deciding for itself: `muff assign` asks about ancestry, and\n" +
			"`cq upgrade` asks about a permission.\n\n" +
			"A number rather than a screen, so a shell can branch on it. A\n" +
			"permission this fleet does not have is `2` rather than `8` — that is\n" +
			"a fact about the store, not a refusal about policy.\n\n" +
			"`upgrade` is builtin: every fleet has it, at floor 90, and holding it\n" +
			"is what lets an agent rebuild and restart every tool on every machine.",
		examples: []string{"orc check-permission upgrade"},
	},
	"remove": {
		forms: []string{"orc remove identity|role|permission <name> [--from <role>]"},
		does:  "delete one; --from <role> narrows instead",
		detail: "Refused while the thing is in use: an identity with a live session, a\n" +
			"role somebody holds, a permission a role still lists. The refusal names\n" +
			"what is holding it.\n\n" +
			"--from takes a permission off one role rather than deleting it\n" +
			"everywhere, which is almost always what was meant.",
		examples: []string{"orc remove identity ember", "orc remove permission edit-anno --from engineer"},
	},
	"assign": {
		forms: []string{
			"orc assign role <identity> <role>",
			"orc assign authority <role> <authority>",
			"orc assign permission <role> <permission>",
		},
		does: "give an agent its job, or change what a job asks for",
		detail: "`assign role` replaces: an identity holds one role, so giving it another\n" +
			"takes the first away.\n\n" +
			"Authority and permissions are set on the *role*, not the identity —\n" +
			"which is what makes a fleet legible, and what `grant` exists to make an\n" +
			"exception to.",
		examples: []string{"orc assign role ember engineer", "orc assign permission engineer edit-anno"},
	},
	"grant": {
		forms: []string{"orc grant permission <identity> <permission> [--until <dur>]"},
		does:  "hand one over temporarily",
		detail: "A permission for one identity, outside its role, and always temporary:\n" +
			"without --until it lapses with the session. A grant that outlived what\n" +
			"it was for would become a role nobody wrote down.\n\n" +
			"It is still capped by the boss: nobody grants what they do not have.",
		examples: []string{"orc grant permission ember edit-common --until 2h"},
	},
	"revoke": {
		forms: []string{"orc revoke permission <identity> <permission>"},
		does:  "end a grant early",
		detail: "Ends a grant before it lapses. It does not touch the role — a permission\n" +
			"an agent has through its job is revoked by changing the job.",
		examples: []string{"orc revoke permission ember edit-common"},
	},
	"move": {
		forms: []string{"orc move <identity> <boss>"},
		does:  "re-parent; the subtree is re-capped",
		detail: "Changes who an agent reports to, and with it what the whole subtree\n" +
			"beneath it may do: authority and permissions are the lower of a role's\n" +
			"and its boss's, so one line changes a branch.\n\n" +
			"A move that would make a cycle is refused before it is written.",
		examples: []string{"orc move ember atlas"},
	},
	"budget": {
		forms: []string{"orc budget", "orc budget <role> <load>"},
		does:  "what each identity may employ, and what it spends",
		detail: "Load is a budget in units of thinking: a session costs its model weight\n" +
			"times its effort weight, and a fleet is charged for being a fleet.\n\n" +
			"With no arguments it reports; with a role and a number it sets what that\n" +
			"role may keep employed.",
		examples: []string{"orc budget", "orc budget engineer 12"},
	},
	"instruct": {
		forms: []string{
			"orc instruct",
			"orc instruct <target> [--set <file>] [--edit] [--clear]",
			"orc instruct show <identity> [--diff]",
		},
		does: "the standing instructions agents run under",
		detail: "A target is `system`, `role <name>`, `identity <name>`, or `wake` —\n" +
			"and `wake role <name>` or `wake identity <name>` for one of those.\n\n" +
			"Four kinds, and they are two mechanisms rather than four of a kind.\n\n" +
			"**system, role, identity** are prompt layers. They compose *additively* —\n" +
			"the fleet's, then the role's, then the agent's, each under a heading\n" +
			"naming where it came from — and an agent gets the result at the start of\n" +
			"a session. An identity layer adds to its role's and cannot replace it:\n" +
			"the fleet's layer is the floor, not a default.\n\n" +
			"**wake** is a message, not a layer: what is typed at an agent that has\n" +
			"nothing to do. `orc wake` says it to one that has gone quiet, and\n" +
			"`employ` and `refresh` say it to one that has just started — a session\n" +
			"nobody has spoken to has no turn in which to act on the layers above.\n" +
			"Those *override* — the agent's, else its role's, else the fleet's, else\n" +
			"`continue` — because a wake message is one thing you say, and three of\n" +
			"them stapled together is not a message.\n\n" +
			"With no arguments it lists every layer, its size, and when it last\n" +
			"changed. `show` prints what an agent actually gets, which is the question\n" +
			"this feature generates most; `--diff` says whether a running session\n" +
			"started on what is set now.\n\n" +
			"The fleet's layer is the operator's alone, because it reaches every\n" +
			"agent. A role's or an agent's needs the `instruct` permission, and an\n" +
			"agent's also needs ancestry: you may instruct what you control.\n\n" +
			"**A prompt is not a permission.** \"do not edit Communique\" is a request;\n" +
			"`write(Anno/**)` is a rule the hook enforces on every tool call. Using\n" +
			"the first where you needed the second fails silently.",
		examples: []string{
			"orc instruct",
			"orc instruct system --edit",
			"orc instruct identity ember --set brief.md",
			"orc instruct wake --set nudge.md",
			"orc instruct show ember",
		},
	},
	"workspace": {
		forms: []string{"orc workspace <identity>", "orc workspace <identity> <path> --adopt [--now]"},
		does:  "where it works; --now restarts it there",
		detail: "With no path it says where the identity works, and whether that is the\n" +
			"directory orc made for it or one somebody chose.\n\n" +
			"With a path it points the identity there. `--adopt` works in a directory\n" +
			"that already exists — a checkout, a worktree, a tree somebody else made —\n" +
			"and is the form that is built. Moving an agent's *files* to a new path is\n" +
			"not: make the directory, put what it needs in it, and adopt it.\n\n" +
			"A workspace inside the fleet's own store is refused — it holds every\n" +
			"identity's key — and so is one inside another agent's workspace, which\n" +
			"would make two scopes overlap where nobody said so.\n\n" +
			"A session's working directory is fixed when Claude starts, so a running\n" +
			"agent keeps writing to the old one until it is replaced. That matters more\n" +
			"here than for a model change: its permissions are compiled against the new\n" +
			"path. `--now` replaces the session, and loses its context.",
		examples: []string{
			"orc workspace ember",
			"orc workspace ember /Users/me/trees/parser --adopt",
			"orc workspace ember /Users/me/trees/parser --adopt --now",
		},
	},
	"model": {
		forms: []string{"orc model <identity>", "orc model <identity> <model> [--effort <e>] [--now]"},
		does:  "what it thinks with; --now restarts it",
		detail: "With no model it reports what the identity is on and what that costs.\n" +
			"With one it changes it, through the same budget arithmetic `employ` uses:\n" +
			"load is model weight times effort weight, so moving an agent to opus is\n" +
			"spending, and the boss has to be able to afford it.\n\n" +
			"A model is fixed when Claude starts, so a running session keeps the one\n" +
			"it was launched with. By default the change waits for the next session\n" +
			"and the command says so; --now replaces the session immediately, which\n" +
			"loses its context.\n\n" +
			"Retuning an identity that is not employed costs nothing and does not\n" +
			"employ it — it is what the next `orc employ` will start it on.",
		examples: []string{
			"orc model ember",
			"orc model ember opus --effort high",
			"orc model ember haiku --now",
		},
	},
	"employ": {
		forms: []string{"orc employ <identity> [--model <m>] [--effort <e>]"},
		does:  "put it on the work list and start it",
		detail: "Adds the identity to the work list and populates it: a supervisor\n" +
			"process, a pty, and a Claude session with the identity's own credential,\n" +
			"workspace, and compiled settings.\n\n" +
			"Refused when the boss cannot afford the load. The work list is the\n" +
			"intent; `tend` is what makes reality match it.",
		examples: []string{"orc employ ember", "orc employ ember --model opus --effort high"},
	},
	"fire": {
		forms: []string{"orc fire <identity> [--yes]"},
		does:  "take it off; --yes if a session is live",
		detail: "Takes the identity off the work list and stops its session. The identity\n" +
			"itself survives — its memories, mailbox, tasks, and workspace are all\n" +
			"still there, and `employ` starts it again.\n\n" +
			"--yes is required when a session is live, which for an agent is always.",
		examples: []string{"orc fire ember --yes"},
	},
	"tend": {
		forms: []string{"orc tend [--watch <dur>]"},
		does:  "reconcile the work list with what is running",
		detail: "Starts what is employed and not running, and stops what is running and\n" +
			"not employed. It is the backstop: every command that changes the work\n" +
			"list already does its own half, and this is what fixes a session that\n" +
			"died while nobody was looking.\n\n" +
			"--watch keeps reconciling on an interval.",
		examples: []string{"orc tend", "orc tend --watch 5m"},
	},
	"attach": {
		forms: []string{"orc attach <identity>", "orc attach <identity> --direct"},
		does:  "join a session: watch it, and say things to it",
		detail: "Orc's composed pane: what the session has done, built from orc's event\n" +
			"journal and the transcript rather than by parsing a terminal.\n" +
			"\n" +
			"Typing composes and **^S sends**, so a stray keystroke cannot land in a\n" +
			"working session — and ^Q leaves in one key. It is not read-only; the\n" +
			"buffer is how you talk to the agent.\n" +
			"\n" +
			"What it cannot show is what Claude's own interface is *drawing*: the\n" +
			"pane is built from events, so anything rendered before the first one, or\n" +
			"drawn rather than recorded, is not in it. **^] hands the terminal over**\n" +
			"when that is what you need, without leaving and running a second command.\n" +
			"\n" +
			"--direct starts there instead: your terminal wired to the real session,\n" +
			"everything it draws exactly as it draws it. **To leave that, press\n" +
			"Ctrl-\\ and then d.** Two keystrokes, not one: every single key belongs\n" +
			"to the session — ^C interrupts the agent, ^D ends its input — so a prefix\n" +
			"is the only safe shape. `q` and `.` work in place of `d`. The session\n" +
			"keeps running either way; leaving an attach never stops an agent.\n" +
			"\n" +
			"--view is accepted and means the default, which is what it always\n" +
			"selected.",
		examples: []string{"orc attach ember", "orc attach ember --direct"},
	},
	"view": {
		forms: []string{"orc view <identity>", "orc view <identity> --lines 50 --json"},
		does:  "what it has been doing, without joining it",
		detail: "The same two sources `attach` draws — orc's event journal and Claude's\n" +
			"transcript — printed once and done with. Nothing is opened, nothing is\n" +
			"written, and no terminal is taken over, so it is safe to run against an\n" +
			"agent mid-turn and it works down a pipe.\n\n" +
			"Use it to check on several agents in turn, or to find out why one went\n" +
			"quiet. `attach` is the one to sit down with; this is the one to glance at.\n\n" +
			"--lines changes how much of each source is shown. --json is the same\n" +
			"thing for another program, and is what cq reads to show a session in the\n" +
			"browser.",
		examples: []string{"orc view ember", "orc view ember --lines 50"},
	},
	"poke": {
		forms: []string{"orc poke <identity> [message]"},
		does:  "type into it without attaching",
		detail: "Writes a message into the session's input, which is the \"nudge it to\n" +
			"continue\" of the reference. The default is `continue`.\n\n" +
			"A poke to a session mid-turn queues in Claude's own input box, which is\n" +
			"the correct behaviour and is what the command says it did.",
		examples: []string{"orc poke ember", `orc poke ember "the tests are green now"`},
	},
	"tariff": {
		forms: []string{
			"orc tariff",
			"orc tariff <setting> <n> [--yes]",
			"orc tariff --calibrate",
			"orc tariff --clear --yes",
		},
		does: "what thinking costs, for every budget at once",
		detail: "A session costs model × effort, and a set of n costs\n" +
			"⌈sum × (crowd-base + n) / crowd-scale⌉ — so the tenth agent costs more\n" +
			"than the first and a fleet is charged for being a fleet.\n\n" +
			"Those weights are a judgement about *money*: opus costing three haikus\n" +
			"is a claim no code can settle, and two fleets can disagree without\n" +
			"either being wrong. They were constants; this stores them, journaled\n" +
			"like a permission, so the question \"what did it used to be, and who\n" +
			"changed it\" has an answer.\n\n" +
			"Every budget is derived, so a change is felt by everything at once:\n" +
			"raising `opus` re-prices every running opus session, and an actor inside\n" +
			"its budget can be over it without anybody touching that actor. It says\n" +
			"who that would be, and asks for --yes when the list is not empty. It\n" +
			"refuses nothing: a fleet over its own budget is information, and a tariff\n" +
			"that could only be loosened while agents ran would be one nobody could\n" +
			"tighten.\n\n" +
			"`--calibrate` proposes weights from what the fleet actually spent, over\n" +
			"the last week, counting new tokens only — a tariff that counted cache\n" +
			"reads would be pricing context rather than work. It proposes and never\n" +
			"applies: the numbers are one fleet over one window, and deciding from\n" +
			"them is the judgement this feature exists to leave to a person.",
		examples: []string{
			"orc tariff",
			"orc tariff opus 4",
			"orc tariff --calibrate",
		},
	},
	"pace": {
		forms: []string{
			"orc pace",
			"orc pace wake [<identity>|<role>] [--after <dur>] [--every <dur>]",
			"orc pace tend [<identity>|<role>] [--watch <dur>]",
			"orc pace <cycle> <who> [--off|--on|--clear]",
		},
		does: "how often the fleet is woken and tended",
		detail: "`orc wake --after`, `orc wake --every` and `orc tend --watch` are flags,\n" +
			"read once when a process starts — so nothing but the person who started\n" +
			"that process could change them, and a browser could not offer them at\n" +
			"all. This stores them instead.\n\n" +
			"They layer the way a wake message does: the identity's, else its role's,\n" +
			"else the fleet's, else the built-in. `orc pace` with no arguments shows\n" +
			"what each agent will actually do and where each value came from.\n\n" +
			"Every cycle re-reads at the top of a pass, so a change lands on the next\n" +
			"one with nothing to restart. Two rules about flags, and they differ on\n" +
			"purpose: `--after` typed on the line wins for that run, because somebody\n" +
			"debugging is deciding about the run in front of them — and a stored\n" +
			"*interval* wins over the flag a loop was started with, because a cycle\n" +
			"running since Tuesday in a shell nobody has open is exactly what a stored\n" +
			"setting has to be able to reach.\n\n" +
			"`--off` is a state and not a zero: an agent nobody is waking looks\n" +
			"different from one being woken and not answering. `--on` turns back on\n" +
			"what a layer above turned off, and `--clear` drops this layer entirely.",
		examples: []string{
			"orc pace",
			"orc pace wake --after 20m --every 5m",
			"orc pace wake ember --after 2m",
			"orc pace tend --watch 30s",
			"orc pace wake ember --off",
		},
	},
	"activity": {
		forms: []string{
			"orc activity [<identity>]",
			"orc activity [--since <dur>] [--json]",
		},
		does: "what it is doing, and what it has cost",
		detail: "One line per agent: what it is doing now — generating, waiting, stuck,\n" +
			"down, idle — and what it has done in the window: turns, tokens, and the\n" +
			"files and lines it read and wrote. The default window is 24h.\n\n" +
			"It reads before it reports, so the figures are current rather than as\n" +
			"stale as the last thing that happened to run `tend`. What it reads is\n" +
			"claude's own transcript, incrementally: a cursor records where the last\n" +
			"pass stopped, so a long session costs one read and not one per command.\n\n" +
			"**new** is input + output + cache writes — what the turns caused to be\n" +
			"produced. **cached** is what was read back out of the cache, and it is\n" +
			"kept separate because on a real session the two differ by five orders of\n" +
			"magnitude and one column called `tokens` would only ever show the second.\n\n" +
			"Files are counted from orc's own event feed and lines from the\n" +
			"transcript, so a file count is always right and a line count is missing\n" +
			"where claude's file could not be read. Nothing here is a privilege:\n" +
			"reading what a fleet you are in has done is not one.",
		examples: []string{
			"orc activity",
			"orc activity ember --since 7d",
			"orc activity --json",
		},
	},
	"wake": {
		forms: []string{
			"orc wake [<identity>…]",
			"orc wake [--every <dur>] [--after <dur>] [--message <text>] [--dry-run]",
		},
		does: "poke whatever has gone quiet, on a cycle",
		detail: "An agent finishes a turn and stops. Nothing is wrong with it — Claude has\n" +
			"said its piece and is waiting for the next thing somebody says — but in a\n" +
			"fleet nobody is watching, that is where work stops. This is the thing\n" +
			"that speaks.\n\n" +
			"It reads each session's event feed, finds the ones that have been\n" +
			"*waiting* longer than --after (10m by default), and pokes them. A session\n" +
			"that is mid-turn is silent for good reasons — a long build, a slow read —\n" +
			"and is left alone: poking it would queue a nudge into the middle of work\n" +
			"it is already doing.\n\n" +
			"Each silence is woken once. An agent that does not move after a poke is\n" +
			"stuck rather than idle, and the next pass says so instead of filling its\n" +
			"context with nudges.\n\n" +
			"With --every it is the cycle; without, it is one sweep. With names it\n" +
			"wakes those, and refuses one you do not control; without, it sweeps what\n" +
			"is employed in your own subtree.",
		examples: []string{
			"orc wake",
			"orc wake --every 5m",
			"orc wake ember --message \"status?\"",
			"orc wake --every 5m --after 30m --dry-run",
		},
	},
	"refresh": {
		forms: []string{"orc refresh <identity>"},
		does:  "new session, fresh context, same identity",
		detail: "Stops the session and starts another with a new id. The context is gone;\n" +
			"the identity is not — memories, mailbox, tasks, and workspace all belong\n" +
			"to the identity, which is what lets several sessions fill the role of one\n" +
			"persistent agent.\n\n" +
			"Distinct from recovery, which resumes the *same* session after a crash.",
		examples: []string{"orc refresh ember"},
	},
	"status": {
		forms: []string{"orc status [<identity>] [--json]"},
		does:  "the fleet, or one card",
		detail: "With no name, the whole tree: who reports to whom, what each is doing,\n" +
			"and what it costs. With a name, one card in full — role, authority,\n" +
			"permissions, grants, session, workspace.\n\n" +
			"Where an effective number differs from what a role asked for, both are\n" +
			"shown and the reason is named.",
		examples: []string{"orc status", "orc status ember", "orc status --json"},
	},
	"list": {
		forms: []string{"orc list identities|roles|permissions|grants [--json]"},
		does:  "the rosters; --json for any of them",
		detail: "The flat lists, for when the tree is not the question. `--json` is the\n" +
			"stable shape another program should read.",
		examples: []string{"orc list roles", "orc list grants --json"},
	},
	"introspect": {
		forms: []string{"orc introspect [--only <field>] [--json]"},
		does:  "who am I, and what may I do",
		detail: "What the credential in the environment actually is: identity, role,\n" +
			"authority, permissions, grants, boss, workspace, mailbox.\n\n" +
			"--only prints one field raw, with no formatting, which is how another\n" +
			"tool asks — `muff` verifies an identity with\n" +
			"`orc introspect --only identity`.",
		examples: []string{"orc introspect", "orc introspect --only permissions"},
	},
	"check-control": {
		forms: []string{"orc check-control <agent>"},
		does:  "exit 0 if you control it, 8 if not",
		detail: "Exit-code only: 0 if the caller is above the agent in the tree, 8 if not,\n" +
			"2 if no fleet has it. Control is ancestry — an agent may act on its\n" +
			"subagents without needing a permission for it.\n\n" +
			"It is the contract `muff assign` asks before giving work away, so that\n" +
			"Macmuffin never has to hold a copy of the tree.",
		examples: []string{"orc check-control ember"},
	},
	"env": {
		forms: []string{"orc env <identity>"},
		does:  "the export block — discloses a key",
		detail: "Prints the environment a manual shell needs to be that identity. It is\n" +
			"one of two commands that disclose a key, and it says so on stderr every\n" +
			"time — a command that quietly prints a secret is one that ends up in a\n" +
			"script whose output is logged.\n\n" +
			"You must control the identity: handing out somebody's credential is\n" +
			"handing out their identity.",
		examples: []string{"orc env ember", "eval \"$(orc env ember)\""},
	},
	"verify": {
		forms: []string{"orc verify"},
		does:  "walk the store and report damage",
		detail: "Reads everything and reports what is wrong without changing any of it:\n" +
			"a journal that will not replay, a session file with no process, a socket\n" +
			"with no state, a work list that disagrees with what is running.\n\n" +
			"It never repairs. Exit 6 when it finds something.",
		examples: []string{"orc verify"},
	},
	"doctor": {
		forms: []string{"orc doctor"},
		does:  "which guards are in force, and which are not",
		detail: "The other health question: not \"is the store intact\" but \"is anything\n" +
			"actually being enforced\". The sandbox stamp, the file lock, the compiled\n" +
			"settings, the hook on PATH, the keyring's mode — each reported as in\n" +
			"force or not, and every known hole printed as a hole.\n\n" +
			"A guard nobody has checked is a guard nobody should be relying on.",
		examples: []string{"orc doctor"},
	},
	"owner": {
		forms: []string{
			"orc owner",
			"orc owner env",
			"orc owner rename <name> --yes",
			"orc owner reset --yes",
		},
		does: "who the operator is, and how orc knows it is you",
		detail: "The operator is you: authority 100, the root of the tree, and the\n" +
			"credential orc falls back to when the environment says nothing.\n\n" +
			"`rename` keeps the key and the memories. `reset` destroys the fleet and\n" +
			"bootstraps a fresh one, which is why it needs --yes and says exactly what\n" +
			"it will take with it.",
		examples: []string{"orc owner", "orc owner env", "orc owner rename redjive2 --yes"},
	},
}

// commandHelp is `orc help <command>`: that command and nothing else.
//
// Nothing else is the point. A reader who asked about one verb has already seen the
// list, and reprinting ninety lines — the model, the load table, the environment —
// after the four they came for is how a help screen teaches people not to read it.
func commandHelp(p style.Palette, name string) (string, bool) {
	got, ok := topics[name]
	if !ok {
		return "", false
	}

	var b strings.Builder
	for _, form := range got.forms {
		b.WriteString(p.Command(form) + "\n")
	}
	b.WriteString("\n  " + p.Muted(got.does) + "\n")

	if got.detail != "" {
		b.WriteString("\n")
		for _, line := range strings.Split(got.detail, "\n") {
			if line == "" {
				b.WriteString("\n")
				continue
			}
			b.WriteString("  " + line + "\n")
		}
	}
	if len(got.examples) > 0 {
		b.WriteString("\n" + p.Header("examples") + "\n")
		for _, example := range got.examples {
			b.WriteString("  " + p.Value(example) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n"), true
}

// noSuchTopic is `orc help <something that is not a command>`.
func noSuchTopic(name string) error {
	if near := guess.Nearest(name, verbs()); near != "" {
		return fault.Usage{Reason: fmt.Sprintf("no help for %q — did you mean `orc help %s`?", name, near)}
	}
	return fault.Usage{Reason: fmt.Sprintf("no help for %q; `orc help` lists the commands", name)}
}

// help answers `orc help`, and `orc help <command>` for one of them.
//
// A two-word command is looked up by its first word: `orc help new identity` and
// `orc help new` are the same page, because the page covers every form of the verb
// and splitting it would mean three pages that each said most of the same thing.
func (a App) help(args []string) error {
	if len(args) == 0 {
		return a.say(usage(a.out))
	}
	got, ok := commandHelp(a.out, args[0])
	if !ok {
		return noSuchTopic(args[0])
	}
	return a.say(got)
}

// unknown is the refusal for a verb orc does not have.
//
// One line, with a guess when there is a good one. The full screen used to follow
// every usage error, which meant the answer to a typo was ninety lines in which
// the answer was somewhere.
func unknown(command string) error {
	if near := guess.Nearest(command, verbs()); near != "" {
		return fault.Usage{Reason: fmt.Sprintf("unknown command %q — did you mean `orc %s`?", command, near)}
	}
	return fault.Usage{Reason: fmt.Sprintf("unknown command %q; `orc help` lists them", command)}
}
