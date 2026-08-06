package hook_test

import (
	"strings"
	"testing"

	"orc/orc/internal/hook"
)

// Parallelism goes through the work list, so a session that can start another one
// makes `orc status` an incomplete picture and the spawn budget an estimate.
//
// There are three ways to start one, and Orc used to stop one of them.

// Claude decides whether a tool call is a subagent by testing the name against both
// `Agent` and `Task`. Orc named only the first — so a fleet running a build that
// spells it `Task` had the rule in its settings, the refusal in its hook, a line in
// `orc doctor` saying subagents were off, and no denial at all.
func TestBothSpellingsOfTheSubagentToolAreDenied(t *testing.T) {
	r := newRig(t)
	// Written out rather than ranged over hook.SubagentTools. A test that iterates
	// the list it is pinning shrinks when the list does, which is exactly the change
	// it exists to catch.
	for _, name := range []string{"Agent", "Task"} {
		out := r.call(r.as("ember", nil), map[string]any{
			"hook_event_name": "PreToolUse",
			"tool_name":       name,
			"tool_input":      map[string]any{"description": "go and do a thing"},
		})
		if out.Code != hook.CodeBlock {
			t.Errorf("the %s tool was allowed", name)
		}
		if !strings.Contains(out.Stderr, name) {
			t.Errorf("the refusal does not name the tool it refused:\n%s", out.Stderr)
		}
	}
}

// A nested Claude is a subagent with a shell in front of it, which is the hole
// `orc doctor` used to name rather than close. No clause permits it: an identity
// trusted with a shell is not thereby trusted with a second fleet nobody can see.
func TestAShellCannotStartASession(t *testing.T) {
	r := newRig(t)
	for _, line := range []string{
		"claude -p 'do the thing'",
		"/usr/local/bin/claude --print hello",
		"cd /tmp && claude -p x",
		"orc-session --identity ember",
	} {
		out := r.call(r.as("ember", nil), map[string]any{
			"hook_event_name": "PreToolUse",
			"tool_name":       "Bash",
			"tool_input":      map[string]any{"command": line},
		})
		if out.Code != hook.CodeBlock {
			t.Errorf("%q was allowed", line)
			continue
		}
		if !strings.Contains(out.Stderr, "orc employ") {
			t.Errorf("%q was refused without saying how to get more hands:\n%s", line, out.Stderr)
		}
	}
}

// And a command that merely mentions one is not refused. A rule that fired on the
// word rather than on the command would stop an agent reading its own notes.
func TestOrdinaryCommandsAreNotMistakenForSessions(t *testing.T) {
	r := newRig(t)
	for _, line := range []string{"echo claude", "orc status", "grep claude notes.md"} {
		out := r.call(r.as("ember", nil), map[string]any{
			"hook_event_name": "PreToolUse",
			"tool_name":       "Bash",
			"tool_input":      map[string]any{"command": line},
		})
		if strings.Contains(out.Stderr, "starts a session") {
			t.Errorf("%q was mistaken for starting a session:\n%s", line, out.Stderr)
		}
	}
}
