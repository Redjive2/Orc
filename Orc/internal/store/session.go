package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/model"
	"orc/orc/internal/proc"
)

// Session state is the one part of this store that is *not* a journal, and the
// difference is the design in Plan.md §6: employment is a decision and belongs in
// history, while having a live session is a fact about a process. A crash must be
// able to take a session away without rewriting anything, so session.json is
// written whole, replaced whole, and removed when the process it describes exits.
//
// Which means: **the file's existence is a claim, not a fact.** A supervisor killed
// with SIGKILL leaves one behind. So every read checks the process is really there
// (§Session), and nothing above here treats the file as evidence on its own.

const (
	sessionFile = "session.json"
	socketFile  = "session.sock"
	sessionLock = "lock"
	sessionLog  = "log.jsonl"
)

// SessionState is what session.json holds.
type SessionState struct {
	Identity string `json:"identity"`
	// ID is the Claude session id Orc minted, which is what makes `--resume`
	// deterministic and what a session-scoped grant is tied to.
	ID string `json:"id"`
	// Supervisor is the pid of the orc-session process holding the pty. Child is
	// the claude process itself, and changes on every restart.
	Supervisor int `json:"supervisor"`
	Child      int `json:"child"`

	Model  string `json:"model"`
	Effort string `json:"effort"`

	Started  string `json:"started"`
	Restarts int    `json:"restarts"`
	// LastExit describes why the child last went away, when it has. It is text
	// rather than a code because "signal: killed" and "exit status 1" are different
	// things a reader wants to tell apart.
	LastExit string `json:"last_exit,omitempty"`
	Socket   string `json:"socket"`
}

// Load returns what this session costs, from the model and effort it is running.
func (s SessionState) Load() (int, error) {
	m, err := model.ParseModel(s.Model)
	if err != nil {
		return 0, err
	}
	e, err := model.ParseEffort(s.Effort)
	if err != nil {
		return 0, err
	}
	return model.SessionLoad(m, e), nil
}

// StartedAt parses the start time, so a screen can say how long a session has been
// up without every caller re-parsing the field.
func (s SessionState) StartedAt() (time.Time, error) { return clock.Parse(s.Started) }

// SessionPath, SocketPath, and SessionLogPath name the files a live session owns.
// They are exported because internal/session writes and reads them, and a second
// package deriving these paths from the root would be a second definition of the
// layout.

func (s *Store) SessionPath(name user.Name) string {
	return filepath.Join(s.SessionDir(name), sessionFile)
}

// MaxSocketPath is the longest a unix socket path may be.
//
// The kernel's limit is the size of sun_path: 104 bytes on darwin, 108 on linux,
// and a bind past it fails with EINVAL — which is one of the least helpful errors
// in the system, because nothing in it says the path was too long. This is under
// both, with room for the terminating NUL.
const MaxSocketPath = 100

// SocketPath is where a session's socket lives.
//
// Beside the session's state when it fits, which is the common case and the one an
// operator can find. When it does not fit — a deep ORC_HOME, a long identity name,
// a temporary directory in a test — it falls back to a short path derived from the
// store root and the identity, and **session.json records which one was used**, so
// no client ever has to guess.
//
// The fallback puts a socket outside the store, which is worth being deliberate
// about: a socket is liveness rather than state, it is recreated on every populate,
// and the hash of the store root keeps two fleets — or a fleet and an Orcprobe copy
// of it — from ever sharing one.
func (s *Store) SocketPath(name user.Name) string {
	direct := filepath.Join(s.SessionDir(name), socketFile)
	if len(direct) <= MaxSocketPath {
		return direct
	}
	return s.shortSocketPath(name)
}

// shortSocketPath is the fallback: /tmp/orc-<uid>/<hash>.sock.
//
// /tmp rather than os.TempDir(), because on darwin TempDir is itself a path long
// enough to have caused this problem in the first place. The directory is per-user
// and 0700, and the socket inside it is 0600, so it is as private as the keyring —
// a socket is a way to type into somebody's agent.
func (s *Store) shortSocketPath(name user.Name) string {
	sum := sha256.Sum256([]byte(s.root + "\x00" + name.String()))
	return filepath.Join("/tmp", fmt.Sprintf("orc-%d", os.Getuid()),
		hex.EncodeToString(sum[:6])+".sock")
}

func (s *Store) SessionLogPath(name user.Name) string {
	return filepath.Join(s.SessionDir(name), sessionLog)
}

// WriteSession records a live session.
//
// It is written *after* the child is up, so the file's presence means a process
// exists rather than that one was intended — the ordering is what makes a
// half-started session invisible instead of misleading.
func (s *Store) WriteSession(name user.Name, state SessionState) error {
	if err := s.refuseWrite(); err != nil {
		return err
	}
	if name.Zero() {
		return fault.Internal{Where: "store.WriteSession", Detail: "no identity named"}
	}
	if state.ID == "" || state.Supervisor <= 0 {
		return fault.Internal{Where: "store.WriteSession", Detail: "a session needs an id and a supervisor pid"}
	}

	state.Identity = name.String()
	state.Socket = s.SocketPath(name)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fault.Internal{Where: "store.WriteSession", Detail: err.Error()}
	}
	return s.writeFile(s.SessionPath(name), append(data, '\n'))
}

