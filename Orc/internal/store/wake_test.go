package store_test

import (
	"os"
	"path/filepath"
	"testing"
)

// The wake cycle's memory between passes.
//
// `orc wake` pokes an agent that has gone quiet and must not poke it again until it
// has said something — a wedged agent needs reporting, not burying under nudges.
// That memory used to live only in the running process, so `orc wake --every` was
// correct and `orc wake` from a cron entry, which is how most machines run it,
// started empty every time and re-poked the same agent for ever.

func TestAWakeMarkSurvivesTheProcessThatWroteIt(t *testing.T) {
	s, _ := fresh(t)
	ember := mustUser(t, "ember")

	// Nothing recorded is not an error: most agents have never been woken.
	if _, ok := s.Woken(ember, "session-one"); ok {
		t.Fatal("a fresh store remembered a wake")
	}

	if err := s.RecordWake(ember, "session-one", "2026-07-26T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Woken(ember, "session-one")
	if !ok || got != "2026-07-26T10:00:00Z" {
		t.Errorf("read back %q (ok=%v), want the mark", got, ok)
	}
}

// TestAMarkBelongsToOneSession. A refresh is a new conversation, and whatever the
// last one was stuck on is not its problem — so the mark must not carry over, or the
// first quiet spell of a fresh session would read as "still silent" and never be
// woken.
func TestAWakeMarkIsScopedToItsSession(t *testing.T) {
	s, _ := fresh(t)
	ember := mustUser(t, "ember")

	if err := s.RecordWake(ember, "session-one", "a mark"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Woken(ember, "session-two"); ok {
		t.Error("a new session inherited the previous session's wake mark")
	}
	// And the old session's is still its own, so a mark is not lost by being asked
	// about with the wrong id.
	if _, ok := s.Woken(ember, "session-one"); !ok {
		t.Error("asking about another session destroyed the mark")
	}
}

// A cycle that refused to run because it could not read its own notes would be a
// backstop that stops working exactly when something else has gone wrong.
func TestAnUnreadableMarkReadsAsNoMark(t *testing.T) {
	s, root := fresh(t)
	ember := mustUser(t, "ember")

	dir := filepath.Join(root, "identities", "ember", "session")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "woken.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, ok := s.Woken(ember, "session-one"); ok {
		t.Error("a corrupt mark was believed")
	}
	// And it can be written over, so the fleet repairs itself rather than needing
	// somebody to delete a file.
	if err := s.RecordWake(ember, "session-one", "a mark"); err != nil {
		t.Errorf("a corrupt mark could not be replaced: %v", err)
	}
	if _, ok := s.Woken(ember, "session-one"); !ok {
		t.Error("the replacement did not take")
	}
}

func TestForgettingAWake(t *testing.T) {
	s, _ := fresh(t)
	ember := mustUser(t, "ember")

	if err := s.RecordWake(ember, "session-one", "a mark"); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetWake(ember); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Woken(ember, "session-one"); ok {
		t.Error("the mark outlived being forgotten")
	}
	// Forgetting what is not there satisfies the caller either way.
	if err := s.ForgetWake(ember); err != nil {
		t.Errorf("forgetting an absent mark: %v", err)
	}
}
