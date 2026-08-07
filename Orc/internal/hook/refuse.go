package hook

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"orc/orc/internal/model"
)

// Every refusal here obeys one rule, which Macmuffin's hook doc states and this tree
// applies everywhere: **a refusal that does not say how to proceed just gets worked
// around.** An agent told only "no" will try the same thing another way; an agent told
// which permission it lacks and which command grants it will ask for that instead.
//
// So each message has three parts: what was refused, why, and the way forward. They go
// to stderr, which Claude feeds back to the agent as the reason its tool call failed.

// refuseSubagent explains the one denial that has nothing to do with paths.
func refuseSubagent(tool string) string {
	return join(
		"orc: the "+tool+" tool is off for every identity in this fleet.",
		"",
		"  parallelism goes through the work list, so `orc status` is the whole picture of",
		"  what is thinking and the spawn budget is exact. to get more hands:",
		"",
		"    orc new identity <name> && orc assign role <name> <role>",
		"    orc employ <name>",
		"",
		"  which needs a `spawn` permission, and says so if you do not have one.",
	)
}

// refuseNested explains a subagent started through a shell.
//
// Worded as containment rather than as a permission, because that is what it is: no
// clause permits it and there is nothing to ask for. An identity with `shell(**)` is
// trusted with a shell, not with a second fleet nobody can see.
func refuseNested(program string) string {
	return join(
		"orc: `"+program+"` starts a session, and starting one from a shell is off in this fleet.",
		"",
		"  it is the Agent tool by another route: the work list would not know about it,",
		"  the spawn budget would not count it, and `orc status` would not show it. to get",
		"  more hands:",
		"",
		"    orc new identity <name> && orc assign role <name> <role>",
		"    orc employ <name>",
		"",
		"  which needs a `spawn` permission, and says so if you do not have one.",
	)
}

// refuseStore explains the escape.
//
// It is worded as a containment failure rather than as a permission problem, because
// that is what it is: no clause can permit it, so there is nothing to ask for. The
// message names the exceptions, because an agent that wandered in by accident — a
// glob, a recursive search — needs to know which parts of that directory *are* its own.
func refuseStore(target, root string) string {
	return join(
		fmt.Sprintf("orc: %s is part of the fleet's own state, and nothing may touch it.", target),
		"",
		fmt.Sprintf("  %s holds every identity's credential in plaintext, along with the", root),
		"  journals that decide who may do what. no permission grants access to it, and no",
		"  permission ever will — it is how orc proves who you are.",
		"",
		"  what *is* yours in there: your workspace, your CLAUDE.md, and your memory/",
		"  directory. `orc introspect` shows your standing, and `orc env` your paths.",
	)
}

// refuseBlind explains the third rung of the ladder.
//
// This is the refusal that costs an agent that did nothing wrong, so it is the one
// that most needs to name the fix. It says which command diagnoses it, because the
// agent cannot: whether the store is unreadable is not something a session can see.
func refuseBlind(targets []string) string {
	return join(
		fmt.Sprintf("orc: cannot tell whether you may write %s, so it is refused.", strings.Join(targets, " ")),
		"",
		"  neither the fleet's store nor this session's permission snapshot could be read.",
		"  reads still work; writes are refused until that is fixed, because a write that",
		"  nobody could authorise is the one thing this guard exists to stop.",
		"",
		"  ask the operator to run `orc doctor`, then `orc poke` you to carry on.",
	)
}

// refuseSlow explains the deadline.
//
// It is a different message from refuseBlind on purpose. "Nothing could be read" and
// "nothing answered in time" send an operator to different places — a broken store and
// a stalled disk are not the same problem — and the agent quoting this back is how
// anybody finds out which.
func refuseSlow(targets []string, within time.Duration) string {
	return join(
		fmt.Sprintf("orc: the fleet did not answer within %s, so writing %s is refused.",
			within, strings.Join(targets, " ")),
		"",
		"  reads still work. a write nobody could authorise in time is refused rather than",
		"  waited on, because a hook that waited would freeze this session instead.",
		"",
		"  ask the operator to run `orc doctor` — a store on a slow or stalled disk is what",
		"  this usually means — then `orc poke` you to carry on.",
	)
}

// refuseOutside explains a path that is not in the workspace at all.
// refuseOutside is a path outside the project altogether, which no clause reaches.
func refuseOutside(target, workspace, project string, kind model.Kind, source string) string {
	act := "write"
	if kind == model.KindRead {
		act = "read"
	}
	return join(
		fmt.Sprintf("orc: %s is outside the project, so you may not %s it.", target, act),
		"",
		fmt.Sprintf("  your workspace is %s, and everything in it is yours.", workspace),
		fmt.Sprintf("  the project is %s, and a permission can reach inside it.", project),
		fmt.Sprintf("  (%s)", source),
		"",
		"  nothing outside the project is grantable, so this is not a permission to ask for.",
	)
}

// refuseUngranted is a path inside the project that no clause covers.
//
// Different from being outside it, and the difference is what somebody does next:
// this one is a permission away, and the message says which shape of clause would
// cover it so the ask is exact rather than a guess.
func refuseUngranted(target, rel, project string, kind model.Kind, source string) string {
	act, verb := "write", "write"
	if kind == model.KindRead {
		act, verb = "read", "read"
	}
	return join(
		fmt.Sprintf("orc: no permission of yours covers %s, so you may not %s it.", target, act),
		"",
		fmt.Sprintf("  it is %s inside the project at %s, and a clause is measured from there.", rel, project),
		fmt.Sprintf("  (%s)", source),
		"",
		"  ask your boss for a permission holding a clause that reaches it:",
		fmt.Sprintf("    %s(%s)", verb, rel),
		"    orc grant permission <you> <permission>",
	)
}

