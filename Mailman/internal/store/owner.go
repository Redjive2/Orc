package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"

	"orc/common/fault"
	"orc/common/user"
)

// The store's owner: the one account permitted to read every mailbox.
//
// Mailman has no roles, and until Orc's auth exists it should not grow a role
// system. What it needs is narrower than that — one account, named once, that
// may look at the whole store — and this is that and nothing more.
//
// Provisioning (`admin user add`) stays unauthenticated because an empty store
// has no identity to check and has to be bootstrappable. Reading everyone's
// mail is a different act: on a machine where several agents run as the same
// operating-system user, file permissions separate nothing, so the only thing
// that can separate them is a key.
//
// The rule is therefore: anyone may name the owner while there is none, which is
// the same trust the bootstrap already assumes; once named, only the owner may
// rename it, and only the owner may read the store whole. Unset fails closed —
// a store with no owner refuses the read commands rather than allowing them.
const ownerFile = "owner"

// ownerRecord is a file rather than a field in `version` so that a store written
// by an older Mailman simply has no owner, which is the safe reading.
type ownerRecord struct {
	Name string `json:"name"`
}

// Owner returns the account permitted to read the whole store.
//
// A store with no owner yields the zero name and no error: that is a state, not
// a failure, and the caller decides what to do about it.
func (s *Store) Owner() (user.Name, error) {
	path := filepath.Join(s.root, ownerFile)
	data, err := s.readFile(path)
	if err != nil {
		if errors.Is(err, fault.ErrNotFound) {
			return user.Name{}, nil
		}
		return user.Name{}, err
	}

	var rec ownerRecord
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		return user.Name{}, fault.Parse{Path: path, Reason: "owner record is not readable: " + err.Error()}
	}
	name, err := user.Parse(rec.Name)
	if err != nil {
		return user.Name{}, fault.Parse{Path: path, Reason: "owner record names " + err.Error()}
	}
	return name, nil
}

// SetOwner names the account permitted to read the whole store.
//
// It refuses an account that does not exist, so a typo cannot lock the store's
// admin commands behind a name nobody holds a key for.
//
// Changing an existing owner is the caller's business to authorise; this layer
// only records it, and the command above it requires the current owner's key.
func (s *Store) SetOwner(name user.Name) error {
	if name.Zero() {
		return fault.Internal{Where: "store.SetOwner", Detail: "no user given"}
	}
	exists, err := s.HasUser(name)
	if err != nil {
		return err
	}
	if !exists {
		return fault.NotFound{Target: "mailbox " + name.String()}
	}

	data, err := json.Marshal(ownerRecord{Name: name.String()})
	if err != nil {
		return fault.Internal{Where: "store.SetOwner", Detail: err.Error()}
	}
	path := filepath.Join(s.root, ownerFile)
	return s.withLock(func() error { return s.writeFile(path, append(data, '\n')) })
}

// AuthoriseOwner checks that name may read the whole store.
//
// Authentication has already happened by the time this is called; this is the
// separate question of whether that authenticated account is allowed to see
// everyone's mail. Both failures are refusals, and both say what to do next.
func (s *Store) AuthoriseOwner(name user.Name) error {
	owner, err := s.Owner()
	if err != nil {
		return err
	}
	if owner.Zero() {
		return fault.Denied{
			Actor:  name.String(),
			Action: "read",
			Target: "this store whole",
			Reason: "it has no owner yet — name one with `mailman admin owner <name>`",
		}
	}
	if owner.String() != name.String() {
		// The owner is named rather than the refusal being blank. Who it is is
		// not a secret — it is on every message they send — and naming them
		// turns "denied" into something the reader can act on.
		return fault.Denied{
			Actor:  name.String(),
			Action: "read",
			Target: "this store whole",
			Owner:  owner.String(),
		}
	}
	return nil
}
