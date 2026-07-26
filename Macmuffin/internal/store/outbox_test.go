package store_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/common/user"
	"orc/macmuffin/internal/store"
)

func recipients(t *testing.T, names ...string) []user.Name {
	t.Helper()
	got, err := user.ParseList(names)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestOutboxRoundTrip(t *testing.T) {
	s := newRig(t).Store

	queued, err := s.Queue(recipients(t, "bob", "alice"), "you are on t", "body\nwith lines")
	if err != nil {
		t.Fatal(err)
	}
	if queued.ID == "" || queued.Attempts != 0 {
		t.Fatalf("queued = %+v", queued)
	}

	pending, err := s.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("%d pending, want 1", len(pending))
	}
	got := pending[0]
	if got.Subject != queued.Subject || got.Body != queued.Body {
		t.Errorf("the notice did not survive the round trip: %+v", got)
	}
	if strings.Join(user.Names(got.To), ",") != "bob,alice" {
		t.Errorf("recipients = %v; order matters, since the first is the subject of the notice", user.Names(got.To))
	}

	if err := s.Delivered(got.ID); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.Pending(); err != nil || len(pending) != 0 {
		t.Errorf("after delivery: %d pending, %v", len(pending), err)
	}
	// Delivering twice is not an error: a retry that raced with a delivery
	// must not fail the command it was drained from.
	if err := s.Delivered(got.ID); err != nil {
		t.Errorf("a second delivery = %v", err)
	}
}

func TestUndeliveredCountsAttempts(t *testing.T) {
	s := newRig(t).Store

	n, err := s.Queue(recipients(t, "bob"), "you are on t", "body")
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if err := s.Undelivered(n, errors.New("broken")); err != nil {
			t.Fatal(err)
		}
		pending, err := s.Pending()
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) != 1 || pending[0].Attempts != i {
			t.Fatalf("after %d failures: %+v", i, pending)
		}
		n = pending[0]
		if n.Exhausted() {
			t.Fatalf("gave up after %d attempts, limit is %d", i, store.MaxAttempts)
		}
	}
	if !strings.Contains(n.LastErr, "broken") {
		t.Errorf("the last failure should be recorded: %q", n.LastErr)
	}
}

// TestOneDamagedNoticeDoesNotStopTheRest. The outbox is drained by every
// command, so a single unreadable entry must not hold up delivery of the others.
func TestDamagedNoticeIsSkippedAndReported(t *testing.T) {
	s := newRig(t).Store

	good, err := s.Queue(recipients(t, "bob"), "you are on t", "body")
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(s.Root(), "outbox", "0123456789abcdef01234567.json")
	if err := os.WriteFile(bad, []byte("{ this is not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	pending, err := s.Pending()
	if err != nil {
		t.Fatalf("one damaged notice broke the whole listing: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != good.ID {
		t.Fatalf("pending = %+v, want just the readable one", pending)
	}

	// It is not silently ignored, though: verify can name it.
	damaged, err := s.Damaged()
	if err != nil {
		t.Fatal(err)
	}
	if len(damaged) != 1 || !strings.Contains(damaged[0], "0123456789abcdef") {
		t.Errorf("damaged = %v, want the bad entry named", damaged)
	}
}

// A notice filed under one name but calling itself another is a copied file, not
// a queued notice.
func TestMisfiledNoticeIsRefused(t *testing.T) {
	s := newRig(t).Store

	n, err := s.Queue(recipients(t, "bob"), "you are on t", "body")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.Root(), "outbox", n.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(s.Root(), "outbox", "ffffffffffffffffffffffff.json")
	if err := os.WriteFile(copied, data, 0o600); err != nil {
		t.Fatal(err)
	}

	pending, err := s.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Errorf("%d pending; a misfiled copy should not be delivered twice", len(pending))
	}
	if damaged, err := s.Damaged(); err != nil || len(damaged) != 1 {
		t.Errorf("damaged = %v, %v; the copy should be named", damaged, err)
	}
}

func TestQueueRefusesNonsense(t *testing.T) {
	s := newRig(t).Store

	if _, err := s.Queue(nil, "subject", "body"); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("a notice with no recipient = %v", err)
	}
	if _, err := s.Queue(recipients(t, "bob"), "   ", "body"); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("a notice with no subject = %v", err)
	}
	if _, err := s.Queue(recipients(t, "bob"), "subject", strings.Repeat("x", store.MaxNoticeSize+1)); !errors.Is(err, fault.ErrUsage) {
		t.Errorf("an oversized notice = %v, want a usage fault", err)
	}
	if err := s.Delivered("  "); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("delivering nothing = %v", err)
	}
}

// An empty outbox is the common case: every command drains, and almost none
// have anything to drain.
func TestPendingOnAFreshStore(t *testing.T) {
	s := newRig(t).Store
	if pending, err := s.Pending(); err != nil || len(pending) != 0 {
		t.Errorf("Pending on a fresh store = %v, %v", pending, err)
	}
	if damaged, err := s.Damaged(); err != nil || len(damaged) != 0 {
		t.Errorf("Damaged on a fresh store = %v, %v", damaged, err)
	}
}
