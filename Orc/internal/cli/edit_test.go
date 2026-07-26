package cli_test

import (
	"strings"
	"testing"

	"orc/common/fault"
)

// TestEditPermissionCorrectsInPlace is the case Plan.md §13 did not have when it
// decided a permission was immutable: an operator who typed a clause wrong wants
// that permission fixed, not a second one beside it preserving the misspelling.
func TestEditPermissionCorrectsInPlace(t *testing.T) {
	r := newRig(t)
	r.bootstrap("root")
	r.ok("root", "new", "permission", "edit-anno", "40", "read(Ano/**)")

	got := r.ok("root", "edit", "permission", "edit-anno", "read(Anno/**)", "write(Anno/internal/**)")
	if !strings.Contains(got.stdout, "edited") {
		t.Errorf("the edit was not reported:\n%s", got.stdout)
	}

	// The fleet carries the new clauses and not the old ones.
	after := r.ok("root", "status", "--json").stdout
	if !strings.Contains(after, "read(Anno/**)") {
		t.Errorf("the corrected clause is not in the fleet:\n%s", after)
	}
	if strings.Contains(after, "read(Ano/**)") {
		t.Errorf("the misspelled clause survived:\n%s", after)
	}
}

// TestEditSaysWhoItAffects. Everything the permission guards changes the instant
// the command returns, for every role that holds it — an edit that widened what
// six agents may write should say six.
func TestEditSaysWhoItAffects(t *testing.T) {
	r := newRig(t)
	r.bootstrap("root")
	r.ok("root", "new", "permission", "edit-anno", "40", "read(Anno/**)")
	r.ok("root", "new", "role", "builder", "40", "builds things")
	r.ok("root", "assign", "permission", "builder", "edit-anno")

	got := r.ok("root", "edit", "permission", "edit-anno", "read(Anno/**)", "write(Anno/**)")
	if !strings.Contains(got.stdout, "in force now for") || !strings.Contains(got.stdout, "builder") {
		t.Errorf("the edit did not say who it changed:\n%s", got.stdout)
	}
}

// TestEditRefusesToStrandAHolder. Raising a floor above a holder's authority
// leaves a role holding a permission it is too junior to use. Orc tolerates that
// — verify reports it — but doing it silently from one command would be a
// permission that stops working for reasons nobody was told about.
func TestEditRefusesToStrandAHolder(t *testing.T) {
	r := newRig(t)
	r.bootstrap("root")
	r.ok("root", "new", "permission", "edit-anno", "40", "read(Anno/**)")
	r.ok("root", "new", "role", "builder", "40", "builds things")
	r.ok("root", "assign", "permission", "builder", "edit-anno")

	got := r.run("root", "edit", "permission", "edit-anno", "--floor", "80")
	if got.code != fault.CodeConflict {
		t.Fatalf("code = %d, want %d\n%s%s", got.code, fault.CodeConflict, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stderr, "builder") {
		t.Errorf("the refusal does not name the role it would strand:\n%s", got.stderr)
	}
	// A refusal names the way forward.
	if !strings.Contains(got.stderr, "--from") {
		t.Errorf("the refusal does not say what to do instead:\n%s", got.stderr)
	}
}

// TestEditKeepsWhatItIsNotGiven: the floor survives a clause-only edit, and the
// clauses survive a floor-only one. A form that posts one half must not wipe the
// other.
func TestEditKeepsWhatItIsNotGiven(t *testing.T) {
	r := newRig(t)
	r.bootstrap("root")
	r.ok("root", "new", "permission", "p", "40", "read(A/**)", "write(B/**)")

	clauses := r.ok("root", "edit", "permission", "p", "read(C/**)").stdout
	if !strings.Contains(clauses, "floor 40") {
		t.Errorf("a clause-only edit changed the floor:\n%s", clauses)
	}

	floor := r.ok("root", "edit", "permission", "p", "--floor", "20").stdout
	if !strings.Contains(floor, "read(C/**)") {
		t.Errorf("a floor-only edit changed the clauses:\n%s", floor)
	}
}

// TestEditThatChangesNothingSaysSo, rather than appending an event that changes
// nothing to a journal somebody will read later.
func TestEditThatChangesNothingSaysSo(t *testing.T) {
	r := newRig(t)
	r.bootstrap("root")
	r.ok("root", "new", "permission", "p", "40", "read(A/**)")

	got := r.ok("root", "edit", "permission", "p", "read(A/**)")
	if !strings.Contains(got.stdout, "already that") {
		t.Errorf("a no-op edit was reported as a change:\n%s", got.stdout)
	}
}

// TestEditRefusesTheThingsItCannot.
func TestEditRefusesTheThingsItCannot(t *testing.T) {
	r := newRig(t)
	r.bootstrap("root")
	r.ok("root", "new", "permission", "p", "40", "read(A/**)")

	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"no such permission", []string{"edit", "permission", "nope", "read(A/**)"}, fault.CodeNotFound},
		{"a role cannot be edited", []string{"edit", "role", "builder"}, fault.CodeUsage},
		{"nothing named", []string{"edit", "permission"}, fault.CodeUsage},
		// A malformed clause or floor is a usage error, not a parse one: the same
		// classification `orc new permission` gives the same input, because the
		// text came off a command line rather than out of the store.
		{"a bad clause", []string{"edit", "permission", "p", "read(A/**"}, fault.CodeUsage},
		{"a bad floor", []string{"edit", "permission", "p", "--floor", "nine"}, fault.CodeUsage},
		{"a floor outside the range", []string{"edit", "permission", "p", "--floor", "0"}, fault.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.run("root", tc.args...); got.code != tc.want {
				t.Errorf("code = %d, want %d\n%s", got.code, tc.want, got.stderr)
			}
		})
	}
}

// TestADeletedPermissionLeavesNoHistory. A permission created again under the
// same name must not inherit the amendments of the one that was deleted.
func TestADeletedPermissionLeavesNoHistory(t *testing.T) {
	r := newRig(t)
	r.bootstrap("root")
	r.ok("root", "new", "permission", "p", "40", "read(A/**)")
	r.ok("root", "edit", "permission", "p", "write(SECRET/**)")
	r.ok("root", "remove", "permission", "p")
	r.ok("root", "new", "permission", "p", "40", "read(A/**)")

	got := r.ok("root", "status", "--json").stdout
	if strings.Contains(got, "SECRET") {
		t.Errorf("the new permission inherited a deleted one's journal:\n%s", got)
	}
}
