package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/fault"
)

const probeID = "657651-abcdef"

func stamped(t *testing.T, id string) string {
	t.Helper()
	dir := t.TempDir()
	if err := Stamp(dir, id); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestOutsideAProbeNothingHappens is the case that matters most: every ordinary
// run of every tool goes through this, and it must behave exactly as it did
// before the guard existed.
func TestOutsideAProbeNothingHappens(t *testing.T) {
	real := t.TempDir() // no stamp, like every real store
	env := MapEnv(map[string]string{})

	if err := Guard(env, real); err != nil {
		t.Fatalf("the guard refused a real store outside a probe: %v", err)
	}
	if _, inside := Active(env); inside {
		t.Fatal("Active reported a probe with no variable set")
	}
	// An empty variable is not a probe either: a shell that exported it blank
	// must not put every tool into a state where nothing can be opened.
	if err := Guard(MapEnv(map[string]string{EnvActive: "  "}), real); err != nil {
		t.Fatalf("an empty %s was treated as a probe: %v", EnvActive, err)
	}
}

// TestTheRealStoreIsRefusedFromInsideAProbe is the hole this package exists to
// close: environment redirection cannot stop an absolute path, and this can.
func TestTheRealStoreIsRefusedFromInsideAProbe(t *testing.T) {
	real := t.TempDir()
	env := MapEnv(map[string]string{EnvActive: probeID})

	err := Guard(env, real)
	if err == nil {
		t.Fatal("a real store was opened from inside a probe")
	}
	if !errors.Is(err, fault.ErrEscape) {
		t.Fatalf("the refusal is %T; it must be an escape so every tool exits %d", err, fault.CodeEscape)
	}
	if fault.Code(err) != fault.CodeEscape {
		t.Fatalf("the refusal exits %d, want %d", fault.Code(err), fault.CodeEscape)
	}
	// The message has to say what happened, or it reads as a bug in the tool.
	for _, want := range []string{real, probeID, "Nothing was written"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not mention %q:\n%v", want, err)
		}
	}
}

func TestTheProbesOwnStoreIsAllowed(t *testing.T) {
	dir := stamped(t, probeID)
	if err := Guard(MapEnv(map[string]string{EnvActive: probeID}), dir); err != nil {
		t.Fatalf("a correctly stamped store was refused: %v", err)
	}
}

// TestAnotherProbesStoreIsRefused stops two probes' state being mixed, which
// would be a quieter corruption than reaching the real world.
func TestAnotherProbesStoreIsRefused(t *testing.T) {
	dir := stamped(t, "some-other-probe")
	err := Guard(MapEnv(map[string]string{EnvActive: probeID}), dir)
	if err == nil {
		t.Fatal("a store belonging to another probe was opened")
	}
	if !strings.Contains(err.Error(), "some-other-probe") {
		t.Fatalf("the refusal does not name the other probe:\n%v", err)
	}
}

func TestAProbeRootStampIsAccepted(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ProbeStamp), []byte(probeID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Guard(MapEnv(map[string]string{EnvActive: probeID}), dir); err != nil {
		t.Fatalf("a probe root's own stamp was not accepted: %v", err)
	}
}

// TestTheGuardFailsClosed is the rule that makes it worth having. A stamp that
// cannot be read is refused, because a guard that assumes the best when it
// cannot check is a guard the operator only believes in.
func TestTheGuardFailsClosed(t *testing.T) {
	dir := t.TempDir()
	// A directory where the stamp should be is not a stamp.
	if err := os.MkdirAll(filepath.Join(dir, StampFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Guard(MapEnv(map[string]string{EnvActive: probeID}), dir); err == nil {
		t.Fatal("an unreadable stamp was treated as a good one")
	}

	if err := Guard(MapEnv(map[string]string{EnvActive: probeID}), ""); err == nil {
		t.Fatal("an empty root was allowed")
	}
	if err := Guard(MapEnv(map[string]string{EnvActive: probeID}), filepath.Join(dir, "nothing-here")); err == nil {
		t.Fatal("a root that does not exist was allowed")
	}
}

func TestStampRoundTrips(t *testing.T) {
	dir := stamped(t, probeID)
	data, err := os.ReadFile(filepath.Join(dir, StampFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != probeID {
		t.Fatalf("the stamp holds %q", string(data))
	}
	if err := Stamp(dir, "  "); err == nil {
		t.Fatal("a stamp with no probe id was written")
	}
}
