package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
)

// How often a session that will not start is tried again.
//
// `tend` reconciles the worklist against what is running, and every command runs it
// (§6.4). That is the right design and it has one bad case: an identity that is
// employed and *cannot* start — a `claude` that is not installed, a workspace that
// has gone, a machine out of pty devices — is retried by every command anybody runs,
// and each attempt forks a supervisor that itself retries five times with backoff
// before giving up. A fleet with one broken agent becomes a fleet that spawns a
// doomed process every time somebody types `orc status`.
//
// So a failed start is remembered, and the next one waits. Not to give up: an agent
// that could not start because the laptop was asleep must come back on its own, and
// something that stopped trying would need a person to notice and intervene, which
// is the thing a backstop exists to avoid. The wait is capped, so the fleet keeps
// trying for as long as it stays employed — just at a pace a machine can carry.
//
// It is per identity and it lives beside the session, so it goes when the session
// directory does, and it is cleared the moment a start succeeds.

const attemptsFile = "starts.json"

// StartBackoff is the wait after each consecutive failure, and the last entry is
// the cap. Roughly: try again at once, then in seconds, then in minutes.
var StartBackoff = []time.Duration{
	0,
	5 * time.Second,
	15 * time.Second,
	time.Minute,
	5 * time.Minute,
	15 * time.Minute,
}

// StartAttempts is what has been tried, and when.
type StartAttempts struct {
	// Failures is how many starts in a row have failed.
	Failures int `json:"failures"`
	// At is when the last one did.
	At string `json:"at"`
	// Why is the last reason, so a caller holding off can say what it is waiting
	// out rather than only that it is waiting.
	Why string `json:"why,omitempty"`
}

func (s *Store) attemptsPath(name user.Name) string {
	return filepath.Join(s.SessionDir(name), attemptsFile)
}

// RecordFailedStart notes that a session would not start.
func (s *Store) RecordFailedStart(name user.Name, why string) error {
	if err := s.refuseWrite(); err != nil {
		return err
	}
	if name.Zero() {
		return fault.Internal{Where: "store.RecordFailedStart", Detail: "no identity named"}
	}

	got, _ := s.StartAttempts(name)
	got.Failures++
	got.At = clock.Format(s.Now())
	got.Why = trimReason(why)

	data, err := json.Marshal(got)
	if err != nil {
		return fault.Internal{Where: "store.RecordFailedStart", Detail: err.Error()}
	}
	if err := s.ops.mkdirAll(s.SessionDir(name), dirMode); err != nil {
		return fault.IO{Op: "create", Path: s.SessionDir(name), Err: err}
	}
	return s.writeFile(s.attemptsPath(name), append(data, '\n'))
}

// ClearFailedStarts forgets them, which is what a start that worked means.
func (s *Store) ClearFailedStarts(name user.Name) error {
	if err := s.refuseWrite(); err != nil {
		return err
	}
	if err := s.ops.remove(s.attemptsPath(name)); err != nil && !os.IsNotExist(err) {
		return fault.IO{Op: "remove", Path: s.attemptsPath(name), Err: err}
	}
	return nil
}

// StartAttempts reads the record. A missing or unreadable one is "nothing has
// failed", which is the answer that keeps a fleet trying: a backoff file that
// cannot be read must never be the reason an agent stays down.
func (s *Store) StartAttempts(name user.Name) (StartAttempts, bool) {
	data, err := s.ops.readFile(s.attemptsPath(name))
	if err != nil {
		return StartAttempts{}, false
	}
	var got StartAttempts
	if err := json.Unmarshal(data, &got); err != nil {
		return StartAttempts{}, false
	}
	return got, got.Failures > 0
}

// StartDue reports whether a start should be attempted now, and how long is left
// when it should not.
//
// A record whose timestamp will not parse is due: an unreadable clock is not a
// reason to leave an agent down.
func (s *Store) StartDue(name user.Name) (due bool, left time.Duration, got StartAttempts) {
	got, failed := s.StartAttempts(name)
	if !failed {
		return true, 0, got
	}
	at, err := clock.Parse(got.At)
	if err != nil {
		return true, 0, got
	}
	wait := StartBackoff[len(StartBackoff)-1]
	if got.Failures-1 < len(StartBackoff) {
		wait = StartBackoff[got.Failures-1]
	}
	// A record stamped in the future is a clock that moved. Waiting out an interval
	// measured against it could park an agent for hours, so it is treated as due
	// and the next failure re-stamps it.
	if at.After(s.Now()) {
		return true, 0, got
	}
	if left = wait - s.Now().Sub(at); left > 0 {
		return false, left, got
	}
	return true, 0, got
}

// trimReason keeps a failure short enough to sit in a record and on a line.
func trimReason(why string) string {
	why = strings.TrimSpace(strings.ReplaceAll(why, "\n", " "))
	if len(why) > 200 {
		return why[:200] + "…"
	}
	return why
}
