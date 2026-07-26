package cli_test

import (
	"strings"
	"testing"

	"orc/common/user"
	"orc/macmuffin/internal/control"
)

// The fleet's operator over a task nobody owns.
//
// A pooled task has no owner, so `scope`, `complete`, `invite` and `delete` all
// refuse with "claim it first" — right for an agent, wrong for the person running
// the fleet, who has to retire a stale task or fix a bad scope without first
// putting their name on work they are not doing.
//
// What these pin is the *edge* of that standing, because a power over other
// people's work is only as good as the line around it: an owner's task is still
// the owner's, a draft is still private, and an unreachable fleet does not
// silently promote anybody.

// asOperator answers as Orc would on a fleet whose operator is `name`.
func asOperator(name string) control.Operating {
	return func(claimed user.Name) (bool, error) {
		return claimed.String() == name, nil
	}
}

// noFleet answers as Orc would when it is not installed.
func noFleet() control.Operating {
	return func(user.Name) (bool, error) {
		return false, control.Unasked{Reason: "orc is not installed, so no fleet has an operator"}
	}
}

func TestTheOperatorActsOnAnUnownedTask(t *testing.T) {
	r := newRig(t)
	r.operator = asOperator("boss")
	r.ok("ember", "create", "parser", "4", "3")
	r.pool("ember", "parser")

	// Every owner-only action on a task nobody holds.
	r.ok("boss", "scope", "parser", "internal/parse/")
	r.ok("boss", "invite", "hand", "parser")
	r.ok("boss", "status", "parser", "2")
	r.ok("boss", "create", "parser", "--sub", "read-the-grammar")
}

func TestTheOperatorSaysWhenItIsStandingIn(t *testing.T) {
	r := newRig(t)
	r.operator = asOperator("boss")
	r.ok("ember", "create", "parser", "4", "3")
	r.pool("ember", "parser")

	got := r.ok("boss", "status", "parser", "2")
	if !strings.Contains(got.stderr+got.stdout, "acting as the operator") {
		t.Errorf("standing in for the owner was silent:\n%s%s", got.stdout, got.stderr)
	}
}

// The line that matters most: this is not a master key.
func TestTheOperatorDoesNotTakeOverAnOwnedTask(t *testing.T) {
	r := newRig(t)
	r.operator = asOperator("boss")
	r.ok("ember", "create", "parser", "4", "3")
	r.pool("ember", "parser")
	r.ok("ember", "claim", "parser")

	got := r.run("boss", "scope", "parser", "internal/parse/")
	if got.code == 0 {
		t.Errorf("the operator scoped a task somebody owns:\n%s", got.stdout)
	}
	if !strings.Contains(got.stderr, "ember") {
		t.Errorf("the refusal does not name the owner to ask: %s", got.stderr)
	}
}

// Privacy does not have an exception at the top. An unowned *draft* belongs to
// its author and is not the operator's to see, so it is still not found.
func TestTheOperatorDoesNotSeeSomebodyElsesDraft(t *testing.T) {
	r := newRig(t)
	r.operator = asOperator("boss")
	r.ok("ember", "create", "parser", "4", "3")

	got := r.run("boss", "scope", "parser", "internal/parse/")
	if got.code == 0 {
		t.Fatalf("the operator reached into a draft:\n%s", got.stdout)
	}
	if strings.Contains(got.stderr, "claim it first") {
		t.Errorf("the refusal disclosed that the draft exists: %s", got.stderr)
	}
}

// Everyone else is exactly where they were.
func TestAnAgentIsStillToldToClaimIt(t *testing.T) {
	r := newRig(t)
	r.operator = asOperator("boss")
	r.ok("ember", "create", "parser", "4", "3")
	r.pool("ember", "parser")

	got := r.run("hand", "scope", "parser", "internal/parse/")
	if got.code == 0 {
		t.Fatalf("a stranger scoped an unowned task:\n%s", got.stdout)
	}
	if !strings.Contains(got.stderr, "claim it first") {
		t.Errorf("the refusal lost its next step: %s", got.stderr)
	}
}

// Fails closed, and says why. Widening on a missing answer is how a check like
// this stops meaning anything.
func TestNoFleetMeansNoOperator(t *testing.T) {
	r := newRig(t)
	r.operator = noFleet()
	r.ok("ember", "create", "parser", "4", "3")
	r.pool("ember", "parser")

	got := r.run("boss", "scope", "parser", "internal/parse/")
	if got.code == 0 {
		t.Fatalf("an unreachable fleet promoted somebody:\n%s", got.stdout)
	}
	if !strings.Contains(got.stderr, "could not be asked") {
		t.Errorf("the refusal does not say nobody could be asked: %s", got.stderr)
	}
}

// The check costs a subprocess, so it is asked only when it could change the
// answer — never on ordinary work, and once per command however many tasks the
// command touches.
func TestTheFleetIsAskedOnlyWhenItWouldMatter(t *testing.T) {
	r := newRig(t)
	asked := 0
	r.operator = func(claimed user.Name) (bool, error) {
		asked++
		return claimed.String() == "boss", nil
	}

	r.ok("ember", "create", "parser", "4", "3")
	r.pool("ember", "parser")
	r.ok("ember", "claim", "parser")
	r.ok("ember", "status", "parser", "2")
	if asked != 0 {
		t.Errorf("an owner doing its own work asked the fleet %d times", asked)
	}

	r.ok("ember", "create", "grammar", "4", "3")
	r.pool("ember", "grammar")
	r.ok("boss", "status", "grammar", "2")
	if asked != 1 {
		t.Errorf("the fleet was asked %d times for one command, want 1", asked)
	}
}
