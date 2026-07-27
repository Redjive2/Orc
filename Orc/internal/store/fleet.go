package store

import (
	"orc/orc/internal/authz"
)

// Fleet reads everything and derives it.
//
// Every command starts here, and that is the point of the design in Plan.md §2.4:
// nothing effective is stored, so a command cannot act on a cached authority that
// a `move` has since invalidated. The cost is one pass over the store per command,
// which for a fleet of tens is a handful of small files.
//
// The derivation is refused wholesale when the tree is structurally broken — no
// operator, a missing boss, a cycle — and that refusal is what every command
// inherits. A partly derived fleet would let some commands succeed against a
// store nobody can reason about, which is worse than every command saying the
// same true thing.
func (s *Store) Fleet() (authz.Fleet, error) {
	identities, err := s.Identities()
	if err != nil {
		return authz.Fleet{}, err
	}
	roles, err := s.Roles()
	if err != nil {
		return authz.Fleet{}, err
	}
	permissions, err := s.Permissions()
	if err != nil {
		return authz.Fleet{}, err
	}
	sessions, err := s.Sessions()
	if err != nil {
		return authz.Fleet{}, err
	}

	return authz.New(authz.Snapshot{
		Identities:  identities,
		Roles:       roles,
		Permissions: permissions,
		Sessions:    sessions,
		Now:         s.clock.Now(),
		// What this fleet charges, read once per derivation and carried in, so
		// every budget in the answer was computed against one price list.
		Tariff: s.Tariff(),
	})
}
