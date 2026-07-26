package store

import (
	"errors"
	"os"
	"testing"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/sandbox"
)

// Macmuffin's half of Orcprobe's containment. See the note in Mailman's
// equivalent: the property under test is "muff refuses", not "the helper
// returns an error".

func guardClock() clock.Clock {
	return clock.NewFake(time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC), time.Second)
}

func TestOpenRefusesARealStoreFromInsideAProbe(t *testing.T) {
	real := t.TempDir()
	t.Setenv(sandbox.EnvActive, "657651-abcdef")

	_, err := Open(real, guardClock())
	if err == nil {
		t.Fatal("muff opened the real store from inside a probe")
	}
	if !errors.Is(err, fault.ErrEscape) {
		t.Fatalf("the refusal is %T, want an escape", err)
	}

	entries, err := os.ReadDir(real)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the refused store now holds %d entries; the guard ran too late", len(entries))
	}
}

// TestReadIsGuardedToo covers the hook's path. It writes nothing, but answering
// "is this edit in scope?" from the real pool while inside a probe would let
// real state govern what happens in a sandbox.
func TestReadIsGuardedToo(t *testing.T) {
	real := t.TempDir()
	if _, err := Open(real, guardClock()); err != nil {
		t.Fatal(err)
	}

	t.Setenv(sandbox.EnvActive, "657651-abcdef")
	if _, err := Read(real, guardClock()); !errors.Is(err, fault.ErrEscape) {
		t.Fatalf("Read returned %v, want an escape refusal", err)
	}
}

func TestOpenAllowsTheProbesOwnStore(t *testing.T) {
	dir := t.TempDir()
	const probe = "657651-abcdef"
	if err := sandbox.Stamp(dir, probe); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sandbox.EnvActive, probe)

	if _, err := Open(dir, guardClock()); err != nil {
		t.Fatalf("muff refused the probe's own store: %v", err)
	}
}

func TestOpenIsUnchangedOutsideAProbe(t *testing.T) {
	if _, err := Open(t.TempDir(), guardClock()); err != nil {
		t.Fatalf("an ordinary run was broken by the guard: %v", err)
	}
}
