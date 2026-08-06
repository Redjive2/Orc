package hook

import (
	"path/filepath"
	"strings"
)

// Reading a command line well enough to gate it.
//
// A shell is not a thing this can evaluate, and nothing here pretends otherwise.
// What it does is find the *names* being run — the first word of each command in
// a line, past the wrappers that precede one — and hand them to the `shell`
// clause. `ls -la`, `cd x && ls`, `sudo ls`, `FOO=1 ls`, `/bin/ls` are all `ls`.
//
// One thing it cannot see, and it is handled by refusing rather than by guessing:
// **substitution**. `$(…)`, backticks, and `<(…)` run a command this cannot name,
// so a line containing one is opaque and needs the clause that covers everything.
// A gate that let `$(rm -rf /)` past because it could not find a command name
// would be a gate that fails open on exactly the input designed to defeat it.
//
// **Interpretation is not the same problem**, though it was treated as one.
// `python3 -c …` and `sh -c …` take a program as data, so the name says nothing
// about what will *happen* — but it says exactly what will *run*, and that is the
// question a clause answers. So an interpreter goes through the ordinary check
// and a clause naming it permits it. What that grants is total, within that
// interpreter, and the toolkit prices it that way rather than pretending
// otherwise. See the note on `interpreters`.

// wrappers run another command, so the interesting name is the next word.
//
// `cd` is *not* one of them: its argument is a directory, not a program, so
// treating it as a wrapper made `cd /tmp && ls` read as running `/tmp`. It ends
// its segment instead — see commandOf.
//
// Neither is `sudo`, for two reasons that point the same way. Its flags take
// values, so `sudo -u bob ls` read as running `bob` — a refusal naming a person.
// And running as somebody else is not the thing a clause described: `shell(ls)`
// named `ls`, not `ls` as root. It is an interpreter below, so it needs the
// clause that covers everything.
var wrappers = map[string]bool{
	"command": true, "env": true, "exec": true,
	"nice": true, "nohup": true, "time": true,
}

// Interpreters take a program as an argument, so their own name says nothing
// about *what* will run — only about what could.
//
// That is a different thing from a substitution, and the two used to be treated
// as one. `$(…)` hides the name: nothing can say what it runs, so nothing
// narrower than everything can honestly permit it. `python3 -c …` hides nothing
// of the sort — the name is right there, and a clause naming it is a decision
// somebody made about python3 specifically.
//
// Conflating them made `shell(python3)` a clause that could not be satisfied.
// The toolkit's own `shell-build` named python, python3, sh and bash, and every
// one of them was refused as unreadable — a permission that lied about itself.
//
// So naming an interpreter now permits it, and what that grants is stated
// plainly rather than implied: **it is everything that interpreter can do.**
// `shell(python3)` is not a narrow grant. It is a shell, reached through python,
// and the toolkit prices it accordingly — see FloorShellInterpret.
//
// `sudo` and `doas` stay here as privilege changes rather than as interpreters:
// what runs under them is not what a clause named, and `shell(sudo)` naming a
// command that then runs as somebody else is not a decision about `sudo`.
var interpreters = map[string]bool{
	"awk": true, "bash": true, "dash": true, "doas": true, "eval": true,
	"ksh": true, "perl": true, "python": true, "python3": true, "ruby": true,
	"sh": true, "source": true, "sudo": true, "xargs": true, "zsh": true,
}

// Invocation is one command a line would run: the name being gated, and what it
// was handed.
//
// The arguments are carried because a couple of commands are not one privilege.
// `mailman` reads the caller's own mail and needs no clause; `mailman admin`
// provisions mailboxes and does — see model.InnocuousRun. Nothing else looks at
// them, and in particular no path clause is decided here.
type Invocation struct {
	Name string
	Args []string
}

// Runs returns every command a line would run, in the order they appear.
//
// Names are lowercased base names, so `/usr/bin/ls` and `ls` are one word. A line
// this cannot read is handled by Opaque rather than here.
func Runs(line string) []Invocation {
	var out []Invocation
	for _, segment := range splitCommands(line) {
		if run, ok := runOf(segment); ok {
			out = append(out, run)
		}
	}
	return out
}

// Commands returns just the names, for callers that gate on nothing else.
func Commands(line string) []string {
	runs := Runs(line)
	out := make([]string, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.Name)
	}
	return out
}

// commandOf finds the name one command segment runs, or the empty string.
func commandOf(segment string) string {
	run, _ := runOf(segment)
	return run.Name
}

// runOf finds what one command segment runs, and what it hands it.
//
// A segment that only changes directory runs nothing, and says so with false.
// `cd x && ls` is two segments and one command.
func runOf(segment string) (Invocation, bool) {
	fields := strings.Fields(segment)
	for i, field := range fields {
		word := unquote(field)

		if i == 0 && strings.ToLower(filepath.Base(word)) == "cd" {
			return Invocation{}, false
		}

		// `FOO=bar cmd` — an assignment prefixes a command rather than being one.
		if i := strings.IndexByte(word, '='); i > 0 && !strings.ContainsAny(word[:i], "/\\$") {
			continue
		}
		// A bare redirection or operator is not a name.
		if word == "" || strings.HasPrefix(word, "-") || strings.ContainsAny(word, "<>") {
			continue
		}
		name := strings.ToLower(filepath.Base(word))
		if wrappers[name] {
			continue
		}

		args := make([]string, 0, len(fields)-i-1)
		for _, rest := range fields[i+1:] {
			args = append(args, unquote(rest))
		}
		return Invocation{Name: name, Args: args}, true
	}
	return Invocation{}, false
}

// Opaque reports whether a line hides *the name* of what it runs.
//
// Substitutions only. `$(…)`, backticks, `${…}` and process substitution all
// produce a command nothing can name in advance, so no clause narrower than
// everything can honestly permit one.
//
// Interpreters used to be folded in here and are not any more. Their name is
// knowable — it is the first word — so they go through the ordinary check, and a
// clause that names one permits it. What changed is the question being asked: not
// "could this do anything?", which is true of most commands, but "can this line
// be attributed to a name a clause could have decided about?"
//
// Still deliberately eager about what remains. A false positive costs somebody a
// rephrase; a false negative costs the whole gate, because a substitution is what
// somebody reaches for when a command name is the thing being checked.
func Opaque(line string) bool {
	return strings.Contains(line, "$(") || strings.Contains(line, "`") ||
		strings.Contains(line, "${") || strings.Contains(line, "<(") ||
		strings.Contains(line, ">(")
}

// Interpreter reports whether a name takes a program as an argument.
//
// Exported so a refusal can say what naming one would grant, rather than making
// an operator work out why `shell(ls)` and `shell(python3)` are priced so
// differently.
func Interpreter(name string) bool { return interpreters[strings.ToLower(name)] }
