package store

import (
	"encoding/json"
	"os"
	"path/filepath"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
)

// What the wake cycle remembers between passes.
//
// `orc wake` pokes an agent that has gone quiet, and then must *not* poke it again
// until it has said something — an agent that is wedged needs reporting, not burying
// under nudges. That decision needs one fact: what the agent's last event was when it
// was last woken.
//
// It is on disk rather than in the waker because the cycle is not always one process.
// `orc wake --every 10m` keeps its own memory and was fine; `orc wake` from a cron
// entry, which is how most machines will run it, starts empty every time — so a
// wedged agent was poked afresh on every invocation, for ever, and never once
// reported as stuck. The two ways of running the same command behaved differently in
// the case the command exists for.
//
// Keyed by session, so a refresh clears it: a new session is a new conversation, and
// whatever the last one was stuck on is not its problem.
const wokenFile = "woken.json"

// WakeMark is the record of one wake.
type WakeMark struct {
	// Session is the session this is about. A mark from a previous session is not
	// stale data to be cleaned up — it is simply about something else, and is
	// ignored.
	Session string `json:"session"`
	// Mark is the identity's last event when it was woken, as the waker spells it.
	Mark string `json:"mark"`
	At   string `json:"at"`
}

func (s *Store) wakePath(name user.Name) string {
	return filepath.Join(s.SessionDir(name), wokenFile)
}

// Woken is what the last wake recorded for this session, if anything.
//
// Absent, unreadable, or belonging to another session all answer the same way: no
// mark. A wake cycle that refused to run because it could not read its own notes
// would be a backstop that stops working exactly when something else has gone wrong.
func (s *Store) Woken(name user.Name, session string) (string, bool) {
	if name.Zero() || session == "" {
		return "", false
	}

	data, err := s.ops.readFile(s.wakePath(name))
	if err != nil {
		return "", false
	}
	var got WakeMark
	if err := json.Unmarshal(data, &got); err != nil {
		return "", false
	}
	if got.Session != session {
		return "", false
	}
	return got.Mark, true
}

// RecordWake notes that an identity was poked, and what it had last said.
//
// A failure here is worth reporting but not worth stopping a cycle for: the poke has
// already happened, and the worst it costs is one extra nudge on the next pass.
func (s *Store) RecordWake(name user.Name, session, mark string) error {
	if err := s.refuseWrite(); err != nil {
		return err
	}
	if name.Zero() {
		return fault.Internal{Where: "store.RecordWake", Detail: "no identity named"}
	}
	if session == "" {
		return fault.Internal{Where: "store.RecordWake", Detail: "no session named"}
	}

	data, err := json.Marshal(WakeMark{Session: session, Mark: mark, At: clock.Format(s.Now())})
	if err != nil {
		return fault.Internal{Where: "store.RecordWake", Detail: err.Error()}
	}
	if err := s.ops.mkdirAll(s.SessionDir(name), dirMode); err != nil {
		return fault.IO{Op: "create", Path: s.SessionDir(name), Err: err}
	}
	return s.writeFile(s.wakePath(name), append(data, '\n'))
}

// ForgetWake drops the mark, so the next quiet spell is a new one.
//
// Called where a session ends. Not strictly needed — the mark is keyed by session
// and a new one ignores it — but a file that outlives what it describes is one
// somebody eventually reads as current.
func (s *Store) ForgetWake(name user.Name) error {
	if name.Zero() {
		return fault.Internal{Where: "store.ForgetWake", Detail: "no identity named"}
	}
	if err := s.ops.remove(s.wakePath(name)); err != nil && !os.IsNotExist(err) {
		return fault.IO{Op: "remove", Path: s.wakePath(name), Err: err}
	}
	return nil
}
