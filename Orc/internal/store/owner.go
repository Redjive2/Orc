package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/model"
)

// The two owner-side operations that change the shape of a fleet rather than its
// contents: renaming an identity, and destroying the whole thing.
//
// Both are unusual for this store. Everything else here is append-only or
// write-once, on the principle that a record nobody rewrites is a record nobody
// can corrupt. A rename cannot be either — a name is a directory, and the
// credential's digest binds it (see orc/common/user.derive) — so it is a *copy
// then switch*, ordered so that every intermediate state still derives.

// RenameIdentity gives an identity a new name.
//
// The order below is the whole design, and it is chosen so a crash at any point
// leaves a fleet that still derives:
//
//  1. build the new directory completely — record, re-derived credential, journal,
//     configuration, workspace;
//  2. point the `operator` file at it, if it named the old name;
//  3. append a `move` to every child, so nobody's boss is missing;
//  4. remove the old directory.
//
// After step 1 both names exist and both are valid identities. After step 3 the
// children point at the new one and the old is an unused identity with no
// children — odd, but derivable, which is the property that matters. Only step 4
// makes it tidy. The alternative orderings all have a window in which somebody's
// boss does not exist, and a fleet that will not derive refuses *every* command.
//
// Two things it cannot do, and the caller has to say so:
//
//   - the credential's digest is re-derived rather than reused. The name is part
//     of the HMAC message, so the old record would not verify under the new name.
//     The plaintext key is unchanged, which is what makes the rename invisible to
//     anybody holding it.
//   - it does not touch Mailman. A mailbox is Mailman's, there is no rename in
//     that tool, and mail addressed to the old name lives in the old mailbox.
//     internal/provision does that half, and the CLI says what it costs.
func (s *Store) RenameIdentity(by, from, to user.Name) error {
	if err := s.refuseWrite(); err != nil {
		return err
	}
	if by.Zero() || from.Zero() || to.Zero() {
		return fault.Internal{Where: "store.RenameIdentity", Detail: "rename needs an actor and two names"}
	}
	if from.String() == to.String() {
		return fault.Usage{Reason: fmt.Sprintf("%s is already called that", from)}
	}

	current, err := s.Identity(from)
	if err != nil {
		return err
	}
	if exists, err := s.HasIdentity(to); err != nil {
		return err
	} else if exists {
		return fault.Conflict{Path: to.String(), Reason: fmt.Sprintf(
			"an identity called %s already exists", to)}
	}

	// A live session holds a pty, a socket, and an environment full of the old
	// paths. Renaming underneath it would leave a supervisor writing to a
	// directory that no longer exists, so this refuses rather than trying to
	// carry a running process across.
	if _, live, err := s.Session(from); err != nil {
		return err
	} else if live {
		return fault.Conflict{Path: from.String(), Reason: fmt.Sprintf(
			"%s has a live session; `orc fire %s --yes` first, or wait for it to stop", from, from)}
	}

	key, err := s.Key(from)
	if err != nil {
		return err
	}

	// Step 1: the new directory, complete before anything points at it.
	if err := s.copyIdentity(current, to, key); err != nil {
		return err
	}

	// Step 2: the operator file, if this was the operator.
	if recorded, err := s.Operator(); err == nil && recorded.String() == from.String() {
		if err := s.writeFile(filepath.Join(s.root, operatorFile), []byte(to.String()+"\n")); err != nil {
			return err
		}
	}

	// Step 3: every child's boss. Appended as an ordinary `move`, in the identity
	// journal's own vocabulary — the same event `orc move` writes — so nothing
	// replaying a journal has to learn what a rename is.
	identities, err := s.Identities()
	if err != nil {
		return err
	}
	for _, other := range identities {
		if other.Boss().String() != from.String() || other.Name().String() == to.String() {
			continue
		}
		if _, err := s.ApplyIdentity(other.Name(), func(model.Identity) (model.IdentityEvent, error) {
			return model.Move(by, s.clock.Now(), to)
		}); err != nil {
			return err
		}
	}

	// Step 4: the old directory.
	return s.DeleteIdentity(from)
}

// copyIdentity writes a complete identity under a new name.
func (s *Store) copyIdentity(from model.Identity, to user.Name, key string) error {
	dir := s.identityDir(to)

	record, err := json.MarshalIndent(identityRecord{
		Version: Version,
		Name:    to.String(),
		// The id and the creation time come across unchanged: this is the same
		// agent, and a rename that reset either would make the fleet's history
		// look like a resignation and a new hire.
		ID:      from.ID(),
		Boss:    from.Boss().String(),
		Created: clock.Format(from.Created()),
	}, "", "  ")
	if err != nil {
		return fault.Internal{Where: "store.copyIdentity", Detail: err.Error()}
	}

	err = s.withLock(dir, func() error {
		if err := s.writeNew(filepath.Join(dir, identityFile), append(record, '\n')); err != nil {
			return err
		}
		for _, sub := range []string{claudeDir, workspaceDir, sessionDir} {
			path := filepath.Join(dir, sub)
			if err := s.ops.mkdirAll(path, dirMode); err != nil {
				return fault.IO{Op: "create", Path: path, Err: err}
			}
		}
		// The journal comes across verbatim. Its events name whoever acted, which
		// is history and stays true: the agent that was called something else
		// really did make those changes under that name.
		return s.copyTree(filepath.Join(s.identityDir(from.Name()), journalFile),
			filepath.Join(dir, journalFile))
	})
	if err != nil {
		return err
	}

	// The credential, re-derived. The name is part of the digest, so the record
	// has to be rebuilt; the key is the same, so nothing holding it notices.
	if err := s.WriteCredential(to, key); err != nil {
		return err
	}

	// The agent's own things: its instructions, its memories, its work.
	for _, sub := range []string{claudeDir, workspaceDir} {
		if err := s.copyTree(filepath.Join(s.identityDir(from.Name()), sub),
			filepath.Join(dir, sub)); err != nil {
			return err
		}
	}
	// Session state deliberately does not come across. There is no live session —
	// this refuses one above — and a stale session.json under the new name would
	// claim a supervisor that never existed.
	return nil
}

