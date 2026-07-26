package agent_test

import (
	"errors"
	"log/slog"
	"testing"

	commonfault "orc/common/fault"
	"orc/common/sandbox"
	"orc/cq/internal/agent"
	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
)

// cq's half of Orcprobe's containment.
//
// The probe's shims already refuse `cq sync` outright, so this is the layer
// underneath: a cq invoked by its full path bypasses the shims entirely, and
// without this it could open the real agent state and sync a sandbox's mail to
// the real server.

func TestNewRefusesRealAgentStateFromInsideAProbe(t *testing.T) {
	real := t.TempDir() // no stamp: ordinary agent state on a real machine
	t.Setenv(sandbox.EnvActive, "657651-abcdef")

	_, err := agent.New(agent.Options{
		Source:  &fakeSource{},
		Server:  "https://example.invalid",
		Token:   "token",
		Machine: protocol.MachineID("laptop"),
		State:   real,
		Logger:  slog.Default(),
	})
	if err == nil {
		t.Fatal("cq opened the real agent state from inside a probe")
	}
	if !errors.Is(err, commonfault.ErrEscape) {
		t.Fatalf("the refusal is %T, want an escape", err)
	}
	// cq's own vocabulary has to classify it, or a containment failure exits 70
	// and reads as a bug in cq rather than as the guard doing its job.
	if got := fault.Exit(err); got != fault.ExitEscape {
		t.Fatalf("the refusal exits %d, want %d", got, fault.ExitEscape)
	}
	if got := fault.Classify(err); got != fault.CodeEscape {
		t.Fatalf("the refusal classifies as %q, want %q", got, fault.CodeEscape)
	}
}

func TestNewAllowsTheProbesOwnState(t *testing.T) {
	dir := t.TempDir()
	const probe = "657651-abcdef"
	if err := sandbox.Stamp(dir, probe); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sandbox.EnvActive, probe)

	if _, err := agent.New(agent.Options{
		Source:  &fakeSource{},
		Server:  "https://example.invalid",
		Token:   "token",
		Machine: protocol.MachineID("laptop"),
		State:   dir,
		Logger:  slog.Default(),
	}); err != nil {
		t.Fatalf("cq refused the probe's own state: %v", err)
	}
}

func TestNewIsUnchangedOutsideAProbe(t *testing.T) {
	if _, err := agent.New(agent.Options{
		Source:  &fakeSource{},
		Server:  "https://example.invalid",
		Token:   "token",
		Machine: protocol.MachineID("laptop"),
		State:   t.TempDir(),
		Logger:  slog.Default(),
	}); err != nil {
		t.Fatalf("an ordinary run was broken by the guard: %v", err)
	}
}
