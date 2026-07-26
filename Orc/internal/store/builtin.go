package store

import (
	"orc/orc/internal/model"
)

// The builtin permissions.
//
// Almost every permission in a fleet is one somebody made. These are the
// exceptions: capabilities that exist outside Orc — in another tool — and that
// therefore have nobody to define them. `cq upgrade` rebuilds and restarts every
// binary on every machine in the fleet, and the question "who may do that" has to
// have an answer before anybody asks it, in a vocabulary the fleet already speaks.
//
// They are created at bootstrap and re-created by `orc verify --repair` if they go
// missing, and they are otherwise ordinary: assignable to a role, holdable by an
// identity, listed by `orc list permissions`, and refused to anybody below their
// floor. Nothing here is a special case in the derivation.
//
// The floor is the whole of the policy. 90 puts these above every ordinary role in
// a fleet whose agents sit at 1–99, so holding one is a deliberate act by somebody
// at the top rather than something a role drifts into.
const UpgradeFloor = 90

// Builtin returns the permissions every fleet has.
//
// `upgrade` carries `write(**)` because that is literally what an upgrade does: it
// replaces every binary on the machine. Naming the clause after the effect rather
// than after the command keeps the permission honest — somebody reading
// `orc list permissions` sees a permission that may rewrite anything, which is
// exactly the thing they are being asked to hand out.
func Builtin() []struct {
	Name     model.Name
	Floor    model.Authority
	Patterns []model.Pattern
	Why      string
} {
	name, nameErr := model.ParseName("upgrade")
	floor, floorErr := model.NewAuthority(UpgradeFloor)
	clause, clauseErr := model.ParsePattern("write(**)")
	if nameErr != nil || floorErr != nil || clauseErr != nil {
		// Unreachable: all three are constants checked by the test below. Returning
		// nothing rather than panicking keeps the no-panic rule — a fleet without
		// its builtins is a fleet `orc doctor` reports on, not a crash.
		return nil
	}
	return []struct {
		Name     model.Name
		Floor    model.Authority
		Patterns []model.Pattern
		Why      string
	}{
		{Name: name, Floor: floor, Patterns: []model.Pattern{clause},
			Why: "rebuild and restart every Orc tool, on every machine in the fleet"},
	}
}

// EnsureBuiltin creates any builtin permission the store does not have.
//
// Idempotent, and safe to call on a fleet that predates a new builtin: an existing
// permission of the same name is left exactly as it is. Orc never rewrites a
// permission — see permissionRecord — and a fleet where somebody has deliberately
// redefined `upgrade` is not one this should quietly overwrite. `orc doctor`
// reports the difference instead.
func (s *Store) EnsureBuiltin() error {
	for _, want := range Builtin() {
		if _, err := s.Permission(want.Name); err == nil {
			continue
		}
		if _, err := s.CreatePermission(want.Name, want.Floor, want.Patterns); err != nil {
			return err
		}
	}
	return nil
}
