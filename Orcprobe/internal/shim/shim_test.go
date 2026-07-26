package shim

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"orc/orcprobe/internal/fault"
)

func TestCheckRefusals(t *testing.T) {
	cases := []struct {
		name    string
		command string
		args    []string
		refused bool
	}{
		{"cq sync is the way out", "cq", []string{"sync"}, true},
		{"cq sync with flags", "cq", []string{"--dry-run", "sync"}, true},
		{"cq serve on loopback is fine", "cq", []string{"serve", "--addr", "127.0.0.1:8080"}, false},
		{"cq serve on localhost is fine", "cq", []string{"serve", "--addr=localhost:8080"}, false},
		{"cq serve on the world is not", "cq", []string{"serve", "--addr", "0.0.0.0:8080"}, true},
		{"cq serve on a real interface is not", "cq", []string{"serve", "--addr", "192.168.1.4:8080"}, true},
		{"cq serve with no addr is fine", "cq", []string{"serve"}, false},
		{"cq status is fine", "cq", []string{"status"}, false},
		{"git push", "git", []string{"push"}, true},
		{"git fetch", "git", []string{"fetch", "origin"}, true},
		{"git pull", "git", []string{"pull"}, true},
		{"git clone over the network", "git", []string{"clone", "https://example.com/x.git"}, true},
		{"git clone over ssh", "git", []string{"clone", "git@example.com:x.git"}, true},
		{"git clone locally is fine", "git", []string{"clone", "../other"}, false},
		{"git commit is fine", "git", []string{"commit", "-m", "x"}, false},
		{"git status is fine", "git", []string{"status"}, false},
		// Orc's verbs split: reading a copied fleet is the point of a probe,
		// and anything that populates or wakes an identity is rule 1.
		{"orc status reads", "orc", []string{"status"}, false},
		{"orc introspect reads", "orc", []string{"introspect", "--only", "identity"}, false},
		{"orc check-control reads", "orc", []string{"check-control", "ember"}, false},
		{"orc verify reads", "orc", []string{"verify"}, false},
		{"orc doctor reads", "orc", []string{"doctor"}, false},
		{"orc help reads", "orc", []string{"help"}, false},
		{"bare orc prints its help", "orc", nil, false},

		{"orc employ populates", "orc", []string{"employ", "ember"}, true},
		{"orc attach", "orc", []string{"attach", "ember"}, true},
		{"orc poke", "orc", []string{"poke", "ember"}, true},
		{"orc refresh", "orc", []string{"refresh", "ember"}, true},
		{"orc tend", "orc", []string{"tend"}, true},
		{"orc bootstrap", "orc", []string{"bootstrap"}, true},
		{"orc new", "orc", []string{"new", "ember"}, true},
		{"orc grant", "orc", []string{"grant", "ember", "write"}, true},
		{"orc fire", "orc", []string{"fire", "ember"}, true},
		// A verb this build has never heard of is refused, not waved through:
		// the allow-list runs that way round on purpose.
		{"a verb orc grows next week", "orc", []string{"conscript"}, true},
		{"mailman is the point", "mailman", []string{"inbox"}, false},
		{"muff is the point", "muff", []string{"pool"}, false},
		{"a full path is still checked", "/usr/local/bin/cq", []string{"sync"}, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Check(c.command, c.args)
			if c.refused && err == nil {
				t.Fatalf("Check(%q, %v) allowed it; it must be refused", c.command, c.args)
			}
			if !c.refused && err != nil {
				t.Fatalf("Check(%q, %v) refused: %v", c.command, c.args, err)
			}
			if c.refused && !errors.Is(err, fault.ErrEscape) {
				t.Fatalf("refusal is %T, want a fault.Escape so the exit code says escape", err)
			}
		})
	}
}

// TestCheckAmbiguousAddressIsRefused pins the direction the doubt falls in: an
// address orcprobe cannot classify is not quietly allowed.
func TestCheckAmbiguousAddressIsRefused(t *testing.T) {
	if err := Check("cq", []string{"serve", "--addr", "not-an-address"}); err == nil {
		t.Fatal("an unparseable --addr was allowed; it must be refused")
	}
}

func TestRealSkipsTheProbesOwnBin(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	real := filepath.Join(dir, "real")
	for _, d := range []string{bin, real} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "mailman"), []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	path := bin + string(filepath.ListSeparator) + real
	got, err := Real("mailman", path, bin)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(real, "mailman"); got != want {
		t.Fatalf("Real resolved %q, want %q — a shim that finds itself would loop", got, want)
	}
}

func TestRealSaysWhenThereIsNoRealBinary(t *testing.T) {
	dir := t.TempDir()
	if _, err := Real("mailman", dir, dir); err == nil {
		t.Fatal("Real found a binary that is not there")
	}
}

func TestInstallLinksEveryCommand(t *testing.T) {
	dir := t.TempDir()
	shimPath := filepath.Join(dir, "orcprobe-shim")
	if err := os.WriteFile(shimPath, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "probe", "bin")

	if _, err := Install(bin, shimPath); err != nil {
		t.Fatal(err)
	}
	for _, command := range Commands() {
		if _, err := os.Stat(filepath.Join(bin, command)); err != nil {
			t.Fatalf("%s was not installed: %v", command, err)
		}
	}

	// Installing twice must work: creating a probe from another probe, or
	// repairing one, should not fail on a name that is already there.
	if _, err := Install(bin, shimPath); err != nil {
		t.Fatalf("second install failed: %v", err)
	}
}
