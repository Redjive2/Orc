package cli_test

import (
	"strings"
	"testing"

	"orc/common/fault"
)

// The builtin permissions, and the check another tool asks about them.
//
// `upgrade` is the capability that rebuilds and restarts every binary on every
// machine in the fleet. It lives in cq, so Orc has nobody to define it — which is
// exactly why it is builtin: the question "who may do that" has to have an answer
// before anybody asks it, in the vocabulary the fleet already speaks.

// TestBootstrapMakesTheBuiltins: a fresh fleet can answer the question on day one.
func TestBootstrapMakesTheBuiltins(t *testing.T) {
	r := newRig(t)
	r.bootstrap("boss")

	got := r.ok("boss", "list", "permissions")
	if !strings.Contains(got.stdout, "upgrade") {
		t.Fatalf("a fresh fleet has no upgrade permission:\n%s", got.stdout)
	}
	// The floor is the whole of the policy: 90 puts it above every ordinary role
	// in a fleet whose agents sit at 1–99.
	if !strings.Contains(got.stdout, "90") {
		t.Errorf("the upgrade permission is not at floor 90:\n%s", got.stdout)
	}
	// And it says what it does. `write(**)` is not decoration — an upgrade
	// replaces every binary on the machine, and a permission that claimed less
	// would be one somebody hands out without reading.
	if !strings.Contains(got.stdout, "write(**)") {
		t.Errorf("the upgrade permission does not say what it may do:\n%s", got.stdout)
	}
}

// TestCheckPermissionIsAnExitCode: the contract another tool branches on.
func TestCheckPermissionIsAnExitCode(t *testing.T) {
	r := fullFleet(t)

	// The operator holds every permission in the fleet.
	if got := r.run("boss", "check-permission", "upgrade"); got.code != fault.CodeOK {
		t.Errorf("the operator cannot use upgrade: exit %d\n%s", got.code, got.stderr)
	}
	// An ordinary agent does not, and is refused rather than told nothing.
	got := r.run("ember", "check-permission", "upgrade")
	if got.code != fault.CodeDenied {
		t.Errorf("an agent holding nothing exited %d, want %d", got.code, fault.CodeDenied)
	}
	if !strings.Contains(got.stderr, "upgrade") {
		t.Errorf("the refusal does not name what was asked for:\n%s", got.stderr)
	}

	// A permission this fleet does not have is `2`. That is a fact about the
	// store, not a refusal about policy, and a caller that could not tell them
	// apart would report "you may not" for a typo.
	if got := r.run("boss", "check-permission", "nonesuch"); got.code != fault.CodeNotFound {
		t.Errorf("an unknown permission exited %d, want %d", got.code, fault.CodeNotFound)
	}
}

// TestUpgradeNeedsTheFloor is the point of the floor. An agent can only be given
// `upgrade` by somebody who could already do it, and only if it is senior enough
// to hold it at all.
func TestUpgradeNeedsTheFloor(t *testing.T) {
	r := fullFleet(t)

	// `engineer` is authority 60, below the floor of 90, so the role cannot hold
	// it — Orc refuses, and the refusal is about the floor.
	got := r.run("boss", "assign", "permission", "engineer", "upgrade")
	if got.code == fault.CodeOK {
		t.Fatalf("a role at 60 was given a permission with a floor of 90")
	}
	if !strings.Contains(got.stderr, "90") && !strings.Contains(got.stderr, "floor") {
		t.Errorf("the refusal does not say it is about the floor:\n%s", got.stderr)
	}

	// A role senior enough may hold it, and then so may whoever holds the role.
	r.ok("boss", "new", "role", "chief-of-staff", "95", "runs", "the", "machines")
	r.ok("boss", "assign", "permission", "chief-of-staff", "upgrade")
	r.ok("boss", "assign", "role", "atlas", "chief-of-staff")

	if got := r.run("atlas", "check-permission", "upgrade"); got.code != fault.CodeOK {
		t.Errorf("an executive agent cannot use upgrade: exit %d\n%s", got.code, got.stderr)
	}
	// And its subordinate still cannot: authority is capped by the boss chain, and
	// a permission held above does not flow down.
	r.ok("boss", "move", "quill", "atlas")
	if got := r.run("quill", "check-permission", "upgrade"); got.code != fault.CodeDenied {
		t.Errorf("a subordinate inherited upgrade: exit %d", got.code)
	}
}
