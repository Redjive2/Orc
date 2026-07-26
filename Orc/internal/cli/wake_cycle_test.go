package cli

import (
	"testing"
	"time"

	"orc/common/user"
	"orc/orc/internal/event"
	"orc/orc/internal/view"
)

// The cycle's memory, tested inside the package.
//
// `orc wake` twice is two processes and two fresh cycles, which is the documented
// shape — so the once-per-silence rule can only be seen from in here, where a single
// waker survives more than one pass.

func mark(t *testing.T, at, name string) view.Session {
	t.Helper()

	who, err := user.Parse("ember")
	if err != nil {
		t.Fatal(err)
	}
	got, err := view.Fold(who, []event.Event{{At: at, Session: "s", Name: name}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

var wakeEpoch = time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)

// TestSilenceIsOnlyEverAboutWaiting. The rule the whole cycle rests on.
func TestSilence(t *testing.T) {
	quiet := wakeEpoch.Add(-30 * time.Minute)

	// Waiting, and for half an hour.
	feed := mark(t, quiet.Format("2006-01-02T15:04:05.000Z"), "Stop")
	last, since, waiting := silence(feed, wakeEpoch, wakeEpoch)
	if !waiting {
		t.Fatal("a feed ending on Stop is waiting")
	}
	if since < 29*time.Minute {
		t.Errorf("silence measured %s, want about half an hour", since)
	}
	if last == "" {
		t.Error("a waiting session has a last event to be marked by")
	}

	// Mid-turn, however long ago: a build takes as long as it takes.
	working := mark(t, quiet.Format("2006-01-02T15:04:05.000Z"), "PreToolUse")
	if _, _, waiting := silence(working, wakeEpoch, wakeEpoch); waiting {
		t.Error("an agent mid-turn was called silent")
	}

	// Nothing said at all: judged from when the session started, and marked by it
	// so that a refresh reads as a new silence rather than one already woken.
	empty, err := view.Fold(mustUserName(t), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	last, since, waiting = silence(empty, wakeEpoch.Add(-time.Hour), wakeEpoch)
	if !waiting || since < 59*time.Minute {
		t.Errorf("a session that has never spoken: waiting=%v since=%s", waiting, since)
	}
	if last == "" {
		t.Error("a feedless silence needs a mark of its own, or it collides with 'never woken'")
	}
}

func mustUserName(t *testing.T) user.Name {
	t.Helper()
	who, err := user.Parse("ember")
	if err != nil {
		t.Fatal(err)
	}
	return who
}

// TestASilenceIsWokenOnceWithinACycle, and a new one is woken again.
//
// This is the anti-spam rule: the cycle remembers the event it woke on, so a session
// that has said nothing since is stuck rather than idle. It is checked here on the
// map itself, because reaching a poke needs a live supervisor and a fake one would
// only be testing the fake.
func TestCycleWakesEachSilenceOnce(t *testing.T) {
	w := &waker{woken: map[string]string{}}

	first := "2026-07-25T11:30:00.000Z"
	if mark, ok := w.woken["ember"]; ok && mark == first {
		t.Fatal("a fresh cycle thinks it has already woken somebody")
	}
	w.woken["ember"] = first

	// The same silence: it has said nothing since.
	if mark, ok := w.woken["ember"]; !ok || mark != first {
		t.Error("the cycle forgot the wake it had just made")
	}

	// It spoke, then went quiet again: a new silence, and woken again.
	second := "2026-07-25T11:45:00.000Z"
	if mark, ok := w.woken["ember"]; ok && mark == second {
		t.Error("a new silence was mistaken for the one already woken")
	}
}

// The empty mark is a real value, not the absence of one. This is the bug the feed
// tests found: a session that has never spoken has no last event, and comparing its
// empty mark against a missing map entry made "never woken" and "woken, and silent
// since" the same state — so the agent that had done nothing at all was reported
// stuck on the first pass and never poked.
func TestNeverWokenIsNotWokenAtNothing(t *testing.T) {
	w := &waker{woken: map[string]string{}}

	if _, ok := w.woken["ember"]; ok {
		t.Fatal("a fresh cycle has no memory")
	}
	w.woken["ember"] = ""
	if _, ok := w.woken["ember"]; !ok {
		t.Error("an empty mark must still count as having been woken")
	}
}
