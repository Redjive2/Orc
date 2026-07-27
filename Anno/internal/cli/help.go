package cli

import (
	"fmt"
	"strings"

	"orc/common/fault"
	"orc/common/guess"

	"orc/anno/internal/style"
	"orc/theme"
)

// The help, as data rather than as a wall of text.
//
// It was one constant, which cannot be painted for what each part *is* without
// matching it with a regular expression afterwards — and a regular expression over
// help text is a thing that breaks the first time somebody adds a command with a
// hyphen in it. Rendering from a table means a command name is painted because it is
// a command name.
//
// The plain rendering is byte-for-byte what the constant was, and a test pins that:
// colour is a layer, and the layer must be removable without moving anything.

// entry is one command: its line in the list, and its own page.
type entry struct {
	// name is the command word.
	name string
	// args is everything after it, painted as placeholders.
	args string
	// does is the one-line description, which recedes in the list.
	does string
	// detail is the prose `anno help <command>` adds. It says what the command
	// is *for* — the summary already says what it does.
	detail string
	// examples are forms worth pasting.
	examples []string
}

// commands is Anno's whole surface, in the order a newcomer meets it.
var commands = []entry{
	{
		name: "index", args: "<file>",
		does: "tree of annotations in a file",
		detail: "The map of a file: every annotation, nested as they nest, with the line\n" +
			"range each one covers. It is what to read before reading the file — the\n" +
			"point of the tool is to spend fewer tokens, and an index is a few hundred\n" +
			"where the file is a few thousand.\n\n" +
			"A file with no annotations prints nothing and exits 0. Not every file is\n" +
			"annotated, and that is not a failure.",
		examples: []string{"anno index app.go", "anno index app.go --json"},
	},
	{
		name: "overview", args: "<dir>",
		does: "trees for every annotated file in a tree",
		detail: "The same as `index`, for a whole tree at once, so a package — or a whole\n" +
			"repository — can be understood without opening it file by file. Each tree\n" +
			"is headed by its path relative to the directory you named.\n\n" +
			"The sweep is dock's: .git, node_modules, vendor, target and any\n" +
			"dot-directory are never walked. A file anno cannot read at all is passed\n" +
			"over in silence — over a tree those are images and archives, not\n" +
			"mistakes — and one it read but could not parse is noted on stderr.\n\n" +
			"It ends with the folders that had nothing to show, and why. A folder\n" +
			"holding nothing annotated appears in no tree, and one just made is then\n" +
			"indistinguishable from one that was never created.",
		examples: []string{"anno overview internal/tree", "anno overview . --json"},
	},
	{
		name: "read", args: "<file><chain>",
		does: "content of an annotation",
		detail: "The lines the annotation covers, emitted exactly: no dedent, no trimming,\n" +
			"original line endings, and no colour. That is deliberate — it makes `read`\n" +
			"and `write` inverses, so a region can be read, transformed, and written\n" +
			"back without anything reshaping it in between.",
		examples: []string{"anno read app.go@types", "anno read app.go@types:Pair^fields"},
	},
	{
		name: "find", args: "<dir><chain>",
		does: "content and index of matches in a directory",
		detail: "`read` when you know the annotation's name but not which file holds it.\n" +
			"Every match is printed with its fully qualified chain above it, so the\n" +
			"answer can be pasted back as an exact address.",
		examples: []string{"anno find internal/^fields", "anno find . @types"},
	},
	{
		name: "write", args: "<file><chain> <content>",
		does: `replace an annotation's content ("-" reads stdin)`,
		detail: "Replaces what the annotation covers, and nothing else: the markers stay,\n" +
			"the rest of the file is untouched, and the write is atomic.\n\n" +
			"It refuses a chain that names a whole file — anno does not overwrite\n" +
			"files — and it asks `muff check-scope` first where Macmuffin is\n" +
			"installed, so a task's scope is enforced even through anno.",
		examples: []string{
			`anno write app.go@types "type Pair struct{}"`,
			"cat new.go | anno write app.go@types -",
		},
	},
}

// The columns the list is laid out in. Fixed rather than measured: the help is a
// hand-set page, and a column that moved when a command was added would make every
// diff of this file unreadable.
const (
	nameColumn = 9  // "overview" overruns it by one, as it always did
	argsColumn = 43 // where the description starts
)

// chains documents the addressing syntax, which is the part of Anno somebody reading
// the help has actually come for.
var chains = []struct{ mark, means string }{
	{"@name", "section"},
	{":name", "symbol"},
	{"^name", "part"},
}

