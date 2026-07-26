package cli_test

import (
	"strings"
	"testing"

	"orc/anno/internal/cli"
)

// Three screens, three questions.
//
//	anno help    everything — every form, and the chain syntax
//	anno         the verbs, and the error that nothing was named
//	anno <bad>   the refusal, and a guess
//
// They used to be one: every usage error printed the whole screen after it, so
// the answer to a mistyped path was somewhere in forty lines.

func TestBareAnnoShowsTheVerbs(t *testing.T) {
	got := run(t, "")

	if got.code != cli.CodeUsage {
		t.Fatalf("bare anno exited %d, want %d", got.code, cli.CodeUsage)
	}
	if !strings.HasPrefix(got.stderr, "anno: no command given") {
		t.Errorf("the error should come first, so every diagnostic starts the same way:\n%s", got.stderr)
	}
	for _, verb := range []string{"index", "overview", "read", "find", "write"} {
		if !strings.Contains(got.stderr, verb) {
			t.Errorf("the short screen does not list %q:\n%s", verb, got.stderr)
		}
	}
	for _, absent := range []string{"exit codes:", "a chain addresses an annotation", "usage:"} {
		if strings.Contains(got.stderr, absent) {
			t.Errorf("the short screen carried %q from the full one:\n%s", absent, got.stderr)
		}
	}
}

func TestUnknownAnnoCommandGuesses(t *testing.T) {
	got := run(t, "", "raed")
	if got.code != cli.CodeUsage {
		t.Fatalf("an unknown command exited %d, want %d", got.code, cli.CodeUsage)
	}
	if !strings.Contains(got.stderr, "anno read") {
		t.Errorf("a near miss should be guessed:\n%s", got.stderr)
	}
	if lines := strings.Count(strings.TrimSpace(got.stderr), "\n"); lines != 0 {
		t.Errorf("the refusal is %d lines, want one:\n%s", lines+1, got.stderr)
	}

	got = run(t, "", "frobnicate")
	if strings.Contains(got.stderr, "did you mean") {
		t.Errorf("it guessed at a word that resembles nothing:\n%s", got.stderr)
	}
	if !strings.Contains(got.stderr, "anno help") {
		t.Errorf("with no guess it should at least point at help:\n%s", got.stderr)
	}
}

// TestOtherAnnoUsageErrorsAreJustTheError: a refusal that already says what was
// wrong does not need the chain syntax under it.
func TestOtherAnnoUsageErrorsAreJustTheError(t *testing.T) {
	got := run(t, "", "index")

	if got.code != cli.CodeUsage {
		t.Fatalf("exit %d, want %d\n%s", got.code, cli.CodeUsage, got.stderr)
	}
	for _, absent := range []string{"usage:", "a chain addresses an annotation", "exit codes:"} {
		if strings.Contains(got.stderr, absent) {
			t.Errorf("a usage error printed %q from a help screen:\n%s", absent, got.stderr)
		}
	}
}
