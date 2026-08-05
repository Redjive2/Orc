package cli

import (
	"errors"
	"strings"
	"testing"
)

// A backstop must not stop.
//
// Both cycles used to return an error after five consecutive failures. Nothing
// restarts them — there is no service and no supervisor above them — so five bad
// minutes on a one-minute cycle ended the only thing keeping a fleet alive, and the
// agents then went quiet with nothing left running to notice.

// counted runs a backstop over a scripted set of outcomes and returns what it said.
func counted(t *testing.T, outcomes []error) (*backstop, string) {
	t.Helper()
	var said strings.Builder
	app := App{Stdout: &said, Stderr: &said}
	guard := &backstop{app: app, what: "tending"}
	for _, err := range outcomes {
		guard.pass(func() error { return err })
	}
	return guard, said.String()
}

func TestABackstopKeepsRunningHoweverOftenAPassFails(t *testing.T) {
	failing := make([]error, 50)
	for i := range failing {
		failing[i] = errors.New("the store could not be read")
	}
	guard, said := counted(t, failing)

	if guard.failures != 50 {
		t.Errorf("it counted %d failures, want 50", guard.failures)
	}
	// The point of the test is that there is no way for the loop to end: `pass`
	// returns nothing, so a caller cannot stop on it even by mistake.
	if said == "" {
		t.Error("fifty failed passes went by in silence")
	}
}

// Said often enough to leave a trail, seldom enough not to scroll a terminal.
func TestAFailingBackstopIsSaidWithoutFlooding(t *testing.T) {
	failing := make([]error, 30)
	for i := range failing {
		failing[i] = errors.New("nope")
	}
	_, said := counted(t, failing)

	// Three in full, then one in ten: 1, 2, 3, 10, 20, 30.
	if n := strings.Count(said, "in a row failed"); n != 6 {
		t.Errorf("thirty failures were reported %d times, want 6:\n%s", n, said)
	}
}

// "It is working again" is the line somebody watching a broken fleet is waiting
// for, so it is never one of the ones held back.
func TestRecoveryIsAlwaysSaid(t *testing.T) {
	_, said := counted(t, []error{errors.New("a"), errors.New("b"), nil})
	if !strings.Contains(said, "working again") {
		t.Errorf("a cycle that recovered did not say so:\n%s", said)
	}
	if !strings.Contains(said, "2 passes") {
		t.Errorf("it did not say how bad it had been:\n%s", said)
	}
}

// And a clean run says nothing at all. A cycle that reported every healthy pass is
// one an operator stops reading, which costs them the pass that matters.
func TestAWorkingBackstopIsQuiet(t *testing.T) {
	if _, said := counted(t, []error{nil, nil, nil}); said != "" {
		t.Errorf("a working cycle was not quiet:\n%s", said)
	}
}

// A panic below a backstop is a bug, and a bug must not take the fleet with it.
// Every agent would otherwise stop on its own schedule with nothing running to
// notice, which is the failure this whole file exists to prevent.
func TestAPanicInAPassDoesNotEndTheCycle(t *testing.T) {
	var said strings.Builder
	app := App{Stdout: &said, Stderr: &said}
	guard := &backstop{app: app, what: "waking"}

	guard.pass(func() error { panic("a nil map somewhere below") })
	if guard.failures != 1 {
		t.Fatalf("a panic was not counted as a failed pass: %d", guard.failures)
	}
	if !strings.Contains(said.String(), "panicked") {
		t.Errorf("the panic was swallowed rather than reported:\n%s", said.String())
	}

	// And the cycle carries on: the next pass runs and recovers.
	ran := false
	guard.pass(func() error { ran = true; return nil })
	if !ran {
		t.Error("the pass after a panic never ran")
	}
	if !strings.Contains(said.String(), "working again") {
		t.Errorf("it did not report recovering from the panic:\n%s", said.String())
	}
}
