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
	"orc/orc/internal/model"
)

// permissionRecord is the on-disk shape of a permission.
//
// A permission is immutable — nothing in the CLI changes one after creation — so
// this record is the whole of it and there is no journal beside it. That is a
// simplification of Plan.md §3, which reserved a journal for every entity: with
// no command that mutates a permission, a journal would be a file that is always
// empty and a fold that can never run. Widening a permission is creating another
// one, which is a change that shows up in every card that lists it.
type permissionRecord struct {
	Version  int      `json:"version"`
	Name     string   `json:"name"`
	Floor    int      `json:"floor"`
	Patterns []string `json:"patterns"`
	Created  string   `json:"created"`
}

// CreatePermission writes a permission's record.
//
// The rename is what makes the name unique, so two agents creating the same
// permission at the same instant cannot both succeed.
func (s *Store) CreatePermission(name model.Name, floor model.Authority, patterns []model.Pattern) (model.Permission, error) {
	p, err := model.NewPermission(name, floor, patterns, s.clock.Now())
	if err != nil {
		return model.Permission{}, err
	}
	data, err := json.MarshalIndent(permissionRecord{
		Version:  Version,
		Name:     p.Name().String(),
		Floor:    p.Floor().Int(),
		Patterns: model.PatternStrings(p.Patterns()),
		Created:  clock.Format(p.Created()),
	}, "", "  ")
	if err != nil {
		return model.Permission{}, fault.Internal{Where: "store.CreatePermission", Detail: err.Error()}
	}

	path := s.permissionPath(name)
	if err := s.writeNew(path, append(data, '\n')); err != nil {
		return model.Permission{}, err
	}
	// Read back before reporting success: a record that cannot be decoded is a
	// permission that has been lost, and it is cheaper to find that out here than
	// on the next command.
	return s.Permission(name)
}

// Permission reads one permission.
func (s *Store) Permission(name model.Name) (model.Permission, error) {
	if name.Zero() {
		return model.Permission{}, fault.Internal{Where: "store.Permission", Detail: "no permission named"}
	}
	path := s.permissionPath(name)

	data, err := s.ops.readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return model.Permission{}, fault.NotFound{Target: "permission " + name.String()}
		}
		return model.Permission{}, fault.IO{Op: "read", Path: path, Err: err}
	}
	return decodePermission(path, name, data)
}

func decodePermission(path string, want model.Name, data []byte) (model.Permission, error) {
	if len(data) > MaxRecordSize {
		return model.Permission{}, fault.Parse{Path: path, Reason: fmt.Sprintf(
			"permission record is %d bytes, limit is %d", len(data), MaxRecordSize)}
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var r permissionRecord
	if err := dec.Decode(&r); err != nil {
		return model.Permission{}, fault.Parse{Path: path, Reason: "permission record: " + err.Error()}
	}
	if dec.More() {
		return model.Permission{}, fault.Parse{Path: path, Reason: "permission record has trailing content"}
	}
	if r.Version != Version {
		return model.Permission{}, fault.Parse{Path: path, Reason: fmt.Sprintf(
			"permission record is version %d, this orc writes version %d", r.Version, Version)}
	}

	name, err := model.ParseName(r.Name)
	if err != nil {
		return model.Permission{}, fault.Parse{Path: path, Reason: "permission record name: " + err.Error()}
	}
	// The filename states an identity and so does the content. A disagreement
	// means the store was hand-edited or a file was copied, and either way the
	// content must not answer for a name it is not.
	if !want.Zero() && !name.Equal(want) {
		return model.Permission{}, fault.Conflict{Path: path, Reason: fmt.Sprintf(
			"file is named for %s but the record says %s", want, name)}
	}
	floor, err := model.NewAuthority(r.Floor)
	if err != nil {
		return model.Permission{}, fault.Parse{Path: path, Reason: "permission record floor: " + err.Error()}
	}
	patterns, err := model.ParsePatterns(r.Patterns)
	if err != nil {
		return model.Permission{}, fault.Parse{Path: path, Reason: "permission record patterns: " + err.Error()}
	}
	created, err := clock.Parse(r.Created)
	if err != nil {
		return model.Permission{}, fault.Parse{Path: path, Reason: "permission record created: " + err.Error()}
	}

	p, err := model.NewPermission(name, floor, patterns, created)
	if err != nil {
		return model.Permission{}, fault.Parse{Path: path, Reason: "permission record is invalid: " + err.Error()}
	}
	return p, nil
}

// Permissions lists every permission, in name order.
func (s *Store) Permissions() ([]model.Permission, error) {
	files, err := s.names(filepath.Join(s.root, permissionsDir), MaxPermissions, "permissions")
	if err != nil {
		return nil, err
	}

	out := make([]model.Permission, 0, len(files))
	for _, file := range files {
		base, ok := strings.CutSuffix(file, ".json")
		if !ok {
			// Not written by Orc. Skipping is right; `verify` is what reports it.
			continue
		}
		name, err := model.ParseName(base)
		if err != nil {
			continue
		}
		p, err := s.Permission(name)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// DeletePermission removes a permission's record.
//
// Whether it is in use is the caller's question, not this one's: the answer needs
// the derived fleet, and the store deliberately holds no opinion about policy.
func (s *Store) DeletePermission(name model.Name) error {
	if err := s.refuseWrite(); err != nil {
		return err
	}
	if name.Zero() {
		return fault.Internal{Where: "store.DeletePermission", Detail: "no permission named"}
	}
	path := s.permissionPath(name)
	if _, err := s.ops.stat(path); err != nil {
		if os.IsNotExist(err) {
			return fault.NotFound{Target: "permission " + name.String()}
		}
		return fault.IO{Op: "check for", Path: path, Err: err}
	}
	if err := s.ops.remove(path); err != nil {
		return fault.IO{Op: "remove", Path: path, Err: err}
	}
	s.ops.syncDir(filepath.Dir(path))
	return nil
}
