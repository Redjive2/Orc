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
// Two things it cannot see, and both are handled by refusing rather than by
// guessing:
//
//   - **Substitution.** `$(…)`, backticks, and `<(…)` run a command this cannot
//     name, so a line containing one is opaque.
//   - **Interpretation.** `eval`, `sh -c`, `bash -c`, `xargs` and friends take a
//     program as *data*. The name is right there and says nothing about what
//     runs.
//
// Opaque returns true for those, and the caller refuses unless the identity is
// allowed everything anyway. That is the only honest reading: a gate that let
// `$(rm -rf /)` past because it could not find a command name would be a gate
// that fails open on exactly the input designed to defeat it.

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

// interpreters take a program as an argument, so their own name says nothing
// about what will run.
// `sudo` and `doas` are here as privilege changes rather than as interpreters:
// what runs under them is not what a clause named.
var interpreters = map[string]bool{
	"awk": true, "bash": true, "dash": true, "doas": true, "eval": true,
	"ksh": true, "perl": true, "python": true, "python3": true, "ruby": true,
	"sh": true, "source": true, "sudo": true, "xargs": true, "zsh": true,
}

// Commands returns every command name a line would run, in the order they appear.
//
// Names are lowercased base names, so `/usr/bin/ls` and `ls` are one word. A line
// this cannot read returns whatever it did find plus `opaque` — see Opaque.
func Commands(line string) []string {
	var out []string
	for _, segment := range splitCommands(line) {
		if name := commandOf(segment); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// commandOf finds the name one command segment runs.
//
// A segment that only changes directory runs nothing, and says so with the empty
// string. `cd x && ls` is two segments and one command.
func commandOf(segment string) string {
	for i, field := range strings.Fields(segment) {
		word := unquote(field)

		if i == 0 && strings.ToLower(filepath.Base(word)) == "cd" {
			return ""
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
		return name
	}
	return ""
}

// Opaque reports whether a line hides what it runs.
//
// It is deliberately eager. A false positive costs somebody a rephrase; a false
// negative costs the whole gate, because every one of these shapes is what
// somebody reaches for when a command name is the thing being checked.
func Opaque(line string) bool {
	if strings.Contains(line, "$(") || strings.Contains(line, "`") ||
		strings.Contains(line, "${") || strings.Contains(line, "<(") ||
		strings.Contains(line, ">(") {
		return true
	}
	for _, segment := range splitCommands(line) {
		if interpreters[commandOf(segment)] {
			return true
		}
	}
	return false
}
