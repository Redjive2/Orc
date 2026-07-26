package cli_test

import (
	"strings"
	"testing"

	"orc/common/fault"
)

// Three screens, three questions.
//
//	dock help    everything — every form, and the target syntax
//	dock         the verbs, and the error that nothing was named
//	dock <bad>   the refusal, and a guess
//
// They used to be one: every usage error printed the whole screen after it, so
// the answer to a mistyped target was somewhere in forty lines.

func TestBareDockShowsTheVerbs(t *testing.T) {
	_, errs, code := run(t)

	if code != fault.CodeUsage {
		t.Fatalf("bare dock exited %d, want %d", code, fault.CodeUsage)
	}
	if !strings.HasPrefix(errs, "dock: no command given") {
		t.Errorf("the error should come first, so every diagnostic starts the same way:\n%s", errs)
	}
	for _, verb := range []string{"index", "read", "overview", "find", "links", "check", "write"} {
		if !strings.Contains(errs, verb) {
			t.Errorf("the short screen does not list %q:\n%s", verb, errs)
		}
	}
	for _, absent := range []string{"a target is a path and an address", "exit codes"} {
		if strings.Contains(errs, absent) {
			t.Errorf("the short screen carried %q from the full one:\n%s", absent, errs)
		}
	}
}

func TestUnknownDockCommandGuesses(t *testing.T) {
	_, errs, code := run(t, "lnks")
	if code != fault.CodeUsage {
		t.Fatalf("an unknown command exited %d, want %d", code, fault.CodeUsage)
	}
	if !strings.Contains(errs, "dock links") {
		t.Errorf("a near miss should be guessed:\n%s", errs)
	}
	if lines := strings.Count(strings.TrimSpace(errs), "\n"); lines != 0 {
		t.Errorf("the refusal is %d lines, want one:\n%s", lines+1, errs)
	}

	_, errs, _ = run(t, "frobnicate")
	if strings.Contains(errs, "did you mean") {
		t.Errorf("it guessed at a word that resembles nothing:\n%s", errs)
	}
	if !strings.Contains(errs, "dock help") {
		t.Errorf("with no guess it should at least point at help:\n%s", errs)
	}
}

// TestOtherDockUsageErrorsAreJustTheError: a refusal that already says what was
// wrong does not need the target syntax under it.
func TestOtherDockUsageErrorsAreJustTheError(t *testing.T) {
	_, errs, code := run(t, "read")

	if code != fault.CodeUsage {
		t.Fatalf("exit %d, want %d\n%s", code, fault.CodeUsage, errs)
	}
	// Not "usage:" — dock opens a per-command refusal with that word, which is
	// exactly what it should do. These are lines only the full screen has.
	for _, absent := range []string{"a target is a path and an address", "exit codes", "dock index"} {
		if strings.Contains(errs, absent) {
			t.Errorf("a usage error printed %q from a help screen:\n%s", absent, errs)
		}
	}
}
