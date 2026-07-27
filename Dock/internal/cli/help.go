package cli

import (
	"fmt"
	"strings"

	"orc/common/fault"
	"orc/common/guess"

	"orc/dock/internal/doc"
	"orc/dock/internal/style"
	"orc/theme"
)

// Help is data rather than a wall of text, so that every command name, flag,
// placeholder, and environment variable can be painted for what it *is* rather
// than matched by a regular expression afterwards.
//
// The plain rendering is byte-for-byte what a pipe would have seen: colour is a
// layer, and a test pins that stripping the escapes returns exactly that.

// entry is one line of the command list: the invocation, then what it does.
type entry struct {
	// name is the command word.
	name string
	// args is everything after it, painted as placeholders and flags.
	args string
	// does is the description, which recedes.
	does string
}

// commands is dock's whole surface, in the order a newcomer meets it.
var commands = []entry{
	{"index", "<file>", "the sections in a document, and their sizes"},
	{"overview", "<dir>", "the same for a tree, and the folders with nothing in them"},
	{"read", "<target>", "one section's own prose"},
	{"", "<target> --tree", "and everything under it"},
	{"", "<target> --follow[=n]", "and the sections it links to, n deep"},
	{"", "<target> --budget=<lines>", "stopping before that many content lines"},
	{"find", "<dir>" + doc.Sigil + "<ref>", "a section by number or name, across a tree"},
	{"links", "<target>", "what a section cites, and what cites it"},
	{"check", "[<dir>]", "every link in a tree that does not resolve"},
	{"write", "<target> <text>", `replace a section ("-" reads stdin)`},
}

// The columns the command list is laid out in. Fixed rather than measured: the
// help is a hand-set page, and a column that moved when a command was added
// would make every diff of this file unreadable.
const (
	nameColumn = 9  // "overview" fits; the continuation lines are blank here
	argsColumn = 45 // where the description starts
)

// targets documents the address grammar, which is the one thing a caller has to
// learn before any command is usable.
var targets = []struct{ form, means string }{
	{"guide.md" + doc.Sigil + "1.2", "section 1.2 of guide.md"},
	{"guide.md" + doc.Sigil + "'Install'", "the section named Install"},
	{"example.go@code:Operate", "an anno annotation, resolved by anno"},
}

// settings documents the environment dock reads beyond the shared scheme.
var settings = []struct{ name, does string }{
	{"$ORC_AGENT", "set by orc for an agent; forces plain output"},
}

// usage renders the help, painted with the given palette.
func usage(p style.Palette) string {
	var b strings.Builder

	b.WriteString(p.Paint("dock", style.Tool) + " — " +
		p.Paint("read documentation without reading all of it", style.Quiet) + "\n\n")

	b.WriteString(p.Paint("usage:", style.Name) + "\n")
	for _, c := range commands {
		b.WriteString(commandLine(p, c))
	}

	b.WriteString("\n" + p.Paint("a target is a path and an address:", style.Name) + "\n")
	for _, t := range targets {
		painted := "  " + paintTarget(p, t.form)
		plain := "  " + t.form
		painted, _ = padPair(painted, plain, 2+30)
		b.WriteString(painted + p.Paint(t.means, style.Quiet) + "\n")
	}

	b.WriteString("\n" + p.Paint(
		"a section is a markdown heading carrying a "+doc.Sigil+" number, as in\n"+
			"\"## "+doc.Sigil+"1.2 Sections\". dock adds no syntax of its own.", style.Quiet) + "\n")

	b.WriteString("\n" + p.Paint("the environment:", style.Name) + "\n")
	for _, s := range settings {
		painted := "  " + p.Paint(s.name, style.Setting)
		plain := "  " + s.name
		painted, _ = padPair(painted, plain, 2+12)
		b.WriteString(painted + p.Paint(s.does, style.Quiet) + "\n")
	}

	// The colour settings are the shared package's own words, so every Orc tool
	// documents them identically. The flags are dock's own, and are said here
	// because a caller assembling one command should not have to set an
	// environment variable to keep escapes out of it.
	b.WriteString("\n" + theme.Help() + "\n")
	b.WriteString("        " + p.Paint(FlagNoColour, style.Flag) + " and " + p.Paint(FlagColour, style.Flag) +
		p.Paint(" do the same for one command, before or after it", style.Quiet) + "\n")

	// The numbers are the shared ones every Orc tool uses, so a script can
	// branch on a status without knowing which binary it called. Only the ones
	// dock can return are listed: a table of codes a tool never emits is
	// something a reader has to check against the source to trust.
	b.WriteString("\n" + p.Paint("exit codes:", style.Name) + " 0 ok · 1 usage · 2 not found · 4 parse ·\n")
	b.WriteString("            5 i/o · 6 conflict · 70 internal\n")

	return b.String()
}

