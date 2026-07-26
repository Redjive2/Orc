package source_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orc/cq/internal/protocol"
	"orc/cq/internal/settings"
	"orc/cq/internal/source"
)

// Moving the library is the one action that changes where the other actions may
// write, so what it refuses is most of what it is.

// rig is a machine: a home for cq's own state, a fleet, and somewhere to mirror.
type rig struct {
	cli   *source.CLI
	home  string
	orc   string
	repo  string
	other string
}

func machine(t *testing.T) rig {
	t.Helper()
	base := t.TempDir()

	r := rig{
		home:  filepath.Join(base, "cq"),
		orc:   filepath.Join(base, "orc"),
		repo:  filepath.Join(base, "checkouts", "Orc"),
		other: filepath.Join(base, "checkouts", "Other"),
	}
	for _, dir := range []string{r.home, r.orc, r.repo, r.other} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	c := source.NewCLI("redjive")
	c.LibraryRoot = r.repo
	c.Home = r.home
	c.Look = func(key string) (string, bool) {
		if key == "ORC_HOME" {
			return r.orc, true
		}
		return "redjive", true
	}
	r.cli = c
	return r
}

// move is one queued action. It carries a real id and machine because Apply
// validates before it does anything, and a fixture that failed that check would
// pass every "was it refused" assertion below for the wrong reason.
func move(where string) protocol.Action {
	return protocol.Action{
		ID: protocol.ActionID(strings.Repeat("a", 32)), Seq: 1, Machine: "studio",
		Op: protocol.OpLibraryRoot, Args: protocol.Args{Workspace: where}, Queued: when,
	}
}

var when = time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

// The ordinary case: a directory that is there, and the machine records it.
func TestMovingTheLibrary(t *testing.T) {
	r := machine(t)

	if err := r.cli.Apply(t.Context(), move(r.other)); err != nil {
		t.Fatalf("moving to a real directory was refused: %v", err)
	}

	chosen, err := settings.Read(r.home)
	if err != nil {
		t.Fatal(err)
	}
	// Resolved, not as it was typed: every later containment check compares
	// against a resolved path, and a root with a symlink in it would fail all of
	// them while looking identical on screen.
	want, err := filepath.EvalSymlinks(r.other)
	if err != nil {
		t.Fatal(err)
	}
	if chosen.Library != want {
		t.Errorf("recorded %q, want %q", chosen.Library, want)
	}
}

// Applying it twice lands in the same place, which is what Idempotent() claims
// and what lets a machine retry a round whose outcome it never learned.
func TestMovingTheLibraryTwiceIsTheSameMove(t *testing.T) {
	r := machine(t)
	for i := range 2 {
		if err := r.cli.Apply(t.Context(), move(r.other)); err != nil {
			t.Fatalf("application %d was refused: %v", i+1, err)
		}
	}
	chosen, _ := settings.Read(r.home)
	want, _ := filepath.EvalSymlinks(r.other)
	if chosen.Library != want {
		t.Errorf("recorded %q, want %q", chosen.Library, want)
	}
}

// What it refuses, and why each one matters.
func TestTheLibraryRootIsChecked(t *testing.T) {
	r := machine(t)
	base := filepath.Dir(r.home)

	file := filepath.Join(base, "a-file")
	if err := os.WriteFile(file, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ name, path, want string }{
		{"nothing at all", "", "requires workspace"},
		{"a relative path", "checkouts/Orc", "absolute"},
		{"somewhere that is not there", filepath.Join(base, "nope"), "not there on this machine"},
		{"a file", file, "is a file"},
		// The two that matter. Everything under the root is mirrored to the site
		// and writable from it, so a root that swallows the fleet hands over every
		// agent's key, and one that swallows cq's home hands over its journal.
		{"the fleet itself", r.orc, "is the fleet itself"},
		{"a parent of both", base, "contains the fleet"},
		{"cq's own home", r.home, "is cq's own state itself"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := r.cli.Apply(t.Context(), move(c.path))
			if err == nil {
				t.Fatalf("%q was accepted", c.path)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("the refusal should say %q:\n%v", c.want, err)
			}
			// And nothing was recorded, so a refused move does not half-happen.
			if chosen, _ := settings.Read(r.home); chosen.Library != "" {
				t.Errorf("a refused move still recorded %q", chosen.Library)
			}
		})
	}
}

// A neighbouring directory whose name starts the same is not inside it.
//
// `/srv/orc` and `/srv/orc-old` are exactly the pair somebody has during a
// migration, and a prefix comparison calls the second one a child of the first.
func TestANeighbourIsNotInsideTheFleet(t *testing.T) {
	base := t.TempDir()
	beside := filepath.Join(base, "orc-checkout")
	if err := os.MkdirAll(beside, 0o755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "cq")
	orc := filepath.Join(base, "orc")
	for _, dir := range []string{home, orc} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	c := source.NewCLI("redjive")
	c.Home = home
	c.Look = func(key string) (string, bool) {
		if key == "ORC_HOME" {
			return orc, true
		}
		return "redjive", true
	}
	if err := c.Apply(t.Context(), move(beside)); err != nil {
		t.Errorf("%s is beside the fleet, not inside it, and was refused: %v", beside, err)
	}
}

// Without a home there is nowhere to record the choice, and a move that appeared
// to work and then reverted on the next round would be worse than a refusal.
func TestMovingTheLibraryNeedsAHome(t *testing.T) {
	c := source.NewCLI("redjive")
	c.Look = func(string) (string, bool) { return "redjive", true }

	err := c.Apply(t.Context(), move(t.TempDir()))
	if err == nil {
		t.Fatal("a move was accepted with nowhere to record it")
	}
	if !strings.Contains(err.Error(), "CQ_HOME") {
		t.Errorf("the refusal should name the way forward:\n%v", err)
	}
}
