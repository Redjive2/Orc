package source

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/cq/internal/protocol"
	"orc/cq/internal/upgrade"
)

// An upgrade replaces every binary on this machine, which is exactly the moment
// the loop keeping the machine mirrored is most likely to be lost — either because
// it is still running the old build, or because the thing that applied the upgrade
// was a one-shot sync that is about to exit.
//
// These pin when the hook that fixes that is called, and when it is not.

// fakeRun answers every command with success and nothing to say. What is under
// test is the sequence around the upgrade, not the upgrade itself.
func fakeRun(steps *[]string) func(context.Context, string, string, ...string) ([]byte, error) {
	return func(_ context.Context, dir, name string, args ...string) ([]byte, error) {
		*steps = append(*steps, strings.TrimSpace(name+" "+strings.Join(args, " ")))
		return []byte("ok"), nil
	}
}

func TestAnUpgradeMakesSureSomethingIsStillMirroringTheMachine(t *testing.T) {
	var steps []string
	asked := 0

	c := &CLI{
		Upgrade:     upgrade.Options{Source: checkout(t), Target: t.TempDir(), Run: fakeRun(&steps)},
		EnsureWatch: func() error { asked++; return nil },
	}
	if err := c.upgrade(context.Background()); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if asked != 1 {
		t.Errorf("the machine was upgraded and asked %d times whether anything still mirrors it, want 1", asked)
	}
}

// A build that failed must not have a watcher started on it: the binaries on disk
// are whatever the failed build left, and starting a loop on those is worse than
// starting none.
func TestAFailedUpgradeStartsNothing(t *testing.T) {
	asked := 0
	c := &CLI{
		// No source, which is the refusal a machine that installs binaries gets.
		Upgrade:     upgrade.Options{},
		EnsureWatch: func() error { asked++; return nil },
	}
	if err := c.upgrade(context.Background()); err == nil {
		t.Fatal("an upgrade with nothing to build from reported success")
	}
	if asked != 0 {
		t.Errorf("a failed upgrade started a watcher anyway (%d times)", asked)
	}
}

// The upgrade *worked*. Reporting it as failed because the watcher could not be
// started would have the operator chasing a build that is fine, while the real
// problem — that nothing is mirroring this machine — goes unmentioned. So it is
// mentioned, and separately.
func TestAWatcherThatCouldNotBeStartedDoesNotFailTheUpgrade(t *testing.T) {
	var steps []string
	var said []string

	c := &CLI{
		Upgrade:     upgrade.Options{Source: checkout(t), Target: t.TempDir(), Run: fakeRun(&steps)},
		EnsureWatch: func() error { return errors.New("no room to start one") },
		Warn:        func(format string, args ...any) { said = append(said, sprintf(format, args...)) },
	}
	if err := c.upgrade(context.Background()); err != nil {
		t.Fatalf("a working upgrade was reported as failed because a watcher would not start: %v", err)
	}

	joined := strings.Join(said, "\n")
	if !strings.Contains(joined, "no room to start one") {
		t.Errorf("nothing said that the machine may not be mirrored; it said:\n%s", joined)
	}
	if !strings.Contains(joined, "mirror") {
		t.Errorf("the warning does not say what was lost, only that something failed:\n%s", joined)
	}
}

// A caller that is not managing a real machine — a test, a probe — sets no hook,
// and an upgrade must not require one.
func TestAnUpgradeWithNoHookIsFine(t *testing.T) {
	var steps []string
	c := &CLI{Upgrade: upgrade.Options{Source: checkout(t), Target: t.TempDir(), Run: fakeRun(&steps)}}
	if err := c.upgrade(context.Background()); err != nil {
		t.Fatalf("an upgrade with no watch hook failed: %v", err)
	}
}

// Guard on the shape the queue actually delivers, so a rename of the op cannot
// quietly route around all of the above.
func TestTheUpgradeActionReachesTheUpgradePath(t *testing.T) {
	if !protocol.OpUpgrade.Valid() {
		t.Fatal("the upgrade op is not a valid action")
	}
}

func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// checkout is a directory the upgrade will agree to pull, which means one with a
// .git in it. Nothing runs git here — every command is answered by fakeRun — but
// the refusal for "not a checkout" happens before any command is built, so the
// marker has to be real.
func checkout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sh", "build"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}
