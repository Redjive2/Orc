package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"orc/orc/internal/store"
)

// What is remembered about a session that has ended.
//
// A session's state file describes one that is *running*, and the supervisor removes
// it on the way out — so everything about the conversation used to go with it, and
// `orc tend` started a blank one in its place. An agent that stopped because of
// something outside itself came back an hour later having never heard of the work it
// was part-way through.

func TestAnEndingIsRememberedSoItCanBeResumed(t *testing.T) {
	s, _ := fresh(t)
	ember := mustUser(t, "ember")

	if _, ok := s.LastEnded(ember); ok {
		t.Fatal("a fresh store remembered an ending")
	}

	if err := s.RecordEnded(ember, store.Ended{
		Session: "session-one", Why: "signal: killed", MidTurn: true, Restarts: 5,
	}); err != nil {
		t.Fatal(err)
	}

	got, ok := s.LastEnded(ember)
	if !ok {
		t.Fatal("the ending was not remembered")
	}
	if got.Session != "session-one" {
		t.Errorf("the session to resume is %q", got.Session)
	}
	if !got.MidTurn {
		t.Error("it does not remember that the session went mid-turn")
	}
	if got.Restarts != 5 {
		t.Errorf("restarts = %d, want 5", got.Restarts)
	}
	// Stamped, because "it ended" without "when" cannot be told from a record left
	// over from last week.
	if got.At == "" {
		t.Error("the ending has no time on it")
	}
}

// TestADeliberateEndIsNotResurrected. `orc refresh` and `orc fire` are somebody
// saying the conversation is over; a backstop that resumed it anyway would be Orc
// overruling the operator.
func TestForgettingAnEnding(t *testing.T) {
	s, _ := fresh(t)
	ember := mustUser(t, "ember")

	if err := s.RecordEnded(ember, store.Ended{Session: "session-one"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetEnded(ember); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.LastEnded(ember); ok {
		t.Error("a deliberately ended session is still remembered")
	}
	// Forgetting what is not there satisfies the caller either way.
	if err := s.ForgetEnded(ember); err != nil {
		t.Errorf("forgetting an absent ending: %v", err)
	}
}

// A recovery that refused to run because it could not read its own notes would fail
// exactly when something else already has. An unreadable record means "nothing is
// remembered", which is what the caller would have assumed anyway.
func TestAnUnreadableEndingReadsAsNone(t *testing.T) {
	s, root := fresh(t)
	ember := mustUser(t, "ember")

	dir := filepath.Join(root, "identities", "ember", "session")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ended.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, ok := s.LastEnded(ember); ok {
		t.Error("a corrupt record was believed")
	}
	if err := s.RecordEnded(ember, store.Ended{Session: "session-two"}); err != nil {
		t.Errorf("a corrupt record could not be replaced: %v", err)
	}
	if got, ok := s.LastEnded(ember); !ok || got.Session != "session-two" {
		t.Error("the replacement did not take")
	}
}

// A record with no session names nothing to resume, so it is not a record.
func TestAnEndingNeedsASession(t *testing.T) {
	s, _ := fresh(t)
	if err := s.RecordEnded(mustUser(t, "ember"), store.Ended{Why: "gone"}); err == nil {
		t.Error("an ending with no session was recorded")
	}
}
