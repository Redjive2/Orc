package notify_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/macmuffin/internal/notify"
	"orc/macmuffin/internal/store"
	"orc/macmuffin/internal/task"
)

var epoch = time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

// recorder stands in for the mailman binary. Nothing in this suite ever execs.
type recorder struct {
	mu    sync.Mutex
	calls []call
	fail  error
}

type call struct {
	args  []string
	stdin string
}

func (r *recorder) run(args []string, stdin string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call{args: append([]string(nil), args...), stdin: stdin})
	return r.fail
}

func (r *recorder) last() call {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return call{}
	}
	return r.calls[len(r.calls)-1]
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

type rig struct {
	t     *testing.T
	store *store.Store
	rec   *recorder
	c     notify.Courier
}

func newRig(t *testing.T) *rig {
	t.Helper()
	s, err := store.Open(t.TempDir(), clock.NewFake(epoch, time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	c, err := notify.New(s, rec.run)
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, store: s, rec: rec, c: c}
}

func (r *rig) agent(s string) user.Name {
	r.t.Helper()
	n, err := user.Parse(s)
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

// sample builds a task worth describing in a notice.
func (r *rig) sample() task.Task {
	r.t.Helper()
	name, err := task.ParseName("fix-the-parser")
	if err != nil {
		r.t.Fatal(err)
	}
	p, _ := task.NewPriority(4)
	d, _ := task.NewDifficulty(3)
	got, err := task.NewDraft(name, r.agent("alice"), p, d, epoch)
	if err != nil {
		r.t.Fatal(err)
	}
	for _, make := range []func() (task.Event, error){
		func() (task.Event, error) { return task.Scope(r.agent("alice"), epoch, []string{"internal/tree/"}) },
		func() (task.Event, error) { return task.AddSub(r.agent("alice"), epoch, mustName(r.t, "one")) },
		func() (task.Event, error) { return task.Push(r.agent("alice"), epoch) },
		func() (task.Event, error) { return task.Claim(r.agent("alice"), epoch) },
		func() (task.Event, error) { return task.SetStatus(r.agent("alice"), epoch, task.StatusNominal) },
	} {
		ev, err := make()
		if err != nil {
			r.t.Fatal(err)
		}
		if got, err = got.With(ev); err != nil {
			r.t.Fatal(err)
		}
	}
	return got
}

func mustName(t *testing.T, s string) task.Name {
	t.Helper()
	n, err := task.ParseName(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestTheInviteNoticeIsPinned. A notice that leaves the reader looking things
// up is one they will stop reading, so its body is a golden.
func TestInviteNoticeBody(t *testing.T) {
	r := newRig(t)
	if err := r.c.Joined(r.sample(), r.agent("alice"), r.agent("bob")); err != nil {
		t.Fatalf("Joined: %v", err)
	}

	got := r.rec.last()
	// Both people get it: `mailman send` has one recipient list and no cc
	// field, so the caller is a recipient rather than a copy.
	want := []string{"send", "you are on fix-the-parser", "bob", "alice", "-"}
	if strings.Join(got.args, "|") != strings.Join(want, "|") {
		t.Errorf("args = %v, want %v", got.args, want)
	}
	// The body goes on stdin, not in argv, which is both size-limited and
	// visible in `ps`.
	if got.stdin != wantInvite {
		t.Errorf("the invite body changed.\n got:\n%s\nwant:\n%s", got.stdin, wantInvite)
	}
}

const wantInvite = `alice added you to **fix-the-parser**.

- priority 4, difficulty 3
- status: nominal
- owner: alice
- subtasks: 0 of 1 done
- scope: internal/tree/

See it with ` + "`muff info fix-the-parser`" + `.
Editing is limited to the scope above while the task is in force.
`

func TestKickNoticeBody(t *testing.T) {
	r := newRig(t)
	if err := r.c.Removed(r.sample(), r.agent("alice"), r.agent("bob")); err != nil {
		t.Fatalf("Removed: %v", err)
	}
	got := r.rec.last()
	if !strings.Contains(got.stdin, "removed you from **fix-the-parser**") {
		t.Errorf("the removal body:\n%s", got.stdin)
	}
	if !strings.Contains(got.args[1], "you are off") {
		t.Errorf("subject = %q", got.args[1])
	}
	// It still says what the task was, so the reader knows what they lost.
	if !strings.Contains(got.stdin, "priority 4") {
		t.Errorf("the body should describe the task:\n%s", got.stdin)
	}
}

// TestAFailedNoticeIsQueuedNotLost is milestone 7's criterion.
func TestFailedNoticeIsQueued(t *testing.T) {
	r := newRig(t)
	r.rec.fail = errors.New("mailman is not installed")

	err := r.c.Joined(r.sample(), r.agent("alice"), r.agent("bob"))
	var undeliverable notify.Undeliverable
	if !errors.As(err, &undeliverable) {
		t.Fatalf("Joined = %v, want an Undeliverable", err)
	}
	// It is not a fault: the membership change already happened, so the caller
	// reports it as a warning rather than failing. If it ever unwrapped to a
	// sentinel, a working invite would start exiting non-zero.
	for _, sentinel := range []error{
		fault.ErrInternal, fault.ErrIO, fault.ErrConflict, fault.ErrUsage, fault.ErrUnavailable,
	} {
		if errors.Is(err, sentinel) {
			t.Errorf("an undeliverable notice unwraps to %v, so it would set an exit code", sentinel)
		}
	}

	pending, err := r.store.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("%d notices queued, want 1", len(pending))
	}
	if pending[0].Attempts != 1 {
		t.Errorf("attempts = %d, want 1", pending[0].Attempts)
	}
	if !strings.Contains(pending[0].LastErr, "not installed") {
		t.Errorf("the failure should be recorded: %q", pending[0].LastErr)
	}
}

// TestDrainDeliversWhatWasQueued: the next command in any process retries.
func TestDrainDelivers(t *testing.T) {
	r := newRig(t)
	r.rec.fail = errors.New("temporarily broken")
	if err := r.c.Joined(r.sample(), r.agent("alice"), r.agent("bob")); err == nil {
		t.Fatal("expected the first attempt to fail")
	}

	// Still failing: it waits rather than being lost.
	sent, waiting, stuck, err := r.c.Drain()
	if err != nil {
		t.Fatal(err)
	}
	if sent != 0 || waiting != 1 || stuck != 0 {
		t.Fatalf("Drain = %d sent, %d waiting, %d stuck", sent, waiting, stuck)
	}

	// Mailman comes back.
	r.rec.fail = nil
	sent, waiting, stuck, err = r.c.Drain()
	if err != nil {
		t.Fatal(err)
	}
	if sent != 1 || waiting != 0 || stuck != 0 {
		t.Fatalf("Drain = %d sent, %d waiting, %d stuck", sent, waiting, stuck)
	}

	pending, err := r.store.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("%d notices still queued after delivery", len(pending))
	}
	// And a drain with nothing to do is silent and cheap.
	if sent, waiting, stuck, err := r.c.Drain(); err != nil || sent+waiting+stuck != 0 {
		t.Errorf("an empty drain = %d/%d/%d, %v", sent, waiting, stuck, err)
	}
}

// TestARetryLoopGivesUp. One that never does is one that eventually hides the
// actual problem.
func TestRetriesStopAtTheLimit(t *testing.T) {
	r := newRig(t)
	r.rec.fail = errors.New("still broken")
	if err := r.c.Joined(r.sample(), r.agent("alice"), r.agent("bob")); err == nil {
		t.Fatal("expected a failure")
	}

	for range store.MaxAttempts + 5 {
		if _, _, _, err := r.c.Drain(); err != nil {
			t.Fatal(err)
		}
	}

	pending, err := r.store.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("%d notices queued, want the stuck one", len(pending))
	}
	if !pending[0].Exhausted() {
		t.Errorf("after %d attempts the notice should have given up, got %d", store.MaxAttempts, pending[0].Attempts)
	}
	if pending[0].Attempts > store.MaxAttempts {
		t.Errorf("a stuck notice was retried past the limit: %d attempts", pending[0].Attempts)
	}

	_, _, stuck, err := r.c.Drain()
	if err != nil {
		t.Fatal(err)
	}
	if stuck != 1 {
		t.Errorf("Drain reported %d stuck, want 1", stuck)
	}
	// It stops being executed entirely once exhausted.
	before := r.rec.count()
	if _, _, _, err := r.c.Drain(); err != nil {
		t.Fatal(err)
	}
	if r.rec.count() != before {
		t.Error("a stuck notice was executed again")
	}
}

// TestNoticesToOneselfHaveOneRecipient: an agent inviting themselves should not
// be sent two copies.
func TestSelfNoticeHasOneRecipient(t *testing.T) {
	r := newRig(t)
	if err := r.c.Joined(r.sample(), r.agent("alice"), r.agent("alice")); err != nil {
		t.Fatal(err)
	}
	got := r.rec.last()
	// send, subject, alice, "-"
	if len(got.args) != 4 {
		t.Errorf("args = %v, want one recipient", got.args)
	}
}

func TestNewRejectsBadArguments(t *testing.T) {
	if _, err := notify.New(nil, nil); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("New without a store = %v, want an internal fault", err)
	}
	// A nil Run falls back to the real binary rather than panicking.
	s, err := store.Open(t.TempDir(), clock.NewFake(epoch, time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := notify.New(s, nil); err != nil {
		t.Errorf("New with a nil Run = %v", err)
	}
}