// commandLine draws one command.
//
// The painted line and its plain twin are built side by side, and every pad
// measures the plain one: escape sequences occupy no columns, so measuring the
// painted string would indent a coloured line differently from an uncoloured
// one — the bug that makes half the world's CLIs wobble under NO_COLOR.
func commandLine(p style.Palette, c entry) string {
	painted, plain := "  ", "  "
	if c.name != "" {
		painted += p.Paint("dock", style.Quiet) + " " + p.Paint(c.name, style.Command)
		plain += "dock " + c.name
	}
	painted, plain = padPair(painted, plain, len("  dock ")+nameColumn)

	painted, plain = painted+paintArgs(p, c.args), plain+c.args
	painted, _ = padPair(painted, plain, argsColumn)

	return painted + p.Paint(c.does, style.Quiet) + "\n"
}

// paintArgs colours placeholders and flags inside an argument list.
func paintArgs(p style.Palette, args string) string {
	var out strings.Builder
	for i, field := range strings.Fields(args) {
		if i > 0 {
			out.WriteString(" ")
		}
		if strings.HasPrefix(field, "--") || strings.HasPrefix(field, "[--") {
			out.WriteString(p.Paint(field, style.Flag))
			continue
		}
		out.WriteString(p.Paint(field, style.Value))
	}
	return out.String()
}

// paintTarget colours a target form: the path and address in the same inks the
// rest of dock uses for them.
func paintTarget(p style.Palette, form string) string {
	if i := strings.Index(form, doc.Sigil); i >= 0 {
		return p.Paint(form[:i], style.Value) + p.Paint(form[i:], style.Number)
	}
	if i := strings.IndexAny(form, "@:^"); i >= 0 {
		return p.Paint(form[:i], style.Value) + p.Paint(form[i:], style.Foreign)
	}
	return p.Paint(form, style.Value)
}

// padPair pads a painted string to the column its plain twin would reach.
func padPair(painted, plain string, column int) (string, string) {
	for style.Width(plain) < column {
		painted += " "
		plain += " "
	}
	return painted, plain
}

// brief is what `dock` on its own prints: the verbs and nothing else.
//
// Dock has seven, so they fit on one line and there is nothing to group. What the
// full screen adds is the target syntax, which is the part worth reading — once,
// and not every time somebody forgets whether it is `links` or `refs`.
func brief(p style.Palette) string {
	var b strings.Builder

	b.WriteString(p.Paint("dock", style.Tool) + " — " +
		p.Paint("read documentation without reading all of it", style.Quiet) + "\n\n")

	painted := make([]string, 0, len(commands))
	for _, c := range commands {
		// The table repeats a name with an empty cell for each extra form of the
		// same command; those are forms, not verbs, and the short list wants verbs.
		if c.name != "" {
			painted = append(painted, p.Paint(c.name, style.Command))
		}
	}
	b.WriteString("  " + strings.Join(painted, p.Paint(" · ", style.Quiet)) + "\n")
	b.WriteString("\n  " + p.Paint("dock help for all of it: every form, and the target syntax", style.Quiet))
	return b.String()
}

// verbs is every command dock answers to, for suggesting the one that was meant.
// Derived from the command table, so one added there is guessable here without
// anyone remembering to.
func verbs() []string {
	out := make([]string, 0, len(commands)+1)
	for _, c := range commands {
		if c.name != "" {
			out = append(out, c.name)
		}
	}
	return append(out, "help")
}

// unknown is the refusal for a verb dock does not have: one line, with a guess
// when there is a good one, rather than the whole screen after it.
func unknown(command string) error {
	if near := guess.Nearest(command, verbs()); near != "" {
		return fault.Usage{Reason: fmt.Sprintf("unknown command %q — did you mean `dock %s`?", command, near)}
	}
	return fault.Usage{Reason: fmt.Sprintf("unknown command %q; `dock help` lists them", command)}
}