// usage renders the help, painted with the given palette.
func usage(p style.Palette) string {
	var b strings.Builder

	b.WriteString(p.Paint("anno", style.Tool) + " — " + p.Paint("a minimal file annotation manager", style.Quiet) + "\n\n")

	b.WriteString(p.Paint("usage:", style.Heading) + "\n")
	for _, c := range commands {
		b.WriteString(commandLine(p, c))
	}

	b.WriteString("\n" + p.Paint("a chain addresses an annotation by kind and name:", style.Heading) + "\n")
	b.WriteString("  ")
	for i, c := range chains {
		if i > 0 {
			b.WriteString("      ")
		}
		b.WriteString(p.Paint(c.mark, style.Value) + "   " + p.Paint(c.means, style.Quiet))
	}
	b.WriteString("\n")

	b.WriteString("\n" + p.Paint("chains may be fully qualified or partial:", style.Heading) + "\n")
	for _, example := range []string{
		"anno read app.go@types:Pair^fields",
		"anno read app.go^fields",
	} {
		b.WriteString("  " + p.Paint(example, style.Command) + "\n")
	}

	b.WriteString("\n" + p.Paint(
		"a partial chain that matches more than once fails, listing every candidate\n"+
			"fully qualified so it can be pasted back.", style.Quiet) + "\n")

	// The colour settings are the shared package's own words, so every Orc tool
	// documents them identically. The flags are Anno's own, and are said here
	// because a caller assembling one command should not have to set an
	// environment variable to keep escapes out of it.
	b.WriteString("\n" + theme.Help() + "\n")
	b.WriteString("        " + p.Paint(FlagNoColour, style.Setting) + " and " + p.Paint(FlagColour, style.Setting) +
		p.Paint(" do the same for one command, before or after it", style.Quiet) + "\n")

	b.WriteString("\n" + p.Paint("exit codes:", style.Heading) +
		" 0 ok · 1 usage · 2 not found · 3 ambiguous · 4 parse · 5 i/o · 6 conflict · 9 out of scope")
	return b.String()
}

// commandLine draws one command.
//
// The painted line and its plain twin are built side by side, and every pad measures
// the plain one: escape sequences occupy no columns, so padding the painted string
// would indent a coloured line differently from an uncoloured one.
func commandLine(p style.Palette, c entry) string {
	painted := "  " + p.Paint("anno", style.Quiet) + " " + p.Paint(c.name, style.Command)
	plain := "  anno " + c.name
	painted, plain = padPair(painted, plain, len("  anno ")+nameColumn)

	painted, plain = painted+p.Paint(c.args, style.Value), plain+c.args
	painted, _ = padPair(painted, plain, argsColumn)

	return painted + p.Paint(c.does, style.Quiet) + "\n"
}

// padPair extends both forms with the same spaces, measuring the plain one. A column
// that is already full gets a single space, so nothing ever runs together.
func padPair(painted, plain string, width int) (string, string) {
	gap := width - theme.Width(plain)
	if gap < 1 {
		gap = 1
	}
	spaces := strings.Repeat(" ", gap)
	return painted + spaces, plain + spaces
}

// commandHelp is `anno help <command>`: that command and nothing else.
//
// Nothing else is the point. A reader who asked about one verb has already seen the
// list — that is how they learned the verb's name — and printing it again, with the
// chain syntax and the exit codes after it, buries the two lines they came for.
func commandHelp(p style.Palette, name string) (string, bool) {
	var got entry
	found := false
	for _, c := range commands {
		if c.name == name {
			got, found = c, true
			break
		}
	}
	if !found {
		return "", false
	}

	var b strings.Builder
	b.WriteString(p.Paint("anno", style.Quiet) + " " + p.Paint(got.name, style.Command))
	if got.args != "" {
		b.WriteString(" " + p.Paint(got.args, style.Value))
	}
	b.WriteString("\n\n  " + p.Paint(got.does, style.Quiet) + "\n")

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
		b.WriteString("\n" + p.Paint("examples", style.Heading) + "\n")
		for _, example := range got.examples {
			b.WriteString("  " + p.Paint(example, style.Command) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n"), true
}

// noSuchTopic is `anno help <something that is not a command>`.
func noSuchTopic(name string) error {
	if near := guess.Nearest(name, verbs()); near != "" {
		return fault.Usage{Reason: fmt.Sprintf("no help for %q — did you mean `anno help %s`?", name, near)}
	}
	return fault.Usage{Reason: fmt.Sprintf("no help for %q; `anno help` lists the commands", name)}
}

// brief is what `anno` on its own prints: the verbs and nothing else.
//
// Anno has five, so they fit on one line and there is nothing to group. What the
// full screen adds is the chain syntax, which is the part worth reading — once,
// and not every time somebody forgets whether it is `read` or `show`.
func brief(p style.Palette) string {
	var b strings.Builder

	b.WriteString(p.Paint("anno", style.Tool) + " — " +
		p.Paint("a minimal file annotation manager", style.Quiet) + "\n\n")

	painted := make([]string, len(commands))
	for i, c := range commands {
		painted[i] = p.Paint(c.name, style.Command)
	}
	b.WriteString("  " + strings.Join(painted, p.Paint(" · ", style.Quiet)) + "\n")
	b.WriteString("\n  " + p.Paint("anno help for all of it: every form, and the chain syntax", style.Quiet))
	return b.String()
}

// verbs is every command anno answers to, for suggesting the one that was meant.
// Derived from the command table, so one added there is guessable here without
// anyone remembering to.
func verbs() []string {
	out := make([]string, 0, len(commands)+1)
	for _, c := range commands {
		out = append(out, c.name)
	}
	return append(out, "help")
}

// unknown is the refusal for a verb anno does not have: one line, with a guess
// when there is a good one, rather than the whole screen after it.
func unknown(command string) error {
	if near := guess.Nearest(command, verbs()); near != "" {
		return fault.Usage{Reason: fmt.Sprintf("unknown command %q — did you mean `anno %s`?", command, near)}
	}
	return fault.Usage{Reason: fmt.Sprintf("unknown command %q; `anno help` lists them", command)}
}
