package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"orc/common/clock"
	"orc/common/fault"
	"orc/orc/internal/model"
)

// roleRecord is the immutable half of a role: what is fixed the moment it
// exists, plus the values it was created with.
//
// Authority and description are here *and* mutable through the journal, the same
// shape Macmuffin uses for a task's scores: the record is the starting point and
// the fold is what happened since. The alternative — an empty record and a
// journal that must contain an initial authority event — makes a role with no
// journal invalid, and a store where a valid entity depends on a second file
// existing is one an interrupted create can leave unreadable.
type roleRecord struct {
	Version     int    `json:"version"`
	Name        string `json:"name"`
	Authority   int    `json:"authority"`
	Description string `json:"description"`
	Created     string `json:"created"`
}

// CreateRole writes a role's creation record.
func (s *Store) CreateRole(name model.Name, authority model.Authority, description string) (model.Role, error) {
	fresh, err := model.NewRole(name, authority, description, s.clock.Now())
	if err != nil {
		return model.Role{}, err
	}
	data, err := json.MarshalIndent(roleRecord{
		Version:     Version,
		Name:        fresh.Name().String(),
		Authority:   fresh.Authority().Int(),
		Description: fresh.Description(),
		Created:     clock.Format(fresh.Created()),
	}, "", "  ")
	if err != nil {
		return model.Role{}, fault.Internal{Where: "store.CreateRole", Detail: err.Error()}
	}

	dir := s.roleDir(name)
	err = s.withLock(dir, func() error {
		if _, err := s.loadRoleRecord(name); err == nil {
			return fault.Conflict{Path: name.String(), Reason: fmt.Sprintf("a role called %s already exists", name)}
		} else if !isNotFound(err) {
			return err
		}
		return s.writeNew(filepath.Join(dir, roleFile), append(data, '\n'))
	})
	if err != nil {
		return model.Role{}, err
	}
	return s.Role(name)
}

// Role reads a role: its creation record, then its journal folded onto it.
func (s *Store) Role(name model.Name) (model.Role, error) {
	r, _, err := s.InspectRole(name)
	return r, err
}

// InspectRole reads a role and reports how many bytes at the end of its journal
// an interrupted append left behind.
//
// Role throws that count away, because every command but one is right not to
// care: the fold already recovered. `verify` is the one that cares, since a store
// accumulating interrupted appends is a store something keeps killing.
func (s *Store) InspectRole(name model.Name) (model.Role, int, error) {
	if name.Zero() {
		return model.Role{}, 0, fault.Internal{Where: "store.InspectRole", Detail: "no role named"}
	}

	base, err := s.loadRoleRecord(name)
	if err != nil {
		return model.Role{}, 0, err
	}

	path := filepath.Join(s.roleDir(name), journalFile)
	data, err := s.ops.readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return base, 0, nil
		}
		return model.Role{}, 0, fault.IO{Op: "read", Path: path, Err: err}
	}
	return FoldRole(path, base, data)
}

func (s *Store) loadRoleRecord(name model.Name) (model.Role, error) {
	path := filepath.Join(s.roleDir(name), roleFile)

	data, err := s.ops.readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return model.Role{}, fault.NotFound{Target: "role " + name.String()}
		}
		return model.Role{}, fault.IO{Op: "read", Path: path, Err: err}
	}
	if len(data) > MaxRecordSize {
		return model.Role{}, fault.Parse{Path: path, Reason: fmt.Sprintf(
			"role record is %d bytes, limit is %d", len(data), MaxRecordSize)}
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var r roleRecord
	if err := dec.Decode(&r); err != nil {
		return model.Role{}, fault.Parse{Path: path, Reason: "role record: " + err.Error()}
	}
	if dec.More() {
		return model.Role{}, fault.Parse{Path: path, Reason: "role record has trailing content"}
	}
	if r.Version != Version {
		return model.Role{}, fault.Parse{Path: path, Reason: fmt.Sprintf(
			"role record is version %d, this orc writes version %d", r.Version, Version)}
	}

	stored, err := model.ParseName(r.Name)
	if err != nil {
		return model.Role{}, fault.Parse{Path: path, Reason: "role record name: " + err.Error()}
	}
	if !stored.Equal(name) {
		return model.Role{}, fault.Conflict{Path: path, Reason: fmt.Sprintf(
			"directory is named for %s but the record says %s", name, stored)}
	}
	authority, err := model.NewAuthority(r.Authority)
	if err != nil {
		return model.Role{}, fault.Parse{Path: path, Reason: "role record authority: " + err.Error()}
	}
	created, err := clock.Parse(r.Created)
	if err != nil {
		return model.Role{}, fault.Parse{Path: path, Reason: "role record created: " + err.Error()}
	}

	got, err := model.NewRole(stored, authority, r.Description, created)
	if err != nil {
		return model.Role{}, fault.Parse{Path: path, Reason: "role record is invalid: " + err.Error()}
	}
	return got, nil
}

