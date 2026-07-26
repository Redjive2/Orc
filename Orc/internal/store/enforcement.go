package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/authz"
	"orc/orc/internal/event"
	"orc/orc/internal/model"
)

// The enforcement layer's two session-scoped files: the permission snapshot the hook
// falls back to, and the event feed the clean view reads.
//
// Both are session-scoped rather than part of the identity's journal, and for the
// same reason session.json is: a snapshot describes the permissions one session
// started with, and a feed describes what that session did. A refresh replaces both.
//
// Only the snapshot is written through this package. The feed is appended to by
// `orc-hook`, through event.Append, which takes a path — deliberately, so that the
// read-only door this store offers stays honest. The hook opens the store read-only,
// and a read-only store that could still write *something* would be a guarantee with
// an exception in it.

const authzFile = "authz.json"

// AuthzPath is where a session's permission snapshot lives.
func (s *Store) AuthzPath(name user.Name) string {
	return filepath.Join(s.SessionDir(name), authzFile)
}

// EventsPath is where a session's event feed lives.
func (s *Store) EventsPath(name user.Name) string {
	return filepath.Join(s.SessionDir(name), event.EventFile)
}

// AuthzClause is one effective permission clause, as the snapshot stores it.
type AuthzClause struct {
	Kind string `json:"kind"`
	Arg  string `json:"arg"`
}

// AuthzSnapshot is what a session was allowed when it started.
//
// It is the middle rung of the hook's fallback ladder (Plan.md §7.3): when the live
// store cannot be read, the honest answer is what this identity was allowed when its
// session began. That is why it is one small file with no lock and no journal to
// replay — a fallback that needed either would not be a fallback.
//
// It holds no grant expiry logic and no credential. Grants are already resolved into
// the clauses by the derivation, so a snapshot cannot outlive a grant it recorded any
// more than the session can.
type AuthzSnapshot struct {
	Identity string        `json:"identity"`
	Session  string        `json:"session"`
	At       string        `json:"at"`
	Clauses  []AuthzClause `json:"clauses"`
	Budget   int           `json:"budget"`
}

// Freeze turns an identity's derived clauses into a snapshot.
func Freeze(name user.Name, session string, at string, clauses []authz.Clause, budget int) AuthzSnapshot {
	out := AuthzSnapshot{
		Identity: name.String(),
		Session:  session,
		At:       at,
		Clauses:  make([]AuthzClause, 0, len(clauses)),
		Budget:   budget,
	}
	for _, c := range clauses {
		out.Clauses = append(out.Clauses, AuthzClause{
			Kind: c.Pattern.Kind().String(),
			Arg:  c.Pattern.Arg(),
		})
	}
	return out
}

// Patterns rebuilds the snapshot's clauses as patterns, so the hook matches against
// exactly the same code the live path does.
//
// A clause that will not parse is dropped rather than failing the whole snapshot, and
// the count of dropped clauses is returned: a hook deciding from a partly-readable
// snapshot is on the second rung of the ladder already, and the honest thing is to
// enforce what it could read and say how much it could not.
func (a AuthzSnapshot) Patterns() (patterns []model.Pattern, dropped int) {
	for _, c := range a.Clauses {
		p, err := model.ParsePattern(c.Kind + "(" + c.Arg + ")")
		if err != nil {
			dropped++
			continue
		}
		patterns = append(patterns, p)
	}
	return patterns, dropped
}

// WriteAuthz records the permissions a session is starting with.
//
// Written once when a session is prepared and never afterwards. A restart keeps it,
// because a restart continues the same session; a refresh replaces it, because that
// is a new one.
func (s *Store) WriteAuthz(name user.Name, snapshot AuthzSnapshot) error {
	if err := s.refuseWrite(); err != nil {
		return err
	}
	if name.Zero() {
		return fault.Internal{Where: "store.WriteAuthz", Detail: "no identity named"}
	}
	if snapshot.Session == "" {
		return fault.Internal{Where: "store.WriteAuthz", Detail: "a snapshot needs a session id"}
	}
	snapshot.Identity = name.String()
	if snapshot.At == "" {
		snapshot.At = clock.Format(s.clock.Now())
	}

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fault.Internal{Where: "store.WriteAuthz", Detail: err.Error()}
	}
	return s.writeFile(s.AuthzPath(name), append(data, '\n'))
}

// ReadAuthz reads the snapshot, reporting whether there is one.
//
// A missing snapshot is not an error: it is the third rung of the ladder, where reads
// pass and writes block. A snapshot that will not parse *is* an error, and the caller
// treats it as the same rung — a file that cannot be read is not a permission set.
func (s *Store) ReadAuthz(name user.Name) (AuthzSnapshot, bool, error) {
	if name.Zero() {
		return AuthzSnapshot{}, false, fault.Internal{Where: "store.ReadAuthz", Detail: "no identity named"}
	}
	path := s.AuthzPath(name)

	data, err := s.ops.readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return AuthzSnapshot{}, false, nil
		}
		return AuthzSnapshot{}, false, fault.IO{Op: "read", Path: path, Err: err}
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var snapshot AuthzSnapshot
	if err := dec.Decode(&snapshot); err != nil {
		return AuthzSnapshot{}, false, fault.Parse{Path: path, Reason: "permission snapshot: " + err.Error()}
	}
	if snapshot.Identity != name.String() {
		return AuthzSnapshot{}, false, fault.Conflict{Path: path, Reason: fmt.Sprintf(
			"snapshot names %s but lives in %s's directory", snapshot.Identity, name)}
	}
	return snapshot, true, nil
}
