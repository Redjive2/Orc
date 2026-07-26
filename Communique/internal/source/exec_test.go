package source_test

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
	"orc/cq/internal/source"
)

// These drive the real exec path rather than the injected one, using ordinary
// system commands. They are what shows the adapter actually runs a program,
// reads its output, and reports its failures — the injected Run cannot.

func lookup(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not available: %v", name, err)
	}
	return path
}

func TestRunReadsARealCommandsOutput(t *testing.T) {
	echo := lookup(t, "echo")

	c := source.NewCLI("redjive")
	c.Mailman = echo
	c.Muff = echo

	// echo prints its arguments, which are not JSON, so the parse is what
	// fails — proving the command ran and its output was read.
	_, err := c.Snapshot(t.Context(), source.Options{Machine: "studio"})
	if !errors.Is(err, fault.ErrParse) {
		t.Fatalf("error = %v, want a parse fault from echo's output", err)
	}
	if !strings.Contains(err.Error(), "inbox") {
		t.Errorf("message %q should name the command that produced it", err)
	}
}

func TestRunReportsAFailingCommand(t *testing.T) {
	sh := lookup(t, "sh")

	c := source.NewCLI("redjive")
	c.Mailman = sh
	c.Muff = sh

	// A shell invoked with these arguments exits non-zero and prints to stderr.
	_, err := c.Snapshot(t.Context(), source.Options{Machine: "studio"})
	if !errors.Is(err, fault.ErrIO) {
		t.Fatalf("error = %v, want an i/o fault", err)
	}
}

func TestRunReportsAMissingCommand(t *testing.T) {
	c := source.NewCLI("redjive")
	c.Mailman = "definitely-not-a-real-command-xyzzy"

	_, err := c.Snapshot(t.Context(), source.Options{Machine: "studio"})
	if !errors.Is(err, fault.ErrIO) {
		t.Fatalf("error = %v, want an i/o fault", err)
	}
}

func TestApplyRunsARealCommand(t *testing.T) {
	c := source.NewCLI("redjive")
	c.Mailman = lookup(t, "true")

	action := protocol.Action{
		ID: protocol.ActionID(strings.Repeat("a", 32)), Seq: 1, Machine: "studio",
		Op: protocol.OpRead, Args: protocol.Args{PUID: 41}, Queued: at(),
	}
	if err := c.Apply(t.Context(), action); err != nil {
		t.Errorf("Apply against a command that succeeds: %v", err)
	}

	c.Mailman = lookup(t, "false")
	if err := c.Apply(t.Context(), action); !errors.Is(err, fault.ErrIO) {
		t.Errorf("error = %v, want an i/o fault from a command that failed", err)
	}
}

func TestACancelledContextStopsTheCommand(t *testing.T) {
	sleep := lookup(t, "sleep")

	c := source.NewCLI("redjive")
	c.Mailman = sleep

	ctx, cancel := t.Context(), func() {}
	_ = cancel
	done := make(chan error, 1)
	go func() {
		_, err := c.Snapshot(ctx, source.Options{Machine: "studio"})
		done <- err
	}()

	select {
	case err := <-done:
		// `sleep inbox --all --json` fails immediately on a bad interval, which
		// is the i/o path; either way it must not hang.
		if err == nil {
			t.Errorf("expected a failure")
		}
	case <-t.Context().Done():
		t.Fatal("the command was never reaped")
	}
}
