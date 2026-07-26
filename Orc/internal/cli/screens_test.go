package cli_test

import (
	"strings"
	"testing"

	"orc/common/fault"
)

// Three screens, three questions.
//
//	orc help        everything — the verbs, the model, the patterns, the load table
//	orc             the verbs, and the error that nothing was named
//	orc <bad>       the refusal, and a guess
//
// They used to be one screen: every usage error printed the whole ninety-line
// help after it, which meant the answer to a typo was somewhere in ninety lines.

func TestBareOrcShowsTheVerbs(t *testing.T) {
	r := fullFleet(t)
	got := r.run("boss")

	if got.code != fault.CodeUsage {
		t.Fatalf("bare orc exited %d, want %d", got.code, fault.CodeUsage)
	}
	if !strings.HasPrefix(got.stderr, "orc: no command given") {
		t.Errorf("the error should come first, so every diagnostic starts the same way:\n%s", got.stderr)
	}
	// Every verb, in one of the groups.
	for _, verb := range []string{"bootstrap", "new", "assign", "grant", "revoke", "move",
		"employ", "fire", "tend", "attach", "poke", "refresh", "status", "introspect",
		"check-control", "env", "verify", "doctor", "owner", "remove"} {
		if !strings.Contains(got.stderr, verb) {
			t.Errorf("the short screen does not list %q:\n%s", verb, got.stderr)
		}
	}
	if !strings.Contains(got.stderr, "orc help") {
		t.Errorf("the short screen should point at the full one:\n%s", got.stderr)
	}
	// And not the full screen: the point of it is that it is short.
	// Sentences only the full screen has — the pointer line names its sections,
	// so matching on section titles would match the pointer.
	for _, absent := range []string{"authority is a number on a", "exit codes:", "kinds:"} {
		if strings.Contains(got.stderr, absent) {
			t.Errorf("the short screen carried %q from the full one:\n%s", absent, got.stderr)
		}
	}
}

func TestUnknownVerbGuesses(t *testing.T) {
	r := fullFleet(t)

	got := r.run("boss", "statsu")
	if got.code != fault.CodeUsage {
		t.Fatalf("an unknown verb exited %d, want %d", got.code, fault.CodeUsage)
	}
	if !strings.Contains(got.stderr, "orc status") {
		t.Errorf("a near miss should be guessed:\n%s", got.stderr)
	}
	// One line. Not a screen.
	if lines := strings.Count(strings.TrimSpace(got.stderr), "\n"); lines != 0 {
		t.Errorf("the refusal is %d lines, want one:\n%s", lines+1, got.stderr)
	}

	// A word resembling nothing gets no guess, only the pointer — guessing at it
	// would be worse than silence, because a guess is followed.
	got = r.run("boss", "frobnicate")
	if strings.Contains(got.stderr, "did you mean") {
		t.Errorf("it guessed at a word that resembles nothing:\n%s", got.stderr)
	}
	if !strings.Contains(got.stderr, "orc help") {
		t.Errorf("with no guess it should at least point at help:\n%s", got.stderr)
	}
}

// TestOtherUsageErrorsAreJustTheError: a wrong argument count is a refusal that
// already says what was wrong, and does not need the verb list under it.
func TestOtherUsageErrorsAreJustTheError(t *testing.T) {
	r := fullFleet(t)
	got := r.run("boss", "move")

	if got.code != fault.CodeUsage {
		t.Fatalf("exit %d, want %d\n%s", got.code, fault.CodeUsage, got.stderr)
	}
	for _, absent := range []string{"authority is a number on a", "exit codes:", "the fleet "} {
		if strings.Contains(got.stderr, absent) {
			t.Errorf("a usage error printed %q from a help screen:\n%s", absent, got.stderr)
		}
	}
}

// TestTheShortScreenIsAColourLayerToo: the property every screen in this tree
// holds — stripped of escapes, the coloured rendering is the plain one, byte for
// byte. A new screen is exactly where that gets forgotten.
func TestTheShortScreenIsAColourLayerToo(t *testing.T) {
	r := fullFleet(t)
	plain := r.run("boss", "--no-color")
	coloured := r.run("boss", "--color")

	if stripped := strip(coloured.stderr); stripped != plain.stderr {
		t.Errorf("the short screen differs once colour is stripped:\nplain:\n%s\nstripped:\n%s",
			plain.stderr, stripped)
	}
	if !strings.Contains(coloured.stderr, "\x1b[") {
		t.Errorf("--color produced no colour at all, so the comparison proved nothing")
	}
}
