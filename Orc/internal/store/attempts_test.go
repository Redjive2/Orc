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

// Pacing a session that will not start.
//
// The rule this has to keep is "keeps trying, forever, without hammering". Both
// halves matter and they pull in opposite directions: a fleet that gave up needs a
// person to notice, and a fleet that retries on every command forks a doomed
// supervisor every time somebody types `orc status`.

// paced builds a store whose clock jumps `step` on every reading, which is how a
// test decides how much time has passed between one call and the next.
func paced(t *testing.T, step time.Duration) (*store.Store, user.Name) {
	t.Helper()

	root, err := os.MkdirTemp("/tmp", "orc")
	if err != nil {
		t.Fatalf("temp root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	s, err := store.Create(filepath.Join(root, "f"),
		clock.NewFake(time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC), step))
	if err != nil {
		t.Fatalf("creating the store: %v", err)
	}
	who, err := user.Parse("ember")
	if err != nil {
		t.Fatal(err)
	}
	return s, who
}

func TestAStartIsDueWhenNothingHasFailed(t *testing.T) {
	s, who := paced(t, time.Millisecond)
	if due, left, got := s.StartDue(who); !due || left != 0 || got.Failures != 0 {
		t.Errorf("a fleet with no failures is not due: due=%v left=%v failures=%d", due, left, got.Failures)
	}
}

// The first failure costs nothing. Most of them are a moment of a busy machine, and
// making the common case wait would be pacing against a problem that is already over.
func TestTheFirstRetryIsImmediate(t *testing.T) {
	s, who := paced(t, time.Millisecond)
	if err := s.RecordFailedStart(who, "out of pty devices"); err != nil {
		t.Fatalf("recording: %v", err)
	}
	if due, left, _ := s.StartDue(who); !due {
		t.Errorf("the first retry waited %s; one failure is not a pattern", left)
	}
}

func TestAFailedStartWaitsBeforeTheNext(t *testing.T) {
	s, who := paced(t, time.Millisecond)
	for i := 0; i < 2; i++ {
		if err := s.RecordFailedStart(who, "claude is not installed"); err != nil {
			t.Fatalf("recording: %v", err)
		}
	}

	due, left, got := s.StartDue(who)
	if due {
		t.Error("a start that failed twice was attempted again immediately")
	}
	if left <= 0 {
		t.Errorf("the wait is %s, want something to wait", left)
	}
	if got.Failures != 2 {
		t.Errorf("failures is %d, want 2", got.Failures)
	}
	// The reason travels with the record, so whatever holds off can say what it is
	// waiting out rather than only that it is waiting.
	if got.Why != "claude is not installed" {
		t.Errorf("the reason is %q", got.Why)
	}
}

func TestTheWaitEndsAndItIsTriedAgain(t *testing.T) {
	// Ten seconds pass between readings, which is past the second tier's five.
	s, who := paced(t, 10*time.Second)
	for i := 0; i < 2; i++ {
		if err := s.RecordFailedStart(who, "no pty"); err != nil {
			t.Fatalf("recording: %v", err)
		}
	}
	if due, left, _ := s.StartDue(who); !due {
		t.Errorf("the wait did not end; %s left", left)
	}
}

// The one thing it must never do: stop. An agent that failed twenty times is still
// employed, and a machine that comes back needs no intervention to bring it up.
func TestItNeverStopsTrying(t *testing.T) {
	s, who := paced(t, time.Hour)
	for i := 0; i < 20; i++ {
		if err := s.RecordFailedStart(who, "the machine was asleep"); err != nil {
			t.Fatalf("recording: %v", err)
		}
	}
	due, _, got := s.StartDue(who)
	if !due {
		t.Error("after twenty failures and an hour, a start is not due; that fleet needs a person")
	}
	if got.Failures != 20 {
		t.Errorf("failures is %d, want 20", got.Failures)
	}
}

func TestTheWaitIsCappedRatherThanUnbounded(t *testing.T) {
	s, who := paced(t, time.Millisecond)
	for i := 0; i < 12; i++ {
		if err := s.RecordFailedStart(who, "no"); err != nil {
			t.Fatal(err)
		}
	}
	_, left, _ := s.StartDue(who)
	if cap := store.StartBackoff[len(store.StartBackoff)-1]; left > cap {
		t.Errorf("the wait is %s, past the cap of %s", left, cap)
	}
}

func TestAStartThatWorkedForgetsTheFailures(t *testing.T) {
	s, who := paced(t, time.Millisecond)
	if err := s.RecordFailedStart(who, "no"); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearFailedStarts(who); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if due, _, got := s.StartDue(who); !due || got.Failures != 0 {
		t.Errorf("the failures outlived a start that worked: due=%v failures=%d", due, got.Failures)
	}
	// Clearing what is not there is quiet: every path that succeeds calls this.
	if err := s.ClearFailedStarts(who); err != nil {
		t.Errorf("clearing nothing: %v", err)
	}
}

// A clock that moved must not park an agent. The wait is measured against a
// timestamp, and a timestamp in the future would otherwise hold a session down for
// as long as the clock is wrong.
func TestAClockThatMovedDoesNotParkAnAgent(t *testing.T) {
	s, who := paced(t, -time.Hour+time.Millisecond)
	if err := s.RecordFailedStart(who, "no"); err != nil {
		t.Fatal(err)
	}
	if due, left, _ := s.StartDue(who); !due {
		t.Errorf("a record stamped in the future held the agent down for %s", left)
	}
}
