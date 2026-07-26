package cli

import (
	"fmt"
	"strings"

	"orc/common/fault"
	"orc/common/guess"

	"orc/macmuffin/internal/style"
	"orc/macmuffin/internal/task"
	"orc/theme"
)

// Help is data rather than a wall of text, so that every command name, flag,
// placeholder, and environment variable can be painted for what it *is* rather
// than matched by a regular expression afterwards.
//
// The plain rendering is byte-for-byte what the old constant was: colour is a
// layer, and a test pins that stripping the escapes returns exactly the text a
// pipe would have seen.

// entry is one line of the command list: the invocation, then what it does.
type entry struct {
	// name is the command word, or words.
	name string
	// args is everything after it, painted as placeholders and flags.
	args string
	// does is the description, which recedes.
	does string
}

// commands is muff's whole surface, in the order a newcomer meets it.
var commands = []entry{
	{"create", "<task> <priority> <difficulty>", "create a draft task"},
	{"push", "<task>", "publish a draft to the pool"},
	{"claim", "<task>", "take an unowned task as your own"},
	{"pool", "[--all]", "the board: every task you can see"},
	{"info", "<task>", "one task in full"},
	{"scope", "<task> <paths...>", "limit editing to these paths"},
	{"worktree", "<task> <path>", "bind the task to a git worktree"},
	{"check-scope", "<paths...>", "exit 0 in scope, 9 outside"},
	{"create", "<task> --sub <name>", "add a subtask"},
	{"status", "<task> <1..4>", "say how the work is going"},
	{"complete", `<task> [--sub <name>]`, `mark it done ("--force" overrides)`},
	{"delete", "<task> [--sub <name>] --yes", "remove it, permanently"},
	{"assign", "<agent> <task>", "give it to an agent you control, and tell them"},
	{"invite", "<agent> <task>", "add a collaborator, and tell them"},
	{"kick", "<agent> <task>", "remove one, and tell them"},
	{"leave", "<task>", "stop collaborating"},
	{"verify", "", "check the store, and change nothing"},
}

// topic is what `muff help <command>` adds to the list's one-liner: what the command
// is *for*, and forms worth pasting.
//
// It is a table of its own rather than fields on entry because two entries share a
// name — `create` makes a task and a subtask — and a reader asking about `create`
// wants both forms and one explanation, not the explanation twice.
type topic struct {
	detail   string
	examples []string
}

