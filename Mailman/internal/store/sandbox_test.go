package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/sandbox"
)

// The guard is Mailman's half of Orcprobe's containment: environment
// redirection cannot stop an absolute path, and this can. The tests live here
// rather than only in orc/common/sandbox because the property that matters is
// "mailman refuses", not "the helper returns an error" — a future refactor that
// dropped the call would leave the helper's own tests passing.

func fixedClock() clock.Clock {
	return clock.NewFake(time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC), time.Second)
}

func TestOpenRefusesARealStoreFromInsideAProbe(t *testing.T) {
	real := t.TempDir() // no stamp: an ordinary store on a real machine
	t.Setenv(sandbox.EnvActive, "657651-abcdef")

	_, err := Open(real, fixedClock())
	if err == nil {
		t.Fatal("mailman opened the real store from inside a probe")
	}
	if !errors.Is(err, fault.ErrEscape) {
		t.Fatalf("the refusal is %T, want an escape", err)
	}
	if fault.Code(err) != fault.CodeEscape {
		t.Fatalf("the refusal exits %d, want %d", fault.Code(err), fault.CodeEscape)
	}

	// Nothing was created. Open makes the layout, so a guard that ran after it
	// would already have written to the real world.
	entries, err := os.ReadDir(real)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the refused store now holds %d entries; the guard ran too late", len(entries))
	}
}

func TestOpenAllowsTheProbesOwnStore(t *testing.T) {
	dir := t.TempDir()
	const probe = "657651-abcdef"
	if err := sandbox.Stamp(dir, probe); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sandbox.EnvActive, probe)

	s, err := Open(dir, fixedClock())
	if err != nil {
		t.Fatalf("mailman refused the probe's own store: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Root(), versionFile)); err != nil {
		t.Fatalf("the store was not initialised: %v", err)
	}
}

func TestOpenIsUnchangedOutsideAProbe(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(dir, fixedClock()); err != nil {
		t.Fatalf("an ordinary run was broken by the guard: %v", err)
	}
}
