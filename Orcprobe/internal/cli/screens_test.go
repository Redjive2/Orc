package cli

import (
	"strings"
	"testing"
)

// Three screens, three questions.
//
//	orcprobe help    everything — every form, the query language, the flags
//	orcprobe         the verbs, and the error that nothing was named
//	orcprobe <bad>   the refusal, and a guess
//
// They used to be one: every usage error printed the whole screen after it, so
// the answer to a typo was somewhere in sixty lines.

func TestBareOrcprobeShowsTheVerbs(t *testing.T) {
	h := newHarness(t)
	code, _, errs := h.run()

	if code != CodeUsage {
		t.Fatalf("bare orcprobe exited %d, want %d", code, CodeUsage)
	}
	if !strings.HasPrefix(errs, "orcprobe: no command given") {
		t.Errorf("the error should come first, so every diagnostic starts the same way:\n%s", errs)
	}
	for _, verb := range []string{"create", "list", "use", "shell", "as", "manifest",
		"destroy", "world", "mail", "tasks", "journal", "timeline",
		"save", "restore", "diff", "doctor"} {
		if !strings.Contains(errs, verb) {
			t.Errorf("the short screen does not list %q:\n%s", verb, errs)
		}
	}
	for _, absent := range []string{"operators:", "exit codes", "identity inside a probe is free"} {
		if strings.Contains(errs, absent) {
			t.Errorf("the short screen carried %q from the full one:\n%s", absent, errs)
		}
	}
}

func TestUnknownOrcprobeCommandGuesses(t *testing.T) {
	h := newHarness(t)

	code, _, errs := h.run("destry")
	if code != CodeUsage {
		t.Fatalf("an unknown command exited %d, want %d", code, CodeUsage)
	}
	if !strings.Contains(errs, "orcprobe destroy") {
		t.Errorf("a near miss should be guessed:\n%s", errs)
	}
	if lines := strings.Count(strings.TrimSpace(errs), "\n"); lines != 0 {
		t.Errorf("the refusal is %d lines, want one:\n%s", lines+1, errs)
	}

	// `destory` is equally close to `destroy` and `restore`, so nothing is offered
	// — which is the behaviour worth having here of all places: one of those two
	// discards a probe and the other rewinds it.
	if _, _, tied := h.run("destory"); strings.Contains(tied, "did you mean") {
		t.Errorf("it guessed between destroy and restore:\n%s", tied)
	}

	_, _, errs = h.run("frobnicate")
	if strings.Contains(errs, "did you mean") {
		t.Errorf("it guessed at a word that resembles nothing:\n%s", errs)
	}
	if !strings.Contains(errs, "orcprobe help") {
		t.Errorf("with no guess it should at least point at help:\n%s", errs)
	}
}

// TestOtherOrcprobeUsageErrorsAreJustTheError: a refusal that already says what
// was wrong does not need the query language under it.
func TestOtherOrcprobeUsageErrorsAreJustTheError(t *testing.T) {
	h := newHarness(t)
	code, _, errs := h.run("create")

	if code != CodeUsage {
		t.Fatalf("exit %d, want %d\n%s", code, CodeUsage, errs)
	}
	for _, absent := range []string{"operators:", "exit codes", "identity inside a probe is free"} {
		if strings.Contains(errs, absent) {
			t.Errorf("a usage error printed %q from a help screen:\n%s", absent, errs)
		}
	}
}
