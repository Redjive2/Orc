package session

import (
	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/provision"
	"orc/orc/internal/store"
)

// Prepare writes the two files a session's enforcement rests on: its compiled
// settings and its permission snapshot.
//
// It runs once per session, before the child starts — not once per *restart*. A
// restart continues the same conversation under the same id, so the permissions it
// started with are still the permissions it started with; recompiling would let a
// grant made in the meantime change what a running session may do, which is the one
// thing a snapshot exists to pin down. `orc refresh` is what asks for a new session,
// and that gets a new snapshot because it gets a new id.
//
// Both files are session-scoped and both are replaced wholesale. Neither is fatal to
// get wrong in the same way: without settings the session runs with the hook alone
// (which is the boundary anyway), and without a snapshot the hook's middle rung is
// missing and it falls to blocking writes. So a failure here is reported and the
// session still starts — an agent that cannot think is worse than an agent whose
// cheap first layer is missing, and the expensive layer is intact either way.
func Prepare(s *store.Store, name user.Name, id string) error {
	if s == nil {
		return fault.Internal{Where: "session.Prepare", Detail: "no store given"}
	}
	if name.Zero() || id == "" {
		return fault.Internal{Where: "session.Prepare", Detail: "a session needs an identity and an id"}
	}

	// Derived here rather than passed in, because the supervisor is a separate
	// process from the `orc employ` that spawned it: whatever that command derived is
	// a fleet from before this session existed.
	fleet, err := s.Fleet()
	if err != nil {
		return err
	}
	clauses := fleet.Clauses(name)
	budget, _ := fleet.Budget(name)

	snapshot := store.Freeze(name, id, clock.Format(s.Now()), clauses, budget)
	if err := s.WriteAuthz(name, snapshot); err != nil {
		return err
	}

	return provision.WriteSettings(s, name, provision.SettingsSpec{
		Clauses:   clauses,
		OrcHome:   s.Root(),
		Workspace: s.WorkspaceDir(name),
	})
}
