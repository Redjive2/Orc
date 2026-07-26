package cli_test

import (
	"strings"
	"testing"

	"orc/common/fault"
)

// Three screens, three questions.
//
//	muff help     everything — the verbs, the scales, the identity rules
//	muff          the verbs, and the error that nothing was named
//	muff <bad>    the refusal, and a guess
//
// They used to be one: every usage error printed the whole screen after it, so
// the answer to a typo was somewhere in fifty lines.

func TestBareMuffShowsTheVerbs(t *testing.T) {
	r := newRig(t)
	got := r.run("alice")

	if got.code != fault.CodeUsage {
		t.Fatalf("bare muff exited %d, want %d", got.code, fault.CodeUsage)
	}
	if !strings.HasPrefix(got.stderr, "muff: no command given") {
		t.Errorf("the error should come first, so every diagnostic starts the same way:\n%s", got.stderr)
	}
	for _, verb := range []string{"create", "push", "claim", "pool", "info", "scope",
		"worktree", "check-scope", "status", "complete", "delete", "assign",
		"invite", "kick", "leave", "verify"} {
		if !strings.Contains(got.stderr, verb) {
			t.Errorf("the short screen does not list %q:\n%s", verb, got.stderr)
		}
	}
	for _, absent := range []string{"exit codes:", "scores run 1 to 5", "identity comes from orc"} {
		if strings.Contains(got.stderr, absent) {
			t.Errorf("the short screen carried %q from the full one:\n%s", absent, got.stderr)
		}
	}
}

// TestEveryMuffVerbIsInAGroup: the short screen's grouping is hand-set, so this is
// what keeps it from going stale when a command is added to the table.
func TestEveryMuffVerbIsInAGroup(t *testing.T) {
	r := newRig(t)
	short := r.run("alice").stderr
	full := r.ok("alice", "help").stdout

	// Every verb the full screen documents appears in the short one, and the
	// short one invents none: both are read out of the same table.
	for _, line := range strings.Split(full, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 || fields[0] != "muff" {
			continue
		}
		if !strings.Contains(short, fields[1]) {
			t.Errorf("the full screen documents %q and the short one omits it:\n%s", fields[1], short)
		}
	}
}

func TestUnknownMuffCommandGuesses(t *testing.T) {
	r := newRig(t)

	got := r.run("alice", "clam")
	if got.code != fault.CodeUsage {
		t.Fatalf("an unknown command exited %d, want %d", got.code, fault.CodeUsage)
	}
	if !strings.Contains(got.stderr, "muff claim") {
		t.Errorf("a near miss should be guessed:\n%s", got.stderr)
	}
	if lines := strings.Count(strings.TrimSpace(got.stderr), "\n"); lines != 0 {
		t.Errorf("the refusal is %d lines, want one:\n%s", lines+1, got.stderr)
	}

	got = r.run("alice", "frobnicate")
	if strings.Contains(got.stderr, "did you mean") {
		t.Errorf("it guessed at a word that resembles nothing:\n%s", got.stderr)
	}
	if !strings.Contains(got.stderr, "muff help") {
		t.Errorf("with no guess it should at least point at help:\n%s", got.stderr)
	}
}

// TestOtherMuffUsageErrorsAreJustTheError: a refusal that already says what was
// wrong does not need the verb list under it.
func TestOtherMuffUsageErrorsAreJustTheError(t *testing.T) {
	r := newRig(t)
	got := r.run("alice", "push")

	if got.code != fault.CodeUsage {
		t.Fatalf("exit %d, want %d\n%s", got.code, fault.CodeUsage, got.stderr)
	}
	for _, absent := range []string{"exit codes:", "the board", "scores run 1 to 5"} {
		if strings.Contains(got.stderr, absent) {
			t.Errorf("a usage error printed %q from a help screen:\n%s", absent, got.stderr)
		}
	}
}
