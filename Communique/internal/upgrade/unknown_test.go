package upgrade_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orc/cq/internal/upgrade"
)

// The panel said a rebuild was impossible, and the button rebuilt the fleet.
//
// Every probe shared one budget and `go version` ran last, so a slow git spent the
// whole of it and the toolchain probe was cancelled before it started. That was
// reported as "no working go toolchain on this machine's PATH" — a hard stop,
// blaming something installed and working — while the button, which has ten
// minutes, worked perfectly.
//
// A probe that did not run says nothing about the machine.

// tree is a checkout shaped enough to get past the stat checks.
func tree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, at := range []string{".git", "sh"} {
		if err := os.MkdirAll(filepath.Join(dir, at), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "sh", "build"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// slowGit answers git slowly and go instantly, which is the ordinary shape: git
// walks a large tree and `go version` prints a string.
func slowGit(each time.Duration) func(context.Context, string, string, ...string) ([]byte, error) {
	return func(ctx context.Context, _, name string, _ ...string) ([]byte, error) {
		if name != "git" {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return []byte("go version go1.26.1"), nil
		}
		select {
		case <-time.After(each):
			return []byte("main"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func TestASlowCheckoutDoesNotReadAsABrokenOne(t *testing.T) {
	got := upgrade.Options{Source: tree(t), Run: slowGit(300 * time.Millisecond)}.
		Check(deadline(t, 900*time.Millisecond))

	if got.Verdict == upgrade.Stop {
		t.Errorf("a check that ran out of time said the build was impossible: %+v", got.Reasons)
	}
	if !got.Unknown {
		t.Error("it did not record that it had been cut short")
	}
	// And it says which it is. "No toolchain" is a fact about the machine and
	// "could not ask" is a fact about the check, and they must not print alike.
	var said string
	for _, r := range got.Reasons {
		said += r.Text + "\n"
	}
	if strings.Contains(said, "no working go toolchain") {
		t.Errorf("it blamed the toolchain for its own timeout:\n%s", said)
	}
	if !strings.Contains(said, "in time") {
		t.Errorf("it did not say it had run out of time:\n%s", said)
	}
}

// The other half: when the probes do answer, a real problem is still a hard stop.
// A rule that turned every stop into a caution would be the same defect facing the
// other way.
func TestAnAnsweredCheckStillRefuses(t *testing.T) {
	dir := tree(t)
	got := upgrade.Options{Source: dir, Run: func(_ context.Context, _, name string, args ...string) ([]byte, error) {
		if name == "git" && len(args) > 1 && args[1] == "--abbrev-ref" {
			return []byte("HEAD"), nil // detached, answered promptly
		}
		return []byte(""), nil
	}}.Check(deadline(t, 5*time.Second))

	if got.Unknown {
		t.Fatal("a check that answered every probe reported itself cut short")
	}
	if got.Verdict != upgrade.Stop {
		t.Errorf("a detached head is a stop, and it said %q: %+v", got.Verdict, got.Reasons)
	}
}

// A missing toolchain, answered, is still a stop.
func TestAToolchainThatIsReallyMissingIsAStop(t *testing.T) {
	dir := tree(t)
	got := upgrade.Options{Source: dir, Run: func(_ context.Context, _, name string, _ ...string) ([]byte, error) {
		if name == "go" {
			return nil, os.ErrNotExist
		}
		return []byte("main"), nil
	}}.Check(deadline(t, 5*time.Second))

	if got.Verdict != upgrade.Stop {
		t.Errorf("a missing toolchain said %q: %+v", got.Verdict, got.Reasons)
	}
}

func deadline(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

// This is a GET a page polls, so how long it may take is part of what it is.
//
// Giving each command its own budget is what stops one slow git starving the
// toolchain probe — and the first version of that gave the whole inspection four
// commands' worth of time, so a hung git held the request forty seconds while the
// browser gave up, the subprocess kept running, and the next poll started another.
// A wrong answer had been traded for a hung page.
func TestTheCheckStaysWithinItsBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("this one waits out the budget on purpose")
	}
	dir := tree(t)
	start := time.Now()
	got := upgrade.Options{Source: dir, Run: func(ctx context.Context, _, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done() // every command hangs until it is cut off
		return nil, ctx.Err()
	}}.Check(context.Background())

	took := time.Since(start)
	// Twice the budget, not the budget plus a moment. The regression this catches
	// was four times it, and a bound tight enough to flake on a loaded machine is
	// one somebody reruns until it passes.
	if took > 2*upgrade.CheckBudget {
		t.Errorf("a check with every command hung took %s; the budget is %s",
			took.Round(time.Second), upgrade.CheckBudget)
	}
	// And it says it could not tell, rather than that the machine is broken.
	if got.Verdict == upgrade.Stop {
		t.Errorf("a check that answered nothing refused the build: %+v", got.Reasons)
	}
}