// Session reads a live session, reporting whether there is one.
//
// A state file whose supervisor is no longer running is **not** a session: it is a
// leftover from a process that was killed, and reporting it as live would make
// `orc tend` refuse to restart the very thing it exists to restart. The check is a
// signal 0, which asks the kernel whether the pid exists without disturbing it.
func (s *Store) Session(name user.Name) (SessionState, bool, error) {
	if name.Zero() {
		return SessionState{}, false, fault.Internal{Where: "store.Session", Detail: "no identity named"}
	}
	path := s.SessionPath(name)

	data, err := s.ops.readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return SessionState{}, false, nil
		}
		return SessionState{}, false, fault.IO{Op: "read", Path: path, Err: err}
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var state SessionState
	if err := dec.Decode(&state); err != nil {
		return SessionState{}, false, fault.Parse{Path: path, Reason: "session state: " + err.Error()}
	}
	if state.Identity != name.String() {
		return SessionState{}, false, fault.Conflict{Path: path, Reason: fmt.Sprintf(
			"session state names %s but lives in %s's directory", state.Identity, name)}
	}

	if !proc.Alive(state.Supervisor) {
		return state, false, nil
	}
	return state, true, nil
}

// RemoveSession deletes the state file and the socket.
//
// Both go together: a socket with no state behind it is something an attach would
// connect to and then wait on forever, which is a worse failure than a refusal.
func (s *Store) RemoveSession(name user.Name) error {
	if err := s.refuseWrite(); err != nil {
		return err
	}
	for _, path := range []string{s.SessionPath(name), s.SocketPath(name)} {
		if err := s.ops.remove(path); err != nil && !os.IsNotExist(err) {
			return fault.IO{Op: "remove", Path: path, Err: err}
		}
	}
	s.ops.syncDir(s.SessionDir(name))
	return nil
}

// Sessions reports each identity's current session id, for the derivation.
//
// This is what a session-scoped grant asks in order to know whether it has lapsed,
// and it uses the same liveness rule as Session: a dead supervisor's leftover state
// is not a session, so a grant tied to it has already gone. That is the correct
// answer and not a technicality — the session it was scoped to is not running.
func (s *Store) Sessions() (map[string]string, error) {
	identities, err := s.Identities()
	if err != nil {
		return nil, err
	}

	out := make(map[string]string, len(identities))
	for _, i := range identities {
		state, live, err := s.Session(i.Name())
		if err != nil {
			// A session file that will not parse must not stop the fleet from being
			// derived: the identity is simply not populated as far as anybody can
			// tell, and `orc verify` is what reports the damage.
			out[i.Name().String()] = ""
			continue
		}
		if live {
			out[i.Name().String()] = state.ID
			continue
		}
		out[i.Name().String()] = ""
	}
	return out, nil
}

// SessionEvent is one line of a session's own log: what the supervisor did, and
// how it turned out.
type SessionEvent struct {
	At     string `json:"at"`
	Op     string `json:"op"`
	ID     string `json:"id,omitempty"`
	Child  int    `json:"child,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// AppendSessionEvent records what a supervisor did.
//
// The log is append-only and never read back by anything that makes decisions —
// it is for a person asking why an agent keeps dying. That is why it tolerates its
// own failure: a supervisor that could not write its log still has a session to
// run, and stopping to complain would be the wrong priority.
func (s *Store) AppendSessionEvent(name user.Name, ev SessionEvent) error {
	if err := s.refuseWrite(); err != nil {
		return err
	}
	if ev.At == "" {
		ev.At = clock.Format(s.clock.Now())
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return fault.Internal{Where: "store.AppendSessionEvent", Detail: err.Error()}
	}
	return s.appendLine(s.SessionLogPath(name), line)
}

// HoldSession takes the session lock for as long as the returned release is not
// called, and reports whether it got it.
//
// This is what makes one supervisor per identity a fact rather than a convention:
// a second one starts, fails to take the lock, and exits. flock is the right
// primitive because the holder is a long-lived process that may be killed — the
// lock goes when its descriptors close, so there is no stale lock to reap by
// guessing whether its owner is still alive.
func (s *Store) HoldSession(name user.Name) (release func(), held bool, err error) {
	if err := s.refuseWrite(); err != nil {
		return nil, false, err
	}
	dir := s.SessionDir(name)
	if err := s.ops.mkdirAll(dir, dirMode); err != nil {
		return nil, false, fault.IO{Op: "create", Path: dir, Err: err}
	}

	path := filepath.Join(dir, sessionLock)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, fileMode)
	if err != nil {
		return nil, false, fault.IO{Op: "open the lock at", Path: path, Err: err}
	}

	got, err := tryLockFileHandle(f)
	if err != nil {
		_ = f.Close()
		return nil, false, fault.IO{Op: "lock", Path: path, Err: err}
	}
	if !got {
		_ = f.Close()
		return nil, false, nil
	}
	return func() {
		_ = unlockFileHandle(f)
		_ = f.Close()
	}, true, nil
}
