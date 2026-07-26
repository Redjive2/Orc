package cli_test

import (
	"strings"
	"testing"

	"orc/mailman/internal/cli"
)

// Three screens, three questions.
//
//	mailman help    everything — the verbs, the query language, the settings
//	mailman         the verbs, and the error that nothing was named
//	mailman <bad>   the refusal, and a guess
//
// They used to be one: every usage error printed the whole screen after it, so
// the answer to a typo was somewhere in sixty lines.

func TestBareMailmanShowsTheVerbs(t *testing.T) {
	r := newRig(t, "alice")
	got := r.run("alice")

	if got.code != cli.CodeUsage {
		t.Fatalf("bare mailman exited %d, want %d", got.code, cli.CodeUsage)
	}
	if !strings.HasPrefix(got.stderr, "mailman: no command given") {
		t.Errorf("the error should come first, so every diagnostic starts the same way:\n%s", got.stderr)
	}
	for _, verb := range []string{"inbox", "open", "convo", "archive", "check",
		"send", "reply", "cc", "read", "prune", "verify", "admin"} {
		if !strings.Contains(got.stderr, verb) {
			t.Errorf("the short screen does not list %q:\n%s", verb, got.stderr)
		}
	}
	// And not the full screen, whose query language is the bulk of it.
	for _, absent := range []string{"operators:", "fields:", "exit codes"} {
		if strings.Contains(got.stderr, absent) {
			t.Errorf("the short screen carried %q from the full one:\n%s", absent, got.stderr)
		}
	}
}

func TestUnknownMailmanCommandGuesses(t *testing.T) {
	r := newRig(t, "alice")

	got := r.run("alice", "inbx")
	if got.code != cli.CodeUsage {
		t.Fatalf("an unknown command exited %d, want %d", got.code, cli.CodeUsage)
	}
	if !strings.Contains(got.stderr, "mailman inbox") {
		t.Errorf("a near miss should be guessed:\n%s", got.stderr)
	}
	if lines := strings.Count(strings.TrimSpace(got.stderr), "\n"); lines != 0 {
		t.Errorf("the refusal is %d lines, want one:\n%s", lines+1, got.stderr)
	}

	got = r.run("alice", "frobnicate")
	if strings.Contains(got.stderr, "did you mean") {
		t.Errorf("it guessed at a word that resembles nothing:\n%s", got.stderr)
	}
	if !strings.Contains(got.stderr, "mailman help") {
		t.Errorf("with no guess it should at least point at help:\n%s", got.stderr)
	}
}

// TestOtherMailmanUsageErrorsAreJustTheError: a refusal that already says what
// was wrong does not need the verb list under it.
func TestOtherMailmanUsageErrorsAreJustTheError(t *testing.T) {
	r := newRig(t, "alice")
	got := r.run("alice", "inbox", "extra")

	if got.code != cli.CodeUsage {
		t.Fatalf("exit %d, want %d\n%s", got.code, cli.CodeUsage, got.stderr)
	}
	for _, absent := range []string{"operators:", "fields:", "reading"} {
		if strings.Contains(got.stderr, absent) {
			t.Errorf("a usage error printed %q from a help screen:\n%s", absent, got.stderr)
		}
	}
}

// TestTheShortMailmanScreenIsAColourLayer: stripped of escapes, the coloured
// rendering is the plain one byte for byte — the property every screen in this
// tree holds, and a new screen is where it gets forgotten.
func TestTheShortMailmanScreenIsAColourLayer(t *testing.T) {
	r := newRig(t, "alice")
	plain := r.run("alice")
	coloured := r.runStyled("alice", true, map[string]string{"COLORTERM": "truecolor"})

	if !strings.Contains(coloured.stderr, "\x1b[") {
		t.Fatalf("the short screen was not painted at all, so the comparison proves nothing")
	}
	if got := stripEscapes(coloured.stderr); got != plain.stderr {
		t.Errorf("the short screen differs once colour is stripped:\nplain:\n%s\nstripped:\n%s",
			plain.stderr, got)
	}
}
