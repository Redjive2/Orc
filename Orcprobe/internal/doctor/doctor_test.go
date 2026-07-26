package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/sandbox"
	"orc/orcprobe/internal/env"
	"orc/orcprobe/internal/shim"
)

const probeID = "657651-abcdef"

// probeDir builds a probe-shaped directory: stamped stores, an env file that
// redirects everything, shims, and a detached repo. Each test then breaks one
// thing and checks that doctor says so — the property under test is always
// "doctor is willing to report a failure", never "doctor passes".
func probeDir(t *testing.T) Spec {
	t.Helper()
	dir := t.TempDir()

	stateDirs := map[string]string{}
	for command, sub := range map[string]string{
		"mailman": "state/mailman",
		"muff":    "state/macmuffin",
		"cq":      "state/cq",
		"orc":     "state/orc",
	} {
		path := filepath.Join(dir, filepath.FromSlash(sub))
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := sandbox.Stamp(path, probeID); err != nil {
			t.Fatal(err)
		}
		stateDirs[command] = path
	}

	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte("[core]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.Stamp(repo, probeID); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.Stamp(dir, probeID); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, command := range shim.Commands() {
		if err := os.WriteFile(filepath.Join(bin, command), []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	envFile := filepath.Join(dir, "env")
	vars, err := env.Compose(env.Spec{
		ProbeID: probeID, ProbeName: "scratch", ProbeDir: dir,
		MailmanDir: stateDirs["mailman"], MacmuffinDir: stateDirs["muff"], CQDir: stateDirs["cq"],
		OrcDir: stateDirs["orc"],
		XDGDir: filepath.Join(dir, "state", "xdg"), BinDir: bin,
		ClaudeDir: filepath.Join(dir, "claude"), GitConfig: filepath.Join(repo, ".probe-gitconfig"),
		BasePath: "/usr/bin:/bin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Write(envFile, vars); err != nil {
		t.Fatal(err)
	}

	identities := filepath.Join(dir, "identities.json")
	if err := os.WriteFile(identities, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	return Spec{
		ProbeID: probeID, ProbeDir: dir, StateDirs: stateDirs, RepoDir: repo,
		BinDir: bin, EnvFile: envFile, Identities: identities,
		// An empty PATH: no tools are installed, so the behavioural checks are
		// skipped rather than guessed.
		Path: "",
	}
}

func find(t *testing.T, r Report, guard, what string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.Guard == guard && strings.Contains(c.What, what) {
			return c
		}
	}
	t.Fatalf("no check for %s/%s in %+v", guard, what, r.Checks)
	return Check{}
}

func TestAWellFormedProbePasses(t *testing.T) {
	r, err := Run(probeDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Sound() {
		for _, c := range r.Checks {
			if c.State == Absent {
				t.Errorf("%s/%s: %s", c.Guard, c.What, c.Detail)
			}
		}
		t.Fatal("a well-formed probe reported an absent guard")
	}
}

// TestAnUnstampedStoreIsReported is the check the tools' guard depends on: an
// unstamped store is one they would refuse, which makes the probe unusable
// rather than unsafe — but silence about it would be the worse failure.
func TestAnUnstampedStoreIsReported(t *testing.T) {
	s := probeDir(t)
	if err := os.Remove(filepath.Join(s.StateDirs["mailman"], sandbox.StampFile)); err != nil {
		t.Fatal(err)
	}

	r, err := Run(s)
	if err != nil {
		t.Fatal(err)
	}
	if got := find(t, r, "stamp", "mailman").State; got != Absent {
		t.Fatalf("an unstamped store reported %q", got)
	}
	if r.Sound() {
		t.Fatal("the report calls a probe with an unstamped store sound")
	}
}

func TestBrokenRedirectionIsReported(t *testing.T) {
	s := probeDir(t)
	// The one edit that matters: a store pointed back at the real world.
	data, err := os.ReadFile(s.EnvFile)
	if err != nil {
		t.Fatal(err)
	}
	broken := strings.Replace(string(data),
		"export MAILMAN_HOME='"+s.StateDirs["mailman"]+"'",
		"export MAILMAN_HOME='/Users/someone/.mailman'", 1)
	if err := os.WriteFile(s.EnvFile, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := Run(s)
	if err != nil {
		t.Fatal(err)
	}
	check := find(t, r, "redirection", "MAILMAN_HOME")
	if check.State != Absent {
		t.Fatalf("a store redirected outside the probe reported %q", check.State)
	}
	if !strings.Contains(check.Detail, "/Users/someone/.mailman") {
		t.Fatalf("the detail does not name where it points: %s", check.Detail)
	}
}

func TestASurvivingRemoteIsReported(t *testing.T) {
	s := probeDir(t)
	if err := os.WriteFile(filepath.Join(s.RepoDir, ".git", "config"),
		[]byte("[core]\n[remote \"origin\"]\n\turl = https://example.invalid/x.git\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := Run(s)
	if err != nil {
		t.Fatal(err)
	}
	if got := find(t, r, "detachment", "remotes").State; got != Absent {
		t.Fatalf("a repo copy with a remote reported %q", got)
	}
}

func TestWorldReadableKeysAreReported(t *testing.T) {
	s := probeDir(t)
	if err := os.Chmod(s.Identities, 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := Run(s)
	if err != nil {
		t.Fatal(err)
	}
	if got := find(t, r, "credentials", "identities").State; got != Absent {
		t.Fatalf("a world-readable key file reported %q", got)
	}
}

func TestMissingShimsAreReported(t *testing.T) {
	s := probeDir(t)
	for _, command := range shim.Commands() {
		if err := os.Remove(filepath.Join(s.BinDir, command)); err != nil {
			t.Fatal(err)
		}
	}

	r, err := Run(s)
	if err != nil {
		t.Fatal(err)
	}
	if got := find(t, r, "shims", "installed").State; got != Absent {
		t.Fatalf("a probe with no shims reported %q", got)
	}
}

// TestUncheckedIsNotReassurance is the distinction the whole package rests on.
// With no tools installed the stamp guard cannot be measured, and doctor must
// say "not checked" rather than passing the probe as sound.
func TestUncheckedIsNotReassurance(t *testing.T) {
	r, err := Run(probeDir(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"mailman", "muff", "cq"} {
		check := find(t, r, "stamp guard", command)
		if check.State != Skipped {
			t.Fatalf("%s's guard reported %q with no binary installed", command, check.State)
		}
	}
	if r.Measured() {
		t.Fatal("a report with unmeasured guards claims to have measured everything")
	}
}

// TestTheGuardIsMeasuredNotAssumed runs a stand-in tool and watches what it
// does — the behaviour that makes doctor able to tell a plan from a build.
func TestTheGuardIsMeasuredNotAssumed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		exit  string
		state State
	}{
		{"a build with the guard", "11", InForce},
		{"a build without it", "0", Absent},
		{"a build that fails some other way", "1", Absent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := probeDir(t)
			bin := t.TempDir()
			script := "#!/bin/sh\nexit " + tc.exit + "\n"
			for _, command := range []string{"mailman", "muff", "cq"} {
				if err := os.WriteFile(filepath.Join(bin, command), []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			s.Path = bin

			r, err := Run(s)
			if err != nil {
				t.Fatal(err)
			}
			if got := find(t, r, "stamp guard", "mailman").State; got != tc.state {
				t.Fatalf("a tool exiting %s reported %q, want %q", tc.exit, got, tc.state)
			}
		})
	}
}