// copyTree copies a file or a directory, preserving modes.
//
// It is a copy rather than a rename because the two halves have to coexist for
// the ordering above to be crash-safe. A missing source is not an error: an
// identity that never had a journal, or whose workspace was removed, is an
// identity with less to carry.
func (s *Store) copyTree(from, to string) error {
	info, err := s.ops.stat(from)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fault.IO{Op: "check", Path: from, Err: err}
	}

	if !info.IsDir() {
		data, err := s.ops.readFile(from)
		if err != nil {
			return fault.IO{Op: "read", Path: from, Err: err}
		}
		return s.writeFile(to, data)
	}

	if err := s.ops.mkdirAll(to, dirMode); err != nil {
		return fault.IO{Op: "create", Path: to, Err: err}
	}
	entries, err := s.ops.readDir(from)
	if err != nil {
		return fault.IO{Op: "list", Path: from, Err: err}
	}
	for _, e := range entries {
		if err := s.copyTree(filepath.Join(from, e.Name()), filepath.Join(to, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// Census counts what a fleet holds, for a confirmation that has to be specific.
//
// "This will remove everything" is not a confirmation anybody can weigh. "Three
// identities, two workspaces holding files, one live session" is.
type Census struct {
	Identities int
	Employed   int
	Live       int
	Workspaces int
	Roles      int
	Permission int
}

// Survey counts the fleet without changing it.
func (s *Store) Survey() (Census, error) {
	var out Census

	identities, err := s.Identities()
	if err != nil {
		return Census{}, err
	}
	out.Identities = len(identities)
	for _, i := range identities {
		if i.Employed() {
			out.Employed++
		}
		if _, live, err := s.Session(i.Name()); err == nil && live {
			out.Live++
		}
		if entries, err := s.ops.readDir(s.WorkspaceDir(i.Name())); err == nil && len(entries) > 0 {
			out.Workspaces++
		}
	}

	roles, err := s.Roles()
	if err != nil {
		return Census{}, err
	}
	out.Roles = len(roles)

	permissions, err := s.Permissions()
	if err != nil {
		return Census{}, err
	}
	out.Permission = len(permissions)
	return out, nil
}

// Destroy removes the whole fleet.
//
// It is the only function in this package that deletes something it did not just
// create, so it checks that the thing it is about to remove really is a fleet:
// a store has a version file, and a directory without one is somebody's home
// directory or a mistyped path. The refusal is deliberately not overridable —
// a flag that let `orc` remove an arbitrary directory would be a footgun with a
// safety catch, which is worse than no safety catch at all.
//
// Whether the operator meant it is the CLI's question. Whether this is a fleet is
// this function's.
func (s *Store) Destroy() error {
	if err := s.refuseWrite(); err != nil {
		return err
	}

	root := filepath.Clean(s.root)
	if root == "/" || root == "" {
		return fault.Escape{Path: root, Root: "a fleet"}
	}
	if home, err := os.UserHomeDir(); err == nil && filepath.Clean(home) == root {
		return fault.Escape{Path: root, Root: "a fleet — that is your home directory"}
	}
	if _, err := s.ops.stat(filepath.Join(root, versionFile)); err != nil {
		return fault.NotFound{Target: fmt.Sprintf(
			"a fleet at %s (no version file); orc will not remove a directory it cannot recognise", root)}
	}

	if err := s.ops.removeAll(root); err != nil {
		return fault.IO{Op: "remove", Path: root, Err: err}
	}
	return nil
}

// OwnedByCaller reports whether the store is private to the process's own unix
// user, and says why not when it is not.
//
// This is what the credential fallback in the CLI rests on: the keyring is
// plaintext at 0600 inside a 0700 directory, so a process that can read the
// directory can already read every key in it. Asking such a process to also
// export one adds no security, only friction — but that argument only holds while
// the directory really is private, so it is checked rather than assumed.
func (s *Store) OwnedByCaller() (bool, string) {
	info, err := os.Stat(s.root)
	if err != nil {
		return false, "the fleet cannot be read: " + err.Error()
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return false, fmt.Sprintf("%s is mode %04o, so it is not private to you", s.root, mode)
	}
	uid, ok := ownerUID(info)
	if !ok {
		return false, "this platform does not report a file's owner"
	}
	if uid != os.Getuid() {
		return false, fmt.Sprintf("%s belongs to uid %d, and this process is uid %d", s.root, uid, os.Getuid())
	}
	return true, ""
}

// OperatorCredential reads the operator's name and key straight from the keyring.
//
// It exists for one caller — the CLI's owner fallback — and it is deliberately not
// general: it never takes a name, so it cannot be used to look up somebody else's
// key. An agent presents its credential; the owner of the store is the one party
// whose credential is already in their own hands.
func (s *Store) OperatorCredential() (user.Name, string, error) {
	if private, why := s.OwnedByCaller(); !private {
		return user.Name{}, "", fault.Auth{
			Reason: "orc will not read the operator's key from a fleet it cannot call private: " + why,
			Detail: "keyring fallback refused",
		}
	}

	who, err := s.Operator()
	if err != nil {
		return user.Name{}, "", err
	}
	key, err := s.Key(who)
	if err != nil {
		return user.Name{}, "", err
	}
	return who, key, nil
}
