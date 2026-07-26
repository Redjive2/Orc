package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/model"
)

// identityRecord is the immutable half of an identity.
//
// Boss is here as well as being mutable through the journal, for the same reason
// a role's authority is: the record is where the identity started, and a `move`
// is what happened since. An identity created with no boss is the operator, and
// there is exactly one of those.
type identityRecord struct {
	Version int    `json:"version"`
	Name    string `json:"name"`
	ID      string `json:"id"`
	Boss    string `json:"boss"`
	Created string `json:"created"`
}

// CreateIdentity writes an identity's creation record and its directories.
//
// It does not mint a key, provision a mailbox, or write any Claude configuration:
// those belong to internal/provision, which composes this with the other tools.
// The split matters because provisioning can fail halfway — Mailman may refuse a
// name — and the thing that has to clean up after a half-made identity should not
// also be the thing that decides what a whole one is.
//
// boss is zero for the operator alone.
func (s *Store) CreateIdentity(name user.Name, id string, boss user.Name) (model.Identity, error) {
	fresh, err := model.NewIdentity(name, id, boss, s.clock.Now())
	if err != nil {
		return model.Identity{}, err
	}
	data, err := json.MarshalIndent(identityRecord{
		Version: Version,
		Name:    fresh.Name().String(),
		ID:      fresh.ID(),
		Boss:    fresh.Boss().String(),
		Created: clock.Format(fresh.Created()),
	}, "", "  ")
	if err != nil {
		return model.Identity{}, fault.Internal{Where: "store.CreateIdentity", Detail: err.Error()}
	}

	dir := s.identityDir(name)
	err = s.withLock(dir, func() error {
		if _, err := s.loadIdentityRecord(name); err == nil {
			return fault.Conflict{Path: name.String(), Reason: fmt.Sprintf(
				"an identity called %s already exists", name)}
		} else if !isNotFound(err) {
			return err
		}
		if err := s.writeNew(filepath.Join(dir, identityFile), append(data, '\n')); err != nil {
			return err
		}
		// The identity's own directories are made with it, so that populating one
		// later is not also a provisioning step — and so that a workspace an
		// operator wants to put files in exists from the moment the identity does.
		for _, sub := range []string{claudeDir, workspaceDir, sessionDir} {
			path := filepath.Join(dir, sub)
			if err := s.ops.mkdirAll(path, dirMode); err != nil {
				return fault.IO{Op: "create", Path: path, Err: err}
			}
		}
		return nil
	})
	if err != nil {
		return model.Identity{}, err
	}
	return s.Identity(name)
}

// Identity reads an identity: its creation record, then its journal folded onto
// it.
func (s *Store) Identity(name user.Name) (model.Identity, error) {
	i, _, err := s.InspectIdentity(name)
	return i, err
}

// InspectIdentity reads an identity and reports how many bytes at the end of its
// journal an interrupted append left behind.
func (s *Store) InspectIdentity(name user.Name) (model.Identity, int, error) {
	if name.Zero() {
		return model.Identity{}, 0, fault.Internal{Where: "store.InspectIdentity", Detail: "no identity named"}
	}

	base, err := s.loadIdentityRecord(name)
	if err != nil {
		return model.Identity{}, 0, err
	}

	path := filepath.Join(s.identityDir(name), journalFile)
	data, err := s.ops.readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return base, 0, nil
		}
		return model.Identity{}, 0, fault.IO{Op: "read", Path: path, Err: err}
	}
	return FoldIdentity(path, base, data)
}

func (s *Store) loadIdentityRecord(name user.Name) (model.Identity, error) {
	path := filepath.Join(s.identityDir(name), identityFile)

	data, err := s.ops.readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return model.Identity{}, fault.NotFound{Target: name.String()}
		}
		return model.Identity{}, fault.IO{Op: "read", Path: path, Err: err}
	}
	if len(data) > MaxRecordSize {
		return model.Identity{}, fault.Parse{Path: path, Reason: fmt.Sprintf(
			"identity record is %d bytes, limit is %d", len(data), MaxRecordSize)}
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var r identityRecord
	if err := dec.Decode(&r); err != nil {
		return model.Identity{}, fault.Parse{Path: path, Reason: "identity record: " + err.Error()}
	}
	if dec.More() {
		return model.Identity{}, fault.Parse{Path: path, Reason: "identity record has trailing content"}
	}
	if r.Version != Version {
		return model.Identity{}, fault.Parse{Path: path, Reason: fmt.Sprintf(
			"identity record is version %d, this orc writes version %d", r.Version, Version)}
	}

	stored, err := user.Parse(r.Name)
	if err != nil {
		return model.Identity{}, fault.Parse{Path: path, Reason: "identity record name: " + err.Error()}
	}
	if stored.String() != name.String() {
		return model.Identity{}, fault.Conflict{Path: path, Reason: fmt.Sprintf(
			"directory is named for %s but the record says %s", name, stored)}
	}

	// An empty boss is the operator, and it is the only identity allowed one.
	// Distinguishing "no boss" from "a boss whose name would not parse" matters:
	// the first is the root of the tree and the second is a damaged record.
	var boss user.Name
	if strings.TrimSpace(r.Boss) != "" {
		if boss, err = user.Parse(r.Boss); err != nil {
			return model.Identity{}, fault.Parse{Path: path, Reason: "identity record boss: " + err.Error()}
		}
	}
	created, err := clock.Parse(r.Created)
	if err != nil {
		return model.Identity{}, fault.Parse{Path: path, Reason: "identity record created: " + err.Error()}
	}

	got, err := model.NewIdentity(stored, r.ID, boss, created)
	if err != nil {
		return model.Identity{}, fault.Parse{Path: path, Reason: "identity record is invalid: " + err.Error()}
	}
	return got, nil
}

