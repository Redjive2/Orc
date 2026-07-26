package store

import (
	"os"
	"path/filepath"
	"strings"

	"orc/common/fault"
	"orc/common/user"
)

// This file is the only place a plaintext key is written or read, and the reason
// it is worth isolating is Plan.md §4.2: Orc holds the only plaintext copy of
// every key in the fleet, because Orc is the only thing that must hand one out
// again later. A digest cannot be turned back into a key, and a session that has
// to be restarted needs the same credential it had before.
//
// Two rules, both enforced here rather than trusted to callers:
//
//   - the key file is 0600 inside a 0700 store, which is the whole boundary;
//   - Key never appears in a rendered screen, a log line, or an error message.
//     The one command that discloses one is `orc env`, which says so.

// WriteCredential stores an identity's credential: the digest record every other
// Orc tool verifies against, and the plaintext key only Orc keeps.
//
// Both are written or neither is. A digest with no key beside it is an identity
// nobody can ever act as, and a key with no digest is a credential nothing will
// accept — so the digest goes first and the key second, and a failure between
// them leaves an identity that provisioning will clean up rather than one that
// half works.
func (s *Store) WriteCredential(name user.Name, key string) error {
	if err := s.refuseWrite(); err != nil {
		return err
	}
	if name.Zero() {
		return fault.Internal{Where: "store.WriteCredential", Detail: "no identity named"}
	}
	if err := user.CheckKey(key); err != nil {
		return err
	}

	record, err := user.NewRecord(name, key, s.clock.Now(), nil)
	if err != nil {
		return err
	}
	encoded, err := record.Encode()
	if err != nil {
		return err
	}

	dir := s.identityDir(name)
	if err := s.writeFile(filepath.Join(dir, userFile), encoded); err != nil {
		return err
	}
	return s.writeFile(filepath.Join(dir, keyFile), []byte(key+"\n"))
}

// Key reads an identity's plaintext key.
//
// The error deliberately does not quote the file's contents, and neither does any
// caller: a key that reaches a diagnostic reaches a log, and a log is the one
// place a credential must never be.
func (s *Store) Key(name user.Name) (string, error) {
	if name.Zero() {
		return "", fault.Internal{Where: "store.Key", Detail: "no identity named"}
	}
	path := filepath.Join(s.identityDir(name), keyFile)

	data, err := s.ops.readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fault.NotFound{Target: "a key for " + name.String()}
		}
		return "", fault.IO{Op: "read", Path: path, Err: err}
	}
	key := strings.TrimSpace(string(data))
	if err := user.CheckKey(key); err != nil {
		return "", fault.Parse{Path: path, Reason: "stored key is not usable: " + err.Error()}
	}
	return key, nil
}

// Credential reads the digest record, for verifying a key offered by a caller.
//
// This is the other half of the contract Common/identity describes: it resolves
// ORC_USER and ORC_KEY from the environment, and this is what says whether they
// are real. Milestone 5 exposes the same check to Macmuffin through
// orc/common/account, so that a tool which currently trusts $ORC_USER can stop.
func (s *Store) Credential(name user.Name) (user.Record, error) {
	if name.Zero() {
		return user.Record{}, fault.Internal{Where: "store.Credential", Detail: "no identity named"}
	}
	path := filepath.Join(s.identityDir(name), userFile)

	data, err := s.ops.readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return user.Record{}, fault.NotFound{Target: "a credential for " + name.String()}
		}
		return user.Record{}, fault.IO{Op: "read", Path: path, Err: err}
	}
	return user.Decode(path, data)
}

// Authenticate verifies a key against an identity's stored record.
//
// It fails closed in every direction: an identity that does not exist, a
// credential that cannot be read, and a key that does not match all produce the
// same authentication failure, with the real cause carried in Detail for a log
// rather than for the caller. Telling an unauthenticated caller which of the
// three happened is an enumeration oracle over the fleet's roster.
func (s *Store) Authenticate(name user.Name, key string) error {
	record, err := s.Credential(name)
	if err != nil {
		return fault.Auth{Reason: "authentication failed", Detail: err.Error()}
	}
	return record.Verify(key)
}

// HasCredential reports whether an identity has both halves of its credential.
// `orc verify` uses it: a half-provisioned identity is the one shape a crash
// during `orc new identity` can leave behind.
func (s *Store) HasCredential(name user.Name) (bool, error) {
	dir := s.identityDir(name)
	for _, file := range []string{userFile, keyFile} {
		if _, err := s.ops.stat(filepath.Join(dir, file)); err != nil {
			if os.IsNotExist(err) {
				return false, nil
			}
			return false, fault.IO{Op: "check for", Path: filepath.Join(dir, file), Err: err}
		}
	}
	return true, nil
}

// Sessions lives in session.go, now that there are sessions to read.