var topics = map[string]topic{
	"create": {
		detail: "A task starts as a private draft: only you can see it, and it can be\n" +
			"scoped or deleted but not worked. `muff push` publishes it.\n\n" +
			"Priority and difficulty are set here and run 1 to 5. They are set at\n" +
			"creation because a board where everything is urgent is a board nobody\n" +
			"sorts, and changing them later is how that happens.\n\n" +
			"With --sub it adds a subtask instead, to a task you already own.",
		examples: []string{
			"muff create fix-the-parser 4 3",
			"muff create fix-the-parser --sub write-the-tests",
		},
	},
	"push": {
		detail: "Publishes a draft to the pool, where anyone can see it and claim it.\n" +
			"One-way: there is no unpush, because an agent may have claimed it\n" +
			"between the two commands.\n\n" +
			"It refuses a task with no scope. A pooled task that nobody can edit is\n" +
			"an invitation to a dead end.",
		examples: []string{"muff push fix-the-parser"},
	},
	"claim": {
		detail: "Takes an unowned task as your own. It is a compare-and-set: two agents\n" +
			"scanning the same pool will claim within milliseconds of each other, and\n" +
			"the second is told who won rather than quietly sharing it.",
		examples: []string{"muff claim fix-the-parser"},
	},
	"pool": {
		detail: "The board: every task you can see, active work first and completed work\n" +
			"sunk below it. Drafts are yours alone, so somebody else's do not appear.\n\n" +
			"--all includes what is finished. --json prints the stable shape\n" +
			"Communiqué mirrors through.",
		examples: []string{"muff pool", "muff pool --all", "muff pool --json"},
	},
	"info": {
		detail: "One task in full: scores, status, owner, collaborators, subtasks, scope,\n" +
			"and the worktree it is bound to. It is what to read before claiming\n" +
			"something, and what to read when a hook has just refused an edit.",
		examples: []string{"muff info fix-the-parser"},
	},
	"scope": {
		detail: "Declares the files the task may edit, and enforces it: `muff-hook`\n" +
			"refuses an out-of-scope edit before it happens, and `anno write` asks\n" +
			"first. Entries match as an exact file, a directory prefix, or a glob.\n\n" +
			"It replaces the scope rather than adding to it, so the command always\n" +
			"states the whole editable surface and a reader never has to accumulate a\n" +
			"history to know what it is.",
		examples: []string{
			"muff scope fix-the-parser internal/tree internal/marker",
			"muff scope fix-the-parser 'cmd/**/*.go'",
		},
	},
	"worktree": {
		detail: "Binds the task to a git worktree, which is how the hook works out what\n" +
			"you are doing without being told on every call: it resolves the session's\n" +
			"directory to its worktree root and looks it up.\n\n" +
			"A worktree already bound to another live task is refused — an ambiguous\n" +
			"lookup would silently enforce the wrong scope.",
		examples: []string{"muff worktree fix-the-parser .", "muff worktree fix-the-parser ../parser-tree"},
	},
	"check-scope": {
		detail: "Exit 0 if every path is in scope, 9 if any is not, and nothing on stdout\n" +
			"either way. It is the contract Anno calls before `anno write` touches a\n" +
			"file — from a hook a shell command's writes are undecidable, but on\n" +
			"Anno's side the question has an answer.\n\n" +
			"With no task in force it exits 0: an agent that never opted in is never\n" +
			"blocked.",
		examples: []string{"muff check-scope internal/tree/tree.go", "muff check-scope src/*.go"},
	},
	"status": {
		detail: "Says how the work is going, 1 to 4: broken, slow, nominal, done. It is\n" +
			"the signal an operator scans a board for, so it is worth setting when it\n" +
			"changes rather than at the end.\n\n" +
			"The previous value is printed beside the new one, so a change is visible.",
		examples: []string{"muff status fix-the-parser 2", "muff status fix-the-parser broken"},
	},
	"complete": {
		detail: "Marks the task done. A task with unfinished subtasks is refused, and they\n" +
			"are listed.\n\n" +
			"--force completes anyway and journals the skipped list. The point of a\n" +
			"tracker is that shortcuts stay visible, so the override exists and leaves\n" +
			"a mark.",
		examples: []string{
			"muff complete fix-the-parser",
			"muff complete fix-the-parser --sub write-the-tests",
			"muff complete fix-the-parser --force",
		},
	},
	"delete": {
		detail: "The only irreversible command. It prints what will go — subtasks, and\n" +
			"collaborators who lose the task without warning otherwise — and needs\n" +
			"--yes when stdin is not a terminal, which for an agent is always.\n\n" +
			"The deletion is journaled before anything is erased, so a crash midway\n" +
			"leaves a task `muff verify` can name rather than a half-erased directory.",
		examples: []string{"muff delete fix-the-parser --yes", "muff delete fix-the-parser --sub old-idea --yes"},
	},
	"assign": {
		detail: "Gives a task to an agent you control, and tells them by mail. Two\n" +
			"questions are answered by two tools: whether you may direct that agent is\n" +
			"Orc's, asked through `orc check-control`, and whether the task may be\n" +
			"given away is muff's — the same question `claim` asks.\n\n" +
			"With no Orc installed it refuses. A restriction that evaporates when its\n" +
			"authority is unreachable is not a restriction.",
		examples: []string{"muff assign ember fix-the-parser"},
	},
	"invite": {
		detail: "Adds a collaborator, who may then report status and move the checklist\n" +
			"but not push, scope, complete, or delete — those stay the owner's.\n\n" +
			"They are told by mail. A Mailman that is missing or broken delays the\n" +
			"notice rather than losing it, and never fails the change.",
		examples: []string{"muff invite ember fix-the-parser"},
	},
	"kick": {
		detail:   "Removes a collaborator, and tells them. The owner's call alone.",
		examples: []string{"muff kick ember fix-the-parser"},
	},
	"leave": {
		detail: "Stops collaborating on a task. The owner cannot leave their own task — a\n" +
			"task is never orphaned by accident — and is told to complete or delete it\n" +
			"instead.",
		examples: []string{"muff leave fix-the-parser"},
	},
	"verify": {
		detail: "Walks the store and reports what is wrong, changing nothing. A store\n" +
			"several unsupervised agents write to needs a way to answer \"is this\n" +
			"healthy?\" that is not \"read the source\".\n\n" +
			"It never repairs: an automatic repair of damage nobody has understood is\n" +
			"how one bad file becomes many. Exit 6 when it finds something.",
		examples: []string{"muff verify"},
	},
}

