package cli_test

import (
	"strings"
	"testing"

	"orc/common/fault"
)

// The worklist budget. What matters is that this stays a *shorthand*: it must end
// up producing exactly the `spawn(n)` clause the derivation already reads, so that
// `orc employ` refuses on the new number without knowing this command exists.

// TestBudgetGatesEmploy is the whole point. Set a number, and the thing that
// spends it obeys — through the derivation, not through a second code path.
func TestBudgetGatesEmploy(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "move", "quill", "atlas")

	// architect already asks for spawn(24) via `lead`, which `orc budget` does not
	// manage. Take it off first, so this is testing the new verb rather than that.
	r.ok("boss", "remove", "permission", "lead", "--from", "architect")
	r.ok("boss", "budget", "architect", "6")

	// sonnet/medium is 4, which fits.
	if got := r.run("atlas", "employ", "quill"); got.code != fault.CodeOK {
		t.Fatalf("employing within the budget exited %d\n%s", got.code, got.stderr)
	}

	// opus/max is 18, which does not — and the refusal is the budget's.
	r.hire("atlas", "nib")
	r.ok("boss", "assign", "role", "nib", "engineer")
	got := r.run("atlas", "employ", "nib", "--model", "opus", "--effort", "max")
	if got.code != fault.CodeDenied {
		t.Fatalf("employing over the budget exited %d, want %d\n%s", got.code, fault.CodeDenied, got.stderr)
	}
	if !strings.Contains(got.stderr, "of 6") {
		t.Errorf("the refusal does not name the budget that was set:\n%s", got.stderr)
	}

	// Raise it and the same command goes through, with nothing else changed.
	r.ok("boss", "budget", "architect", "40")
	if got := r.run("atlas", "employ", "nib", "--model", "opus", "--effort", "max"); got.code != fault.CodeOK {
		t.Fatalf("employing after the budget rose exited %d\n%s", got.code, got.stderr)
	}
}

// TestBudgetSwapsRatherThanEdits: a permission is immutable, so changing a budget
// means holding a different one. The old permission must actually come off — two
// spawn clauses would leave the derivation taking the larger, which is the number
// nobody asked for.
func TestBudgetSwapsRatherThanEdits(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "remove", "permission", "lead", "--from", "architect")

	r.ok("boss", "budget", "architect", "24")
	r.ok("boss", "budget", "architect", "6")

	role := r.ok("boss", "list", "roles", "--json")
	if !strings.Contains(role.stdout, "spawn-6") {
		t.Errorf("the role does not hold the new budget:\n%s", role.stdout)
	}
	if strings.Contains(role.stdout, "spawn-24") {
		t.Errorf("the role still holds the old budget; the derivation would take the larger:\n%s", role.stdout)
	}

	// Lowering it is what a stale clause would break, so check the effect and not
	// only the bookkeeping.
	got := r.ok("boss", "list", "permissions")
	for _, want := range []string{"spawn-6", "spawn(6)"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("the permissions roster does not show %q:\n%s", want, got.stdout)
		}
	}
}

// TestBudgetIsIdempotent: setting the number a role already has writes nothing and
// says so, which is what makes it safe in a setup script.
func TestBudgetIsIdempotent(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "remove", "permission", "lead", "--from", "architect")

	first := r.ok("boss", "budget", "architect", "24")
	if !strings.Contains(first.stdout, "24") {
		t.Errorf("the first set said nothing useful:\n%s", first.stdout)
	}
	again := r.ok("boss", "budget", "architect", "24")
	if !strings.Contains(again.stdout, "already") {
		t.Errorf("setting the same budget twice should say it was already there:\n%s", again.stdout)
	}
}

// TestBudgetRefusesAForeignSpawnClause: `architect` gets spawn(24) from `lead`,
// which this command did not create and will not silently outvote.
func TestBudgetRefusesAForeignSpawnClause(t *testing.T) {
	r := fullFleet(t)

	got := r.run("boss", "budget", "architect", "6")
	if got.code != fault.CodeConflict {
		t.Fatalf("exit %d, want %d\n%s", got.code, fault.CodeConflict, got.stderr)
	}
	for _, want := range []string{"lead", "largest", "orc remove permission lead --from architect"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("the refusal should mention %q:\n%s", want, got.stderr)
		}
	}
	// And it must not have half-done it.
	if roles := r.ok("boss", "list", "roles", "--json"); strings.Contains(roles.stdout, "spawn-6") {
		t.Errorf("a refused set still assigned the permission:\n%s", roles.stdout)
	}
}

// TestBudgetIsTheOperatorsAlone: a budget is authority over machine time, and an
// agent that could raise its own would have no budget at all.
func TestBudgetIsTheOperatorsAlone(t *testing.T) {
	r := fullFleet(t)

	got := r.run("atlas", "budget", "architect", "99")
	if got.code != fault.CodeDenied {
		t.Errorf("an agent setting a budget exited %d, want %d\n%s", got.code, fault.CodeDenied, got.stderr)
	}
	if !strings.Contains(got.stderr, "in this fleet") {
		t.Errorf("the refusal reads oddly:\n%s", got.stderr)
	}

	// Reading is not setting: an agent may see what it may spend.
	if got := r.run("atlas", "budget"); got.code != fault.CodeOK {
		t.Errorf("an agent cannot read its own budget: exit %d\n%s", got.code, got.stderr)
	}
}

// TestBudgetReportShowsWhatIsSpent, and tells "no budget" apart from "a budget of
// zero" — both refuse every employ, and they are different mistakes.
func TestBudgetReportShowsWhatIsSpent(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "move", "quill", "atlas")
	r.ok("atlas", "employ", "quill")

	got := r.ok("boss", "budget")
	for _, want := range []string{"atlas", "quill", "budget", "spent"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("the budget table does not mention %q:\n%s", want, got.stdout)
		}
	}
	// engineer holds no spawn clause at all, so quill's row reads "none" rather
	// than "0".
	for _, line := range strings.Split(got.stdout, "\n") {
		if strings.Contains(line, "quill") && !strings.Contains(line, "none") {
			t.Errorf("an identity with no budget should say so, not show a number: %q", line)
		}
	}
}

// TestBudgetRefusesWhatIsNotANumber, and the arities either side of the two forms.
func TestBudgetRefusesWhatIsNotANumber(t *testing.T) {
	r := fullFleet(t)

	for _, args := range [][]string{
		{"budget", "architect", "twelve"},
		{"budget", "architect"},
		{"budget", "architect", "6", "extra"},
	} {
		if got := r.run("boss", args...); got.code != fault.CodeUsage {
			t.Errorf("orc %s exited %d, want %d\n%s",
				strings.Join(args, " "), got.code, fault.CodeUsage, got.stderr)
		}
	}
	// A role that does not exist is not found, not a usage error: the shape was
	// right and the target was not there.
	if got := r.run("boss", "budget", "nobody", "6"); got.code != fault.CodeNotFound {
		t.Errorf("an unknown role exited %d, want %d", got.code, fault.CodeNotFound)
	}
}

// TestBudgetIsAColourLayer: the table holds the property every screen holds.
func TestBudgetIsAColourLayer(t *testing.T) {
	r := fullFleet(t)
	plain := r.ok("boss", "budget", "--no-color")
	coloured := r.ok("boss", "budget", "--color")
	if stripped := strip(coloured.stdout); stripped != plain.stdout {
		t.Errorf("the budget table differs once colour is stripped:\nplain:\n%s\nstripped:\n%s",
			plain.stdout, stripped)
	}
}