// Identities lists every identity, in name order. The fleet's *tree* order comes
// from the derivation; this is the store's flat view of what exists.
func (s *Store) Identities() ([]model.Identity, error) {
	dirs, err := s.names(filepath.Join(s.root, identitiesDir), MaxIdentities, "identities")
	if err != nil {
		return nil, err
	}

	out := make([]model.Identity, 0, len(dirs))
	for _, dir := range dirs {
		name, err := user.Parse(dir)
		if err != nil {
			continue
		}
		i, err := s.Identity(name)
		if err != nil {
			if isNotFound(err) {
				// A directory with a lock but no record is an interrupted create.
				continue
			}
			return nil, err
		}
		out = append(out, i)
	}
	return out, nil
}

// HasIdentity reports whether an identity exists.
func (s *Store) HasIdentity(name user.Name) (bool, error) {
	if name.Zero() {
		return false, fault.Internal{Where: "store.HasIdentity", Detail: "no identity named"}
	}
	_, err := s.ops.stat(filepath.Join(s.identityDir(name), identityFile))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fault.IO{Op: "check for", Path: s.identityDir(name), Err: err}
}

// WriteClaudeFile writes a file inside an identity's Claude configuration
// directory.
//
// rel is checked rather than trusted, even though every caller is in this module:
// this is the one write path whose path comes from a string rather than from a
// validated name, and the whole point of keeping the layout in one package is
// that a traversal cannot be introduced from outside it.
func (s *Store) WriteClaudeFile(name user.Name, rel string, data []byte) error {
	path, err := s.insideClaude(name, rel)
	if err != nil {
		return err
	}
	return s.writeFile(path, data)
}

// MakeClaudeDir creates a directory inside an identity's Claude configuration.
func (s *Store) MakeClaudeDir(name user.Name, rel string) error {
	if err := s.refuseWrite(); err != nil {
		return err
	}
	path, err := s.insideClaude(name, rel)
	if err != nil {
		return err
	}
	if err := s.ops.mkdirAll(path, dirMode); err != nil {
		return fault.IO{Op: "create", Path: path, Err: err}
	}
	return nil
}

func (s *Store) insideClaude(name user.Name, rel string) (string, error) {
	if name.Zero() {
		return "", fault.Internal{Where: "store.insideClaude", Detail: "no identity named"}
	}
	root := s.ClaudeDir(name)
	clean := filepath.Clean(rel)
	if rel == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fault.Escape{Path: rel, Root: root}
	}
	path := filepath.Join(root, clean)
	if path != root && !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return "", fault.Escape{Path: rel, Root: root}
	}
	return path, nil
}

// DecideIdentity is a caller's conditional write on an identity.
type DecideIdentity func(model.Identity) (model.IdentityEvent, error)

// ApplyIdentity is the only way to change an identity.
//
// Same discipline as ApplyRole: the lock spans the read and the write, so a
// decision can never be made against state that has already moved. That is what
// makes two agents granting and revoking the same permission at the same moment
// resolve to one outcome rather than to both.
func (s *Store) ApplyIdentity(name user.Name, decide DecideIdentity) (model.Identity, error) {
	if name.Zero() {
		return model.Identity{}, fault.Internal{Where: "store.ApplyIdentity", Detail: "no identity named"}
	}
	if decide == nil {
		return model.Identity{}, fault.Internal{Where: "store.ApplyIdentity", Detail: "no decision given"}
	}

	var out model.Identity
	err := s.withLock(s.identityDir(name), func() error {
		current, err := s.Identity(name)
		if err != nil {
			return err
		}

		ev, err := decide(current)
		if err != nil {
			return err
		}
		if ev.Zero() {
			out = current
			return nil
		}

		next, err := current.With(ev)
		if err != nil {
			return err
		}
		line, err := encodeIdentityEvent(ev)
		if err != nil {
			return err
		}
		if err := s.appendLine(filepath.Join(s.identityDir(name), journalFile), line); err != nil {
			return err
		}
		out = next
		return nil
	})
	if err != nil {
		return model.Identity{}, err
	}
	return out, nil
}

// DeleteIdentity removes an identity whole: its record, its journal, its
// credential, its Claude configuration, and its workspace.
//
// This is the one destructive path in the store, and it removes a *workspace*,
// which may hold work nobody else has a copy of. So the CLI requires --yes and
// prints what will go; here the only guard that belongs is the structural one —
// the path must be inside the store, which a validated user.Name already
// guarantees, and which is re-derived rather than assumed.
func (s *Store) DeleteIdentity(name user.Name) error {
	if err := s.refuseWrite(); err != nil {
		return err
	}
	if name.Zero() {
		return fault.Internal{Where: "store.DeleteIdentity", Detail: "no identity named"}
	}
	if _, err := s.loadIdentityRecord(name); err != nil {
		return err
	}

	dir := s.identityDir(name)
	inside := filepath.Join(s.root, identitiesDir)
	if filepath.Dir(dir) != inside {
		return fault.Escape{Path: dir, Root: inside}
	}
	if err := s.ops.removeAll(dir); err != nil {
		return fault.IO{Op: "remove", Path: dir, Err: err}
	}
	s.ops.syncDir(inside)
	return nil
}
