package cli

import (
	"fmt"
	"strings"

	"orc/common/guess"

	"orc/orcprobe/internal/fault"
	"orc/orcprobe/internal/query"
	"orc/orcprobe/internal/style"
	"orc/theme"
)

// The help is data rather than a wall of text, so every command name, flag,
// placeholder, and environment variable can be painted for what it *is* rather
// than matched by a regular expression afterwards. The shape is Macmuffin's,
// and so are the roles: two tools' help pages should read as pages of one
// manual.
//
// Colour is still only a layer. The painted line and its plain twin are built
// side by side and every pad measures the *plain* one — escape sequences occupy
// no columns, so measuring the painted string would indent a coloured line
// differently from an uncoloured one, which is the bug that makes half the
// world's CLIs wobble under NO_COLOR. A test pins that stripping the escapes
// returns exactly what a pipe would have seen.

// entry is one line of a command list: the invocation, then what it does.
type entry struct {
	name string
	args string
	does string
}

// The three groups orcprobe's surface falls into, in the order a newcomer
// meets them: make a world, look at it from outside, keep it honest.
var (
	probeCommands = []entry{
		{"create", "<name>", "take a probe of the current world"},
		{"list", "", "every probe, with age and liveness"},
		{"use", "<name>", "make a probe the default"},
		{"shell", "[--as <user>]", "a subshell inside the probe"},
		{"as", "<user> -- <cmd...>", "one command, as one identity"},
		{"manifest", "", "what was copied, dropped, and deferred"},
		{"destroy", "<name> --yes", "remove a probe whole"},
	}

	viewCommands = []entry{
		{"world", "", "every mailbox, the pool, the sync state"},
		{"mail", "[query]", "every mailbox at once"},
		{"tasks", "", "the whole pool, deleted ones included"},
		{"journal", "<task|mailbox>", "one append-only journal, decoded"},
		{"timeline", "[--since 2h]", "every tool's events, in one sequence"},
	}

	keepingCommands = []entry{
		{"save", "<label>", "checkpoint the probe as it stands"},
		{"restore", "<label> --yes", "rewind it, discarding everything since"},
		{"diff", "<a> <b>", "what differs between two probes"},
		{"diff", "--source", "has the world moved since this was taken"},
		{"doctor", "[--strict]", "check every guard, and say which are in force"},
	}
)

// The columns the command lists are laid out in. Fixed rather than measured:
// the help is a hand-set page, and a column that moved when a command was added
// would make every diff of this file unreadable.
const (
	nameColumn = 8  // "manifest" and "timeline" fill it exactly
	argsColumn = 40 // where the description starts
)

// flags documents the options every command shares.
var flagHelp = []struct{ name, does string }{
	{"--probe <name>", "act on this probe instead of the default"},
	{"--as <user>", "identity for shell"},
	{"--repo <path>", "working tree to copy; --no-repo copies none"},
	{"--live-state", "keep claims and pending work as they are"},
	{"--fake-home", "redirect HOME into the probe"},
	{"--strict", "doctor exits non-zero unless every guard is in force"},
	{"--yes", "confirm an irreversible command"},
	{"--width <n>", "lay tables out for this width"},
}

// settings documents the identity a probe hands out.
var settings = []struct{ name, does string }{
	{"$ORC_USER", "the identity shell and as set"},
	{"$ORC_KEY", "its probe key — worthless against the real store"},
}

