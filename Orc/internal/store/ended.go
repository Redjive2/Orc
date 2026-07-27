package store

import (
	"encoding/json"
	"os"
	"path/filepath"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
)

// What is remembered about the session that just ended.
//
// A session's state file describes a session that is *running*; when the supervisor
// gives up it removes that file, and until now everything about the conversation went
// with it. `orc tend` then found an employed identity with no session and started a
// **new** one — a fresh conversation with no memory of what the agent had been doing.
//
// That is the wrong recovery for the commonest way a session dies. An agent does not
// usually stop because its conversation is broken; it stops because something outside
// it went wrong for a while — a usage limit reached mid-turn, a network that came and
// went, a machine that slept. The limit lifts an hour later, `tend` brings the agent
// back, and the operator finds it blank and mid-nothing, having lost the work it was
// part-way through. From outside that is an agent that "did not resume properly".
//
// So the ending is written down before the state file goes, and the recovery resumes
// what was there rather than replacing it.
const endedFile = "ended.json"

// Ended is the record of a session that has finished.
type Ended struct {
	// Session is the id to resume. It is the whole point of this file.
	Session string `json:"session"`
	// At is when the supervisor noticed.
	At string `json:"at"`
	// Why is the exit as the supervisor saw it — "signal: killed", "exit status 1",
	// or the empty string for an orderly end.
	Why string `json:"why,omitempty"`
	// MidTurn says the session went while it was working rather than while it was
	// waiting for somebody.
	//
	// This is the distinction that matters for what to do next. A session that ended
	// *waiting* had finished its turn: resuming it is enough, and it will sit at its
	// prompt until something asks for more. A session that ended **mid-call** was
	// part-way through a turn nobody will finish — the model call it was in never
	// came back — so resuming it alone leaves an agent sitting silently on an
	// unfinished thought, which is exactly what it looks like from outside when a
	// fleet "stops and does not come back".
	MidTurn bool `json:"mid_turn,omitempty"`
	// Restarts is how many times the supervisor had tried before it gave up. A
	// session that ended after five failed restarts is a different thing from one
	// that ended once, and the difference is worth keeping.
	Restarts int `json:"restarts,omitempty"`
}

func (s *Store) endedPath(name user.Name) string {
	return filepath.Join(s.SessionDir(name), endedFile)
}

// LastEnded is what became of the previous session, if anything is remembered.
//
// Absent or unreadable both answer "nothing is remembered", because the caller's
// alternative is to start a fresh session, which is what it would have done anyway.
// A recovery that refused to run because it could not read its own notes would fail
// exactly when something else already has.
func (s *Store) LastEnded(name user.Name) (Ended, bool) {
	if name.Zero() {
		return Ended{}, false
	}
	data, err := s.ops.readFile(s.endedPath(name))
	if err != nil {
		return Ended{}, false
	}
	var got Ended
	if err := json.Unmarshal(data, &got); err != nil {
		return Ended{}, false
	}
	if got.Session == "" {
		return Ended{}, false
	}
	return got, true
}

// RecordEnded notes how a session finished, so the next one can continue it.
func (s *Store) RecordEnded(name user.Name, got Ended) error {
	if err := s.refuseWrite(); err != nil {
		return err
	}
	if name.Zero() {
		return fault.Internal{Where: "store.RecordEnded", Detail: "no identity named"}
	}
	if got.Session == "" {
		return fault.Internal{Where: "store.RecordEnded", Detail: "no session named"}
	}
	if got.At == "" {
		got.At = clock.Format(s.Now())
	}

	data, err := json.Marshal(got)
	if err != nil {
		return fault.Internal{Where: "store.RecordEnded", Detail: err.Error()}
	}
	if err := s.ops.mkdirAll(s.SessionDir(name), dirMode); err != nil {
		return fault.IO{Op: "create", Path: s.SessionDir(name), Err: err}
	}
	return s.writeFile(s.endedPath(name), append(data, '\n'))
}

// ForgetEnded drops the record.
//
// Called when a session is deliberately replaced — `orc refresh`, or a fire — where
// the operator has said the old conversation is not wanted. Recovery must not quietly
// resurrect what somebody chose to end.
func (s *Store) ForgetEnded(name user.Name) error {
	if name.Zero() {
		return fault.Internal{Where: "store.ForgetEnded", Detail: "no identity named"}
	}
	if err := s.ops.remove(s.endedPath(name)); err != nil && !os.IsNotExist(err) {
		return fault.IO{Op: "remove", Path: s.endedPath(name), Err: err}
	}
	return nil
}