func join(lines ...string) string { return strings.Join(lines, "\n") }

// --- the shell ------------------------------------------------------------

// refuseShell explains a command no clause covers.
//
// It names the command rather than the whole line, because the line is usually
// long and exactly one word of it is the problem — and it prints the clause that
// would fix it, ready to be asked for.
func refuseShell(name, line string, patterns []model.Pattern, source string) string {
	allowed := shellTerms(patterns)

	lines := []string{
		fmt.Sprintf("orc: you may not run %s.", name),
		"",
		fmt.Sprintf("  the command:  %s", ellipsis(line, 120)),
	}
	if len(allowed) == 0 {
		lines = append(lines,
			"  you hold no shell permission, so you may run only the commands",
			fmt.Sprintf("  every identity may:  %s", defaultSet()))
	} else {
		lines = append(lines,
			fmt.Sprintf("  you may run:  %s", strings.Join(allowed, "  ")),
			fmt.Sprintf("  and always:   %s", defaultSet()))
	}
	return join(append(lines,
		fmt.Sprintf("  (%s)", source),
		"",
		"  ask your boss for a permission that covers it:",
		fmt.Sprintf("    orc new permission <name> <floor> 'shell(%s)'", name),
	)...)
}

// defaultSet renders what every identity may run, with the carve-outs shown.
func defaultSet() string {
	return strings.Join(model.InnocuousWords(), "  ")
}

// refuseGuarded explains the one part of a default command that is not default.
//
// It is its own message because the ordinary one would be actively misleading.
// `mailman` needs no permission, the agent has been using it all session, and
// being told "you may not run mailman" invites it to conclude the gate is
// broken. What is refused is the subcommand, so that is what the message is
// about — and it says what the subcommand can do, because "ask for a clause" is
// not much use to an agent that does not know why this one is different.
func refuseGuarded(name, sub, line string, patterns []model.Pattern, source string) string {
	lines := []string{
		fmt.Sprintf("orc: you may run %s, but not %s %s.", name, name, sub),
		"",
		fmt.Sprintf("  the command:  %s", ellipsis(line, 120)),
		fmt.Sprintf("  %s checks who is calling and shows you your own mailbox, which is why it", name),
		fmt.Sprintf("  needs no permission. %s %s is the part that does not check: it provisions", name, sub),
		"  mailboxes and can hand its caller every message in the fleet.",
	}
	if allowed := shellTerms(patterns); len(allowed) > 0 {
		lines = append(lines, fmt.Sprintf("  you may run:  %s", strings.Join(allowed, "  ")))
	}
	return join(append(lines,
		fmt.Sprintf("  (%s)", source),
		"",
		"  it is orc that provisions mailboxes — `orc new identity` does it for you.",
		"  if you genuinely need the raw command, ask your boss:",
		fmt.Sprintf("    orc new permission <name> <floor> 'shell(%s)'", name),
	)...)
}

// refuseOpaque explains a line whose commands cannot be read.
//
// The distinction matters to whoever reads it: this is not "you may not run
// that", it is "nobody can tell what that runs". An agent told the first will
// ask for a permission naming a command; told the second, it will rephrase.
func refuseOpaque(line string, patterns []model.Pattern, source string) string {
	allowed := shellTerms(patterns)
	lines := []string{
		"orc: that command line hides what it runs.",
		"",
		fmt.Sprintf("  the command:  %s", ellipsis(line, 120)),
		"  substitutions — $(…), `…`, ${…} — and interpreters like sh -c, eval and",
		"  xargs take a program as data, so the name in front of them says nothing",
		"  about what would happen.",
	}
	if len(allowed) > 0 {
		lines = append(lines, fmt.Sprintf("  you may run:  %s", strings.Join(allowed, "  ")))
	}
	return join(append(lines,
		fmt.Sprintf("  (%s)", source),
		"",
		"  write it as commands whose names are visible, or ask for shell(**),",
		"  which is the only clause that can cover a line nobody can read.",
	)...)
}

// refuseBlindShell is the third rung for a command.
//
// A read passes when no permission can be read, because a blocked read discloses
// nothing the agent does not already have. A command is not like that — it could
// be anything — so the shell stops instead.
func refuseBlindShell(line string) string {
	return join(
		"orc: your permissions cannot be read, so the shell is closed.",
		"",
		fmt.Sprintf("  the command:  %s", ellipsis(line, 120)),
		"  a read would still pass here; a command could be anything, so it does not.",
		"",
		"  check the store is reachable:",
		"    orc doctor",
	)
}

// shellTerms is what the identity's shell clauses allow, as words.
func shellTerms(patterns []model.Pattern) []string {
	out := []string{}
	for _, p := range patterns {
		if p.Kind() == model.KindShell {
			out = append(out, p.Arg())
		}
	}
	sort.Strings(out)
	return out
}

// ellipsis keeps a refusal to one screen. A command line can be a paragraph, and
// the part that matters is the beginning.
func ellipsis(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