// Roles lists every role, in name order.
func (s *Store) Roles() ([]model.Role, error) {
	dirs, err := s.names(filepath.Join(s.root, rolesDir), MaxRoles, "roles")
	if err != nil {
		return nil, err
	}

	out := make([]model.Role, 0, len(dirs))
	for _, dir := range dirs {
		name, err := model.ParseName(dir)
		if err != nil {
			// Not written by Orc. Skipping is right; `verify` reports it.
			continue
		}
		r, err := s.Role(name)
		if err != nil {
			if isNotFound(err) {
				// A directory with a lock but no record is an interrupted create.
				continue
			}
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// DecideRole is a caller's conditional write on a role: it is handed the role as
// it stands under the lock, and returns the event to append — or an error to
// abort with.
type DecideRole func(model.Role) (model.RoleEvent, error)

// ApplyRole is the only way to change a role.
//
// The lock is taken, the journal is replayed, the caller decides against *that*
// state, and the event is appended before the lock is released. The check and the
// write are never separated, which is what makes two concurrent `assign
// authority` calls resolve rather than interleave.
//
// The decided event is folded before it is written, so an event that would leave
// the role in an illegal state is refused rather than journaled: the journal only
// ever contains transitions that happened.
func (s *Store) ApplyRole(name model.Name, decide DecideRole) (model.Role, error) {
	if name.Zero() {
		return model.Role{}, fault.Internal{Where: "store.ApplyRole", Detail: "no role named"}
	}
	if decide == nil {
		return model.Role{}, fault.Internal{Where: "store.ApplyRole", Detail: "no decision given"}
	}

	var out model.Role
	err := s.withLock(s.roleDir(name), func() error {
		current, err := s.Role(name)
		if err != nil {
			return err
		}

		ev, err := decide(current)
		if err != nil {
			return err
		}
		if ev.Zero() {
			// A decision that produced no event is a no-op the caller has already
			// reported on — assigning a permission a role already holds, say.
			out = current
			return nil
		}

		next, err := current.With(ev)
		if err != nil {
			return err
		}
		line, err := encodeRoleEvent(ev)
		if err != nil {
			return err
		}
		if err := s.appendLine(filepath.Join(s.roleDir(name), journalFile), line); err != nil {
			return err
		}
		out = next
		return nil
	})
	if err != nil {
		return model.Role{}, err
	}
	return out, nil
}

// DeleteRole removes a role whole.
//
// Whether it is in use is the caller's question: the answer needs the derived
// fleet, and this package holds no opinion about policy.
func (s *Store) DeleteRole(name model.Name) error {
	if err := s.refuseWrite(); err != nil {
		return err
	}
	if name.Zero() {
		return fault.Internal{Where: "store.DeleteRole", Detail: "no role named"}
	}
	if _, err := s.loadRoleRecord(name); err != nil {
		return err
	}
	dir := s.roleDir(name)
	if err := s.ops.removeAll(dir); err != nil {
		return fault.IO{Op: "remove", Path: dir, Err: err}
	}
	s.ops.syncDir(filepath.Dir(dir))
	return nil
}

// isNotFound reports whether an error is a missing-thing fault.
func isNotFound(err error) bool {
	_, ok := err.(fault.NotFound)
	return ok
}
