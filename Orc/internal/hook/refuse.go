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
func refuseSubagent() string {
	return join(
		"orc: the Agent tool is off for every identity in this fleet.",
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
func refuseOutside(target, workspace string, kind model.Kind, source string) string {
	act := "write"
	if kind == model.KindRead {
		act = "read"
	}
	return join(
		fmt.Sprintf("orc: %s is outside your workspace, so you may not %s it.", target, act),
		"",
		fmt.Sprintf("  your workspace is %s, and every permission you hold is relative to it.", workspace),
		fmt.Sprintf("  (%s)", source),
		"",
		"  work inside it, or ask your boss for a permission that covers where you are:",
		fmt.Sprintf("    orc grant permission %s <permission>", "<you>"),
	)
}

// refuseClause explains a path inside the workspace that no clause covers.
//
// It lists what *is* allowed, because that is the difference between a refusal an
// agent can act on and one it can only retry. The list is the same shape `orc status`
// prints, so the two screens agree.
func refuseClause(target, rel string, kind model.Kind, patterns []model.Pattern, source string) string {
	act := "write"
	if kind == model.KindRead {
		act = "read"
	}

	allowed := make([]string, 0, len(patterns))
	for _, p := range patterns {
		if p.Kind() == kind {
			allowed = append(allowed, p.Arg())
		}
	}
	sort.Strings(allowed)

	lines := []string{
		fmt.Sprintf("orc: you may not %s %s.", act, target),
		"",
	}
	if len(allowed) == 0 {
		lines = append(lines,
			fmt.Sprintf("  you hold no %s permission at all.", act),
		)
	} else {
		lines = append(lines,
			fmt.Sprintf("  you may %s:  %s", act, strings.Join(allowed, "  ")),
			fmt.Sprintf("  you asked for:  %s", rel),
		)
	}
	return join(append(lines,
		fmt.Sprintf("  (%s)", source),
		"",
		"  `orc introspect` shows your whole standing. a wider permission has to come from",
		"  your boss, and it is capped by what they hold.",
	)...)
}

func join(lines ...string) string { return strings.Join(lines, "\n") }
