package store_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"orc/common/clock"
	"orc/common/user"
	"orc/orc/internal/store"
)

// A supervisor tidies up after itself and never after its successor.
//
// This is the shape behind "`orc refresh` ends the session and does not start
// one". A supervisor that gave up after its restarts, or that is simply slow to
// unwind, exits *after* the replacement has started. Removing the state file by
// identity alone deleted the replacement's, which leaves the one state a fleet
// cannot recover from on its own: a live supervisor holding the session lock, and
// no state file saying anybody is there.
//
// From outside it reads as a tool that stops sessions and cannot start them —
// `status` says employed and not running, `tend` and `refresh` find nothing to
// stop, and every replacement they start is refused the lock by the supervisor
// still alive.

func newStore(t *testing.T) (*store.Store, user.Name) {
	t.Helper()

	root, err := os.MkdirTemp("/tmp", "orc")
	if err != nil {
		t.Fatalf("temp root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	s, err := store.Create(filepath.Join(root, "f"),
		clock.NewFake(time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC), time.Second))
	if err != nil {
		t.Fatalf("creating the store: %v", err)
	}
	who, err := user.Parse("ember")
	if err != nil {
		t.Fatal(err)
	}
	return s, who
}

// live writes a session state naming this process, so it reads as running.
func live(t *testing.T, s *store.Store, who user.Name, id string) {
	t.Helper()
	if err := s.WriteSession(who, store.SessionState{
		Identity:   who.String(),
		ID:         id,
		Supervisor: os.Getpid(),
		Child:      os.Getpid(),
		Model:      "sonnet",
		Effort:     "medium",
		Started:    "2026-07-26T12:00:00.000Z",
		Socket:     filepath.Join(s.SessionDir(who), "session.sock"),
	}); err != nil {
		t.Fatalf("writing the session: %v", err)
	}
}

func TestAnOldSupervisorDoesNotRemoveANewSession(t *testing.T) {
	s, who := newStore(t)
	live(t, s, who, "new-session")

	// The old supervisor exits and cleans up, naming the session it was running.
	if err := s.RemoveOwnSession(who, "old-session"); err != nil {
		t.Fatalf("cleaning up: %v", err)
	}

	state, running, err := s.Session(who)
	if err != nil {
		t.Fatalf("reading the session: %v", err)
	}
	if !running {
		t.Fatal("an old supervisor's cleanup deleted the session that replaced it")
	}
	if state.ID != "new-session" {
		t.Errorf("the session is %q, want the new one", state.ID)
	}
}

func TestASupervisorRemovesItsOwnSession(t *testing.T) {
	s, who := newStore(t)
	live(t, s, who, "mine")

	if err := s.RemoveOwnSession(who, "mine"); err != nil {
		t.Fatalf("cleaning up: %v", err)
	}
	if _, running, err := s.Session(who); err != nil || running {
		t.Errorf("a supervisor left its own session behind (running=%v, err=%v)", running, err)
	}
}

// Nothing to remove is not a failure: `fire` and `tend` both call the unconditional
// form to make a fact true, and a supervisor that crashed may have had its state
// tidied away before it unwound.
func TestRemovingASessionThatIsNotThereIsQuiet(t *testing.T) {
	s, who := newStore(t)
	if err := s.RemoveOwnSession(who, "mine"); err != nil {
		t.Errorf("removing nothing: %v", err)
	}
	if err := s.RemoveSession(who); err != nil {
		t.Errorf("removing nothing: %v", err)
	}
}

func TestRemovingWithNoSessionIdIsADefect(t *testing.T) {
	s, who := newStore(t)
	live(t, s, who, "mine")
	if err := s.RemoveOwnSession(who, ""); err == nil {
		t.Error("a cleanup with no session id was accepted")
	}
	if _, running, _ := s.Session(who); !running {
		t.Error("the session went anyway")
	}
}
