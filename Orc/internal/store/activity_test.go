package store_test

import (
	"testing"
	"time"

	"orc/orc/internal/activity"
	"orc/orc/internal/store"
)

// The rollup on disk.
//
// Each line is a delta and folding sums them, which is what makes a bucket's total
// only ever grow. That property is the whole reason the shape is this: a mirror
// that receives the same bucket twice writes the same number, and one that missed a
// sync catches up whole.

func hour(n int) time.Time {
	return time.Date(2026, 7, 26, n, 0, 0, 0, time.UTC)
}

func bucket(at time.Time, turns int, out int64) activity.Bucket {
	return activity.Bucket{
		At: at, Model: "opus", Effort: "high", Turns: turns,
		Tokens: activity.Tokens{Output: out},
		Files:  activity.Files{Reads: 1, ReadLines: 10},
	}
}

func TestDeltasForOneHourAreSummed(t *testing.T) {
	s, who := newStore(t)

	for i := 0; i < 3; i++ {
		if err := s.RecordActivity(who, []activity.Bucket{bucket(hour(12), 2, 100)},
			store.Rollup{Session: "s-1"}); err != nil {
			t.Fatalf("recording: %v", err)
		}
	}

	got, err := s.Activity(who, time.Time{})
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d buckets, want 1 — three deltas for one hour are one bucket", len(got))
	}
	if got[0].Turns != 6 || got[0].Tokens.Output != 300 || got[0].Files.ReadLines != 30 {
		t.Errorf("the deltas did not sum: %+v", got[0])
	}
}

func TestHoursAndModelsStayApart(t *testing.T) {
	s, who := newStore(t)

	other := bucket(hour(13), 1, 50)
	other.Model = "sonnet"
	if err := s.RecordActivity(who, []activity.Bucket{bucket(hour(12), 1, 10), other},
		store.Rollup{Session: "s-1"}); err != nil {
		t.Fatal(err)
	}

	got, _ := s.Activity(who, time.Time{})
	if len(got) != 2 {
		t.Fatalf("got %d buckets, want 2", len(got))
	}
	// Oldest first, which is what every chart and table wants.
	if !got[0].At.Before(got[1].At) {
		t.Error("the buckets came back newest-first")
	}
}

func TestAWindowBoundsWhatComesBack(t *testing.T) {
	s, who := newStore(t)
	if err := s.RecordActivity(who,
		[]activity.Bucket{bucket(hour(9), 1, 10), bucket(hour(15), 1, 10)},
		store.Rollup{Session: "s-1"}); err != nil {
		t.Fatal(err)
	}

	got, _ := s.Activity(who, hour(12))
	if len(got) != 1 || !got[0].At.Equal(hour(15)) {
		t.Errorf("the window kept %d buckets: %+v", len(got), got)
	}
}

// The cursor is what makes the next read incremental, and a new conversation must
// not inherit the last one's offset.
func TestTheCursorSurvivesAndIsKeyedBySession(t *testing.T) {
	s, who := newStore(t)

	want := store.Rollup{Session: "s-1", Cursor: activity.Cursor{Path: "/x/t.jsonl", Offset: 4096, Size: 8192}}
	if err := s.RecordActivity(who, nil, want); err != nil {
		t.Fatal(err)
	}

	got, ok := s.ActivityRollup(who)
	if !ok {
		t.Fatal("the cursor did not survive")
	}
	if got.Session != "s-1" || got.Cursor.Offset != 4096 || got.Cursor.Path != "/x/t.jsonl" {
		t.Errorf("the cursor came back as %+v", got)
	}
	// And it says when, so a fleet whose figures stop an hour ago can be asked why.
	if got.At == "" {
		t.Error("the cursor does not record when it was written")
	}
}

// A missing cursor is "nothing has been read", which starts the next pass at the
// beginning. Re-reading costs a pass over a file; skipping costs an hour nobody can
// get back.
func TestNoCursorMeansReadFromTheStart(t *testing.T) {
	s, who := newStore(t)
	if got, ok := s.ActivityRollup(who); ok || got.Cursor.Offset != 0 {
		t.Errorf("a fleet with no cursor reported %+v", got)
	}
}

func TestPruningKeepsWhatIsAskedFor(t *testing.T) {
	s, who := newStore(t)
	if err := s.RecordActivity(who,
		[]activity.Bucket{bucket(hour(9), 1, 10), bucket(hour(15), 2, 20)},
		store.Rollup{Session: "s-1"}); err != nil {
		t.Fatal(err)
	}

	if err := s.PruneActivity(who, hour(12)); err != nil {
		t.Fatalf("pruning: %v", err)
	}
	got, _ := s.Activity(who, time.Time{})
	if len(got) != 1 || got[0].Turns != 2 {
		t.Errorf("pruning left %+v", got)
	}
}

// Pruning everything leaves nothing behind rather than an empty file that reads as
// a fleet which has done no work.
func TestPruningEverythingRemovesTheFile(t *testing.T) {
	s, who := newStore(t)
	if err := s.RecordActivity(who, []activity.Bucket{bucket(hour(9), 1, 10)},
		store.Rollup{Session: "s-1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneActivity(who, hour(20)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Activity(who, time.Time{}); len(got) != 0 {
		t.Errorf("pruning everything left %+v", got)
	}
}

func TestAFleetWithNoRollupReadsAsNothing(t *testing.T) {
	s, who := newStore(t)
	got, err := s.Activity(who, time.Time{})
	if err != nil {
		t.Fatalf("reading a rollup that is not there: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("it read %d buckets out of nothing", len(got))
	}
}
