package cli_test

import (
	"strings"
	"testing"

	"orc/cq/internal/fault"
)

// A bad interval refuses, immediately and as a usage error.
//
// Two things worth pinning. The intervals are handed straight to the cycles' own
// flags, which would refuse them — but by then the refusal is a child exiting
// instantly and being restarted forever on a widening backoff, so a typo would
// present as a cycle that never comes up rather than as a typo.
//
// And the *code* matters as much as the message. `pace` first reached for
// `orc/common/fault`, which cq's own `fault.Classify` has never heard of, so
// every refusal here fell through to 70 — internal — instead of 1. cq documents
// these codes as stable and scripts branch on them, so a usage error reporting a
// crash is a lie told to a program rather than to a person.
func TestPaceRefusesABadInterval(t *testing.T) {
	h := newHarness(t)
	for _, given := range []struct{ flag, value string }{
		{"--sync", "banana"},
		{"--wake", "0s"},
		{"--tend", "-5m"},
	} {
		got := h.run(t, "", "pace", given.flag, given.value)
		if got.code != fault.ExitUsage {
			t.Errorf("%s %s exited %d, want %d (usage)",
				given.flag, given.value, got.code, fault.ExitUsage)
		}
		if !strings.Contains(got.stderr, given.flag) {
			t.Errorf("the refusal for %s does not name the flag:\n%s", given.flag, got.stderr)
		}
	}
}

// An interval that is fine is not refused for being fine.
//
// It cannot be run to completion here — it would start three long-running cycles
// — so what this checks is that validation lets a good value through, by way of
// the one thing that fails first afterwards on a machine with nothing configured.
func TestPaceAcceptsAGoodInterval(t *testing.T) {
	h := newHarness(t)
	got := h.run(t, "", "pace", "--sync", "banana")
	if !strings.Contains(got.stderr, "not \"banana\"") {
		t.Fatalf("expected the duration refusal, got:\n%s", got.stderr)
	}
	// The same command with a real duration gets past the check — it fails later
	// and differently, which is the point.
	got = h.run(t, "", "pace", "--sync", "30s", "--wake", "banana")
	if strings.Contains(got.stderr, "not \"30s\"") {
		t.Errorf("a valid --sync was refused:\n%s", got.stderr)
	}
	if !strings.Contains(got.stderr, "not \"banana\"") {
		t.Errorf("the refusal moved on to --wake, but did not say so:\n%s", got.stderr)
	}
}