// commandHelp is `muff help <command>`: that command and nothing else.
//
// Nothing else is the point. A reader who asked about one verb has already seen the
// list — that is how they learned the verb's name — and reprinting it, with the score
// table and the exit codes after it, buries the three lines they came for.
func commandHelp(p style.Palette, name string) (string, bool) {
	var forms []entry
	for _, c := range commands {
		if c.name == name {
			forms = append(forms, c)
		}
	}
	if len(forms) == 0 {
		return "", false
	}

	var b strings.Builder
	for _, form := range forms {
		b.WriteString(p.Muted("muff") + " " + p.Command(form.name))
		if form.args != "" {
			b.WriteString(" " + paintArgs(p, form.args))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n  " + p.Muted(forms[0].does) + "\n")

	got := topics[name]
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
			b.WriteString("  " + p.Command(example) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n"), true
}

// noSuchTopic is `muff help <something that is not a command>`.
func noSuchTopic(name string) error {
	if near := guess.Nearest(name, verbs()); near != "" {
		return fault.Usage{Reason: fmt.Sprintf("no help for %q — did you mean `muff help %s`?", name, near)}
	}
	return fault.Usage{Reason: fmt.Sprintf("no help for %q; `muff help` lists the commands", name)}
}

// The columns the command list is laid out in. Fixed rather than measured: the
// help is a hand-set page, and a column that moved when a command was added
// would make every diff of this file unreadable.
const (
	nameColumn = 7  // "worktree", "check-scope", and "complete" overrun it, as they always did
	argsColumn = 47 // where the description starts
)

// settings documents the environment muff reads.
var settings = []struct{ name, does string }{
	{"$ORC_USER", "the agent to act as"},
	{"$ORC_KEY", "the key that proves it"},
}

// usage renders the help, painted with the given palette.
func usage(p style.Palette) string {
	var b strings.Builder

	b.WriteString(p.Tool("muff") + " — " + p.Muted("the task pool") + "\n\n")

	b.WriteString(p.Header("usage:") + "\n")
	for _, c := range commands {
		b.WriteString(commandLine(p, c))
	}

	section(&b, p, "status runs 1 to 4:", []string{
		"1 " + p.Broken("broken") + " · 2 " + p.Slow("slow") + " · 3 " + p.Nominal("nominal") + " · 4 " + p.Done("done"),
	})

	section(&b, p, "scores run 1 to 5, and are set at creation:", []string{
		p.Value("priority") + "    1 low   → 5 high",
		p.Value("difficulty") + "  1 easy  → 5 hard",
	})

	b.WriteString("\n" + p.Muted(
		"a task is a private draft until it is pushed, and cannot be pushed, edited, or\n"+
			"completed until it has a scope.") + "\n")

	b.WriteString("\n" + p.Header("identity comes from orc, and is checked on every command:") + "\n")
	b.WriteString("  " + p.Muted("verified against `orc introspect` where orc is installed") + "\n")
	for _, s := range settings {
		b.WriteString("  " + pad(p.Setting(s.name), s.name, 12) + p.Muted(s.does) + "\n")
	}

	// The colour settings are the shared package's own words, so every Orc tool
	// documents them identically. The flags are muff's own, and are said here
	// because a caller assembling one command should not have to set an
	// environment variable to keep escapes out of it.
	b.WriteString("\n" + theme.Help() + "\n")
	b.WriteString("        " + p.Flag(FlagNoColour) + " and " + p.Flag(FlagColour) +
		p.Muted(" do the same for one command, before or after it") + "\n")

	b.WriteString("\n" + p.Header("exit codes:") + " 0 ok · 1 usage · 2 not found · 3 ambiguous · 4 parse · 5 i/o ·\n")
	b.WriteString("            6 conflict · 7 auth · 8 denied · 9 out of scope · 11 escape ·\n")
	b.WriteString("            70 internal")

	return b.String()
}

// commandLine draws one command.
//
// The painted line and its plain twin are built side by side, and every pad
// measures the plain one: escape sequences occupy no columns, so measuring the
// painted string would indent a coloured line differently from an uncoloured
// one — the bug that makes half the world's CLIs wobble under NO_COLOR.
func commandLine(p style.Palette, c entry) string {
	painted := "  " + p.Muted("muff") + " " + p.Command(c.name)
	plain := "  muff " + c.name
	painted, plain = padPair(painted, plain, len("  muff ")+nameColumn)

	painted, plain = painted+paintArgs(p, c.args), plain+c.args
	painted, _ = padPair(painted, plain, argsColumn)

	return painted + p.Muted(c.does) + "\n"
}

// paintArgs colours placeholders and flags inside an argument list.
func paintArgs(p style.Palette, args string) string {
	var out strings.Builder
	for i, field := range strings.Fields(args) {
		if i > 0 {
			out.WriteString(" ")
		}
		switch {
		case strings.HasPrefix(field, "--"), strings.HasPrefix(field, "[--"), strings.HasPrefix(field, `"--`):
			out.WriteString(p.Flag(field))
		default:
			out.WriteString(p.Value(field))
		}
	}
	return out.String()
}

// section writes a headed block.
func section(b *strings.Builder, p style.Palette, heading string, lines []string) {
	b.WriteString("\n" + p.Header(heading) + "\n")
	for _, line := range lines {
		b.WriteString("  " + line + "\n")
	}
}

// padPair extends both forms with the same spaces, measuring the plain one. A
// column that is already full gets a single space, so nothing ever runs
// together.
func padPair(painted, plain string, width int) (string, string) {
	gap := width - theme.Width(plain)
	if gap < 1 {
		gap = 1
	}
	spaces := strings.Repeat(" ", gap)
	return painted + spaces, plain + spaces
}

// pad extends painted text with spaces until its plain form reaches width.
func pad(painted, plain string, width int) string {
	got, _ := padPair(painted, plain, width)
	return got
}

// paintStatus colours a status by what it means. The word is printed too — a
// reader who cannot see the colour, or is reading a pipe, loses nothing.
func paintStatus(p style.Palette, st task.Status) string {
	label := st.Label()
	switch st {
	case task.StatusBroken:
		return p.Broken(label)
	case task.StatusSlow:
		return p.Slow(label)
	case task.StatusNominal:
		return p.Nominal(label)
	case task.StatusDone:
		return p.Done(label)
	default:
		return p.Muted(label)
	}
}

// brief is what `muff` on its own prints: the verbs, grouped, and nothing else.
//
// The full screen carries the score scales, the status ladder, and the identity
// rules — worth reading once, in the way every time after that. `muff` alone is
// almost always somebody checking what a verb was called.
func brief(p style.Palette) string {
	var b strings.Builder

	group := func(name string, verbs ...string) {
		painted := make([]string, len(verbs))
		for i, v := range verbs {
			painted[i] = p.Command(v)
		}
		// Padding measured on the plain name and applied after the paint, the same
		// rule the rest of this file follows.
		b.WriteString("  " + pad(p.Header(name), name, 12) +
			strings.Join(painted, p.Muted(" · ")) + "\n")
	}

	b.WriteString(p.Tool("muff") + " — " + p.Muted("the task pool") + "\n\n")
	for _, g := range groups {
		group(g.name, g.verbs...)
	}
	b.WriteString("\n  " + p.Muted("muff help for all of it: every form, the scales, the identity rules") + "\n")
	return strings.TrimRight(b.String(), "\n")
}

// groups is the command list as `muff` alone shows it. It is hand-set because the
// grouping is editorial and the order of `commands` is a teaching order, not a
// taxonomy — but a test walks both and fails if a command is in one and not the
// other, so it cannot go stale by omission.
var groups = []struct {
	name  string
	verbs []string
}{
	{"making", []string{"create", "push", "scope", "worktree"}},
	{"the board", []string{"pool", "info", "status", "complete", "delete"}},
	{"who has it", []string{"claim", "assign", "invite", "kick", "leave"}},
	{"checking", []string{"check-scope", "verify"}},
}

// verbs is every command muff answers to, for suggesting the one that was meant.
// Derived from the command table rather than written out, so a command added
// there is guessable here without anyone remembering to.
func verbs() []string {
	seen := map[string]bool{"help": true}
	out := []string{"help"}
	for _, c := range commands {
		if !seen[c.name] {
			seen[c.name] = true
			out = append(out, c.name)
		}
	}
	return out
}

// unknown is the refusal for a verb muff does not have: one line, with a guess
// when there is a good one, rather than the whole screen after it.
func unknown(command string) error {
	if near := guess.Nearest(command, verbs()); near != "" {
		return fault.Usage{Reason: fmt.Sprintf("unknown command %q — did you mean `muff %s`?", command, near)}
	}
	return fault.Usage{Reason: fmt.Sprintf("unknown command %q; `muff help` lists them", command)}
}

// help answers `muff help`, and `muff help <command>` for one of them.
func (a App) help(args []string) error {
	switch len(args) {
	case 0:
		return a.say(usage(a.out))
	case 1:
		got, ok := commandHelp(a.out, args[0])
		if !ok {
			return noSuchTopic(args[0])
		}
		return a.say(got)
	default:
		return fault.Usage{Reason: "help takes one command, or nothing"}
	}
}