// usage renders the help, painted with the given palette.
func usage(p style.Palette) string {
	var b strings.Builder

	b.WriteString(p.Tool("orcprobe") + " — " + p.Muted("a copy of the Orc world you can break") + "\n\n")

	b.WriteString(p.Header("usage:") + "\n")
	for _, c := range probeCommands {
		b.WriteString(commandLine(p, c))
	}

	b.WriteString("\n" + p.Header("seeing what no agent can") +
		p.Muted(" — read straight off the copy, no identity involved:") + "\n")
	for _, c := range viewCommands {
		b.WriteString(commandLine(p, c))
	}

	b.WriteString("\n" + p.Header("checkpoints, drift, and the guards:") + "\n")
	for _, c := range keepingCommands {
		b.WriteString(commandLine(p, c))
	}

	b.WriteString("\n" + p.Header("mail takes mailman's query language,") +
		p.Muted(" over the whole store rather than one\nmailbox — so unread means \"unread by anybody\" and id matches any recipient's:") + "\n")
	for _, q := range []string{`from="boss" & unread=true`, `!(to="alice") & subject~"work"`} {
		b.WriteString("  " + p.Muted("orcprobe") + " " + p.Command("mail") + " " + p.Value("'"+q+"'") + "\n")
	}
	b.WriteString("\n  " + p.Muted("fields: ") + p.Value(strings.Join(query.Fields(), ", ")) + "\n")
	b.WriteString("  " + p.Muted("joined by & and |, grouped with (), negated with !") + "\n")
	b.WriteString("  " + p.Muted("operators: = exact · != absent · ~ contains") + "\n")

	b.WriteString("\n" + p.Muted(
		"a probe copies mailman, macmuffin, and cq state, the working repo, and the\n"+
			"claude hook configuration. it never copies a real key, never starts an agent,\n"+
			"and never lets anything inside it reach the real world.") + "\n")

	b.WriteString("\n" + p.Header("identity inside a probe is free:") +
		p.Muted(" every mailbox is reminted with a key the\nprobe knows, so --as needs no password. the default is ") +
		p.Value(defaultIdentity) + p.Muted(".") + "\n")
	for _, s := range settings {
		b.WriteString("  " + pad(p.Setting(s.name), s.name, 14) + p.Muted(s.does) + "\n")
	}

	b.WriteString("\n" + p.Header("flags:") + "\n")
	for _, f := range flagHelp {
		b.WriteString("  " + pad(p.Flag(f.name), f.name, 17) + p.Muted(f.does) + "\n")
	}

	// The colour settings are the shared package's own words, so every Orc tool
	// documents them identically. The flags are orcprobe's own, and are said
	// here because a caller assembling one command should not have to set an
	// environment variable to keep escapes out of it.
	b.WriteString("\n" + theme.Help() + "\n")
	b.WriteString("        " + p.Flag(FlagNoColour) + " and " + p.Flag(FlagColour) +
		p.Muted(" do the same for one command, before or after it") + "\n")

	// The heading is painted and the codes are not, which is what muff and dock
	// both do. The numbers are the one part of a help page a reader copies into
	// a script, and every tool leaves them in the terminal's own foreground so
	// they look the same wherever they are read.
	b.WriteString("\n" + p.Header("exit codes:") + " 0 ok · 1 usage · 2 not found · 3 ambiguous · 4 parse · 5 i/o ·\n")
	b.WriteString("            6 conflict · 7 auth · 11 escape refused · 70 internal")

	return b.String()
}

// commandLine draws one command, painted and plain in step.
func commandLine(p style.Palette, c entry) string {
	painted := "  " + p.Muted("orcprobe") + " " + p.Command(c.name)
	plain := "  orcprobe " + c.name
	painted, plain = padPair(painted, plain, len("  orcprobe ")+nameColumn)

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
		case strings.HasPrefix(field, "--"), strings.HasPrefix(field, "[--"):
			out.WriteString(p.Flag(field))
		default:
			out.WriteString(p.Value(field))
		}
	}
	return out.String()
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

// brief is what `orcprobe` on its own prints: the verbs, in the three groups the
// full screen already uses, and nothing else.
//
// The full screen carries the query language, the flags, and what a probe does and
// does not copy — worth reading once, in the way every time after. `orcprobe`
// alone is almost always somebody checking what a verb was called.
func brief(p style.Palette) string {
	var b strings.Builder

	group := func(name string, commands []entry) {
		seen := map[string]bool{}
		painted := make([]string, 0, len(commands))
		for _, c := range commands {
			// `diff` appears twice in the table, once per form. Forms belong on
			// the full screen; the short one lists verbs.
			if c.name == "" || seen[c.name] {
				continue
			}
			seen[c.name] = true
			painted = append(painted, p.Command(c.name))
		}
		// Padding measured on the plain name and applied after the paint, the same
		// rule the rest of this file follows.
		b.WriteString("  " + pad(p.Header(name), name, 12) +
			strings.Join(painted, p.Muted(" · ")) + "\n")
	}

	b.WriteString(p.Tool("orcprobe") + " — " + p.Muted("a copy of the Orc world you can break") + "\n\n")
	group("probes", probeCommands)
	group("seeing", viewCommands)
	group("keeping", keepingCommands)
	b.WriteString("\n  " + p.Muted(
		"orcprobe help for all of it: every form, the query language, the flags") + "\n")
	return strings.TrimRight(b.String(), "\n")
}

// verbs is every command orcprobe answers to, for suggesting the one that was
// meant. Derived from the tables, so one added there is guessable here without
// anyone remembering to.
func verbs() []string {
	seen := map[string]bool{"help": true, "ls": true}
	out := []string{"help", "ls"}
	for _, table := range [][]entry{probeCommands, viewCommands, keepingCommands} {
		for _, c := range table {
			if c.name != "" && !seen[c.name] {
				seen[c.name] = true
				out = append(out, c.name)
			}
		}
	}
	return out
}

// unknown is the refusal for a verb orcprobe does not have: one line, with a guess
// when there is a good one, rather than the whole screen after it.
func unknown(command string) error {
	if near := guess.Nearest(command, verbs()); near != "" {
		return fault.Usage{Reason: fmt.Sprintf("unknown command %q — did you mean `orcprobe %s`?", command, near)}
	}
	return fault.Usage{Reason: fmt.Sprintf("unknown command %q; `orcprobe help` lists them", command)}
}
