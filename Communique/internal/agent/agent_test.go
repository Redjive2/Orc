package agent_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"orc/cq/internal/agent"
	"orc/cq/internal/auth"
	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
	"orc/cq/internal/server"
	"orc/cq/internal/source"
	"orc/cq/internal/store"
)

var at = time.Date(2026, 7, 24, 18, 31, 4, 0, time.UTC)

// fakeSource stands in for Mailman and Macmuffin. It records what it was asked
// to do, so a test can assert an action reached the world exactly once.
type fakeSource struct {
	mu sync.Mutex

	snapshot protocol.Snapshot
	snapErr  error

	applied  []protocol.ActionID
	applyErr map[protocol.ActionID]error
	// onApply runs inside Apply, so a test can simulate a crash at the one
	// moment that matters: after the action took effect, before it was recorded.
	onApply func(protocol.Action)
}

func newFakeSource(machine protocol.MachineID) *fakeSource {
	return &fakeSource{
		snapshot: protocol.Snapshot{
			Machine: machine, User: "redjive", TakenAt: at,
			Inbox: []protocol.Message{{
				PUID: 41, MID: "019a-1", Sent: at, From: "boss",
				To: []string{"redjive"}, Subject: "RE: work", Body: "hello",
			}},
		},
		applyErr: map[protocol.ActionID]error{},
	}
}

func (f *fakeSource) Snapshot(context.Context, source.Options) (protocol.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.snapErr != nil {
		return protocol.Snapshot{}, f.snapErr
	}
	return f.snapshot, nil
}

func (f *fakeSource) Apply(_ context.Context, a protocol.Action) error {
	f.mu.Lock()
	f.applied = append(f.applied, a.ID)
	hook, err := f.onApply, f.applyErr[a.ID]
	f.mu.Unlock()

	if hook != nil {
		hook(a)
	}
	return err
}

func (f *fakeSource) timesApplied(id protocol.ActionID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, seen := range f.applied {
		if seen == id {
			n++
		}
	}
	return n
}

// rig is an agent, a real server, and the fake machine behind it.
type rig struct {
	agent  *agent.Agent
	src    *fakeSource
	state  *store.Store
	creds  *auth.Store
	server *httptest.Server
	dir    string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	return newRigAt(t, t.TempDir(), t.TempDir())
}

func newRigAt(t *testing.T, serverRoot, agentDir string) *rig {
	t.Helper()

	state, err := store.Open(serverRoot)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := auth.Open(serverRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := creds.SetPassword("correct horse battery", at); err != nil {
		t.Fatal(err)
	}
	token, _, err := creds.NewToken("studio", at)
	if err != nil {
		t.Fatal(err)
	}

	srv, err := server.New(server.Options{
		State: state, Creds: creds, Admin: true,
		Logger: slog.New(slog.DiscardHandler),
		Now:    func() time.Time { return at },
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	src := newFakeSource("studio")
	ag, err := agent.New(agent.Options{
		Source: src, Server: ts.URL, Token: token, Machine: "studio",
		State: agentDir, Logger: slog.New(slog.DiscardHandler),
		Now: func() time.Time { return at },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &rig{agent: ag, src: src, state: state, creds: creds, server: ts, dir: agentDir}
}

// queue puts an action on the server, as the browser would.
func (r *rig) queue(t *testing.T, op protocol.Op, args protocol.Args) protocol.Action {
	t.Helper()
	action, err := r.state.Enqueue("studio", op, args, at)
	if err != nil {
		t.Fatal(err)
	}
	return action
}

func (r *rig) entry(t *testing.T, id protocol.ActionID) store.Entry {
	t.Helper()
	entries, err := r.state.Queue()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Action.ID == id {
			return e
		}
	}
	t.Fatalf("no queue entry for %s", id)
	return store.Entry{}
}

// TestSnapshotGoesUpAndActionsComeDown is the milestone gate's first half.
func TestSnapshotGoesUpAndActionsComeDown(t *testing.T) {
	r := newRig(t)
	action := r.queue(t, protocol.OpRead, protocol.Args{PUID: 41})

	report, err := r.agent.Sync(t.Context())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if report.Received != 1 || report.Applied != 1 || report.Failed != 0 {
		t.Errorf("report = %+v", report)
	}
	if got := r.src.timesApplied(action.ID); got != 1 {
		t.Errorf("the action was applied %d times, want 1", got)
	}

	// The snapshot reached the server.
	snap, meta, err := r.state.Snapshot("studio")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Inbox) != 1 || snap.Inbox[0].PUID != 41 {
		t.Errorf("snapshot = %+v", snap.Inbox)
	}
	if meta.Agent != agent.Version {
		t.Errorf("agent = %q, want %q", meta.Agent, agent.Version)
	}

	// The result gets back on the next sync.
	if _, err := r.agent.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := r.entry(t, action.ID); got.State != store.Done {
		t.Errorf("queue entry = %+v, want done", got)
	}
}

// TestAnActionIsAppliedExactlyOnce is the gate's second half: re-delivery is
// how the wire works, and the agent is what makes it harmless.
func TestAnActionIsAppliedExactlyOnce(t *testing.T) {
	r := newRig(t)
	action := r.queue(t, protocol.OpSend, protocol.Args{
		To: []string{"bob"}, Subject: "hello", Body: "hi",
	})

	for range 5 {
		if _, err := r.agent.Sync(t.Context()); err != nil {
			t.Fatalf("Sync: %v", err)
		}
	}
	if got := r.src.timesApplied(action.ID); got != 1 {
		t.Errorf("a send reached the world %d times, want exactly 1", got)
	}
}

// TestAFailedActionIsReportedAndNotRetried: a reply that failed because the
// recipient does not exist will fail identically forever, and the user should
// see why rather than watch it retry.
func TestAFailedActionIsReportedAndNotRetried(t *testing.T) {
	r := newRig(t)
	action := r.queue(t, protocol.OpCC, protocol.Args{ConvoUID: "c-1", User: "carol"})
	r.src.applyErr[action.ID] = errors.New(`no such user "carol"`)

	report, err := r.agent.Sync(t.Context())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if report.Failed != 1 || report.Applied != 0 {
		t.Errorf("report = %+v", report)
	}

	if _, err := r.agent.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	entry := r.entry(t, action.ID)
	if entry.State != store.Failed {
		t.Fatalf("state = %q, want failed", entry.State)
	}
	if !strings.Contains(entry.Error, "carol") {
		t.Errorf("error = %q, want the reason", entry.Error)
	}
	if got := r.src.timesApplied(action.ID); got != 1 {
		t.Errorf("a failed action was retried %d times", got-1)
	}
}

// TestACrashMidActionLeavesItInDoubt is the property the journal exists for.
//
// The process dies after the action took effect and before the outcome was
// recorded. cq must not silently send the message again — and must not silently
// pretend it succeeded either.
func TestACrashMidActionLeavesItInDoubt(t *testing.T) {
	serverRoot, agentDir := t.TempDir(), t.TempDir()
	r := newRigAt(t, serverRoot, agentDir)
	action := r.queue(t, protocol.OpSend, protocol.Args{
		To: []string{"bob"}, Subject: "hello", Body: "hi",
	})

	// The crash: Apply takes effect, then the process is gone.
	crashed := errors.New("simulated crash")
	r.src.onApply = func(protocol.Action) { panic(crashed) }

	func() {
		defer func() {
			if v := recover(); v != crashed {
				t.Fatalf("expected the simulated crash, got %v", v)
			}
		}()
		_, _ = r.agent.Sync(t.Context())
	}()
	if got := r.src.timesApplied(action.ID); got != 1 {
		t.Fatalf("the action was applied %d times before the crash", got)
	}

	// A fresh agent over the same journal, as a restart would be.
	restarted := newRigAt(t, serverRoot, agentDir)
	restarted.src.applied = nil

	if _, err := restarted.agent.Sync(t.Context()); err != nil {
		t.Fatalf("Sync after restart: %v", err)
	}
	if got := restarted.src.timesApplied(action.ID); got != 0 {
		t.Errorf("the action was applied again after the crash: %d times", got)
	}

	if _, err := restarted.agent.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	entry := restarted.entry(t, action.ID)
	// In doubt, and specifically *not* failed. The two look alike and demand
	// opposite responses: a refusal did not happen and can be tried again, and
	// this may already have sent a real message to a real person. Anything that
	// offers "retry" has to be able to tell them apart without reading prose.
	if entry.State != store.InDoubt {
		t.Fatalf("state = %q, want in_doubt — an interrupted action is neither a success nor a refusal", entry.State)
	}
	if !strings.Contains(entry.Error, "may or may not") {
		t.Errorf("error = %q, want it to say the outcome is unknown", entry.Error)
	}
}

// TestACrashBetweenActionsLosesNothing: the first action is recorded, the
// second is not yet started.
func TestACrashBetweenActionsLosesNothing(t *testing.T) {
	serverRoot, agentDir := t.TempDir(), t.TempDir()
	r := newRigAt(t, serverRoot, agentDir)
	first := r.queue(t, protocol.OpRead, protocol.Args{PUID: 41})
	second := r.queue(t, protocol.OpArchive, protocol.Args{PUID: 41})

	boom := errors.New("crash between actions")
	r.src.onApply = func(a protocol.Action) {
		if a.ID == second.ID {
			panic(boom)
		}
	}

	func() {
		defer func() { _ = recover() }()
		_, _ = r.agent.Sync(t.Context())
	}()

	restarted := newRigAt(t, serverRoot, agentDir)
	restarted.src.applied = nil
	if _, err := restarted.agent.Sync(t.Context()); err != nil {
		t.Fatalf("Sync after restart: %v", err)
	}
	if got := restarted.src.timesApplied(first.ID); got != 0 {
		t.Errorf("the completed action was applied again %d times", got)
	}

	if _, err := restarted.agent.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := restarted.entry(t, first.ID); got.State != store.Done {
		t.Errorf("the first action = %q, want done", got.State)
	}
	if got := restarted.entry(t, second.ID); got.State != store.InDoubt {
		t.Errorf("the interrupted action = %q, want in_doubt", got.State)
	}
}

// TestAnInterruptedJournalAppendIsTolerated: only the last line can be damaged
// by a kill, and replay says so rather than refusing to start.
func TestAnInterruptedJournalAppendIsTolerated(t *testing.T) {
	r := newRig(t)
	action := r.queue(t, protocol.OpRead, protocol.Args{PUID: 41})
	if _, err := r.agent.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(r.dir, "applied.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte(`{"op":"applied","id":"aaaa`)...), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := r.agent.Sync(t.Context())
	if err != nil {
		t.Fatalf("a truncated final line should be tolerated: %v", err)
	}
	if !report.Truncated {
		t.Errorf("the report should say the journal was truncated")
	}
	if got := r.src.timesApplied(action.ID); got != 1 {
		t.Errorf("the action was applied %d times", got)
	}
}

// TestCorruptionInTheMiddleIsRefused: a bad line anywhere but the end is not an
// interrupted write, and skipping it would silently drop a record.
func TestCorruptionInTheMiddleIsRefused(t *testing.T) {
	r := newRig(t)
	r.queue(t, protocol.OpRead, protocol.Args{PUID: 41})
	if _, err := r.agent.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(r.dir, "applied.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := append([]byte("{not json at all\n"), data...)
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := r.agent.Sync(t.Context()); !errors.Is(err, fault.ErrParse) {
		t.Errorf("error = %v, want a parse fault", err)
	}
}

func TestSyncReportsAnUnreachableServer(t *testing.T) {
	r := newRig(t)
	r.server.Close()

	_, err := r.agent.Sync(t.Context())
	if !errors.Is(err, fault.ErrUnavailable) {
		t.Fatalf("error = %v, want unavailable", err)
	}
	if got := fault.Exit(err); got != fault.ExitUnavailable {
		t.Errorf("exit = %d, want %d", got, fault.ExitUnavailable)
	}
}

func TestSyncReportsARejectedToken(t *testing.T) {
	r := newRig(t)
	bad, err := agent.New(agent.Options{
		Source: r.src, Server: r.server.URL, Token: "00000000000000ff.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		Machine: "studio", State: t.TempDir(), Logger: slog.New(slog.DiscardHandler),
		Now: func() time.Time { return at },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.Sync(t.Context()); !errors.Is(err, fault.ErrUnauthenticated) {
		t.Errorf("error = %v, want unauthenticated", err)
	}
}

func TestSyncReportsASourceFailure(t *testing.T) {
	r := newRig(t)
	r.src.snapErr = errors.New("mailman is wedged")

	if _, err := r.agent.Sync(t.Context()); err == nil {
		t.Fatal("expected a failure")
	}

	// And `cq status` can say so without touching the network.
	c, _, err := r.agent.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.LastError, "wedged") {
		t.Errorf("cursor error = %q, want the reason", c.LastError)
	}
}

// TestActionsForAnotherMachineAreRefused: a puid means something different in
// every mailbox, so applying one here would act on the wrong message.
func TestActionsForAnotherMachineAreRefused(t *testing.T) {
	r := newRig(t)
	action, err := r.state.Enqueue("laptop", protocol.OpArchive, protocol.Args{PUID: 41}, at)
	if err != nil {
		t.Fatal(err)
	}
	// Hand it to the studio agent anyway, as a confused server would.
	if err := r.state.MarkSent([]protocol.ActionID{action.ID}, at); err != nil {
		t.Fatal(err)
	}

	if _, err := r.agent.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := r.src.timesApplied(action.ID); got != 0 {
		t.Errorf("an action for another machine was applied %d times", got)
	}
}

func TestNewValidatesItsOptions(t *testing.T) {
	src := newFakeSource("studio")
	log := slog.New(slog.DiscardHandler)
	base := agent.Options{
		Source: src, Server: "http://localhost:8080", Token: "t",
		Machine: "studio", State: t.TempDir(), Logger: log,
	}

	for _, tc := range []struct {
		name string
		mut  func(*agent.Options)
	}{
		{"no source", func(o *agent.Options) { o.Source = nil }},
		{"no logger", func(o *agent.Options) { o.Logger = nil }},
		{"no server", func(o *agent.Options) { o.Server = "" }},
		{"no token", func(o *agent.Options) { o.Token = "" }},
		{"bad machine", func(o *agent.Options) { o.Machine = "Not A Machine" }},
		{"server without a scheme", func(o *agent.Options) { o.Server = "localhost:8080" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := base
			tc.mut(&opts)
			if _, err := agent.New(opts); err == nil {
				t.Errorf("an agent was built with %s", tc.name)
			}
		})
	}
}

func TestSnapshotMachineMustMatch(t *testing.T) {
	r := newRig(t)
	r.src.snapshot.Machine = "somewhere-else"
	if _, err := r.agent.Sync(t.Context()); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("error = %v, want an internal fault", err)
	}
}

// TestNudgeCoalescesABurst is the milestone's other named property.
func TestNudgeCoalescesABurst(t *testing.T) {
	r := newRig(t)

	// Twenty nudges at once. One takes the lock; the rest leave a marker and
	// return immediately, and the holder goes round once more for them.
	var (
		mu    sync.Mutex
		syncs int
		wg    sync.WaitGroup
		once  sync.Once
	)
	slow := make(chan struct{})
	// holding is closed once a sync is genuinely under way, which is the only
	// moment at which the lock is certainly held. Signalling before the call
	// instead — as this test used to — says nothing: the goroutine could still
	// be descheduled, one of the nineteen would take the lock, and it would park
	// in here waiting on a channel the loop below closes only after it finishes.
	// The test deadlocked whenever the goroutine lost that race, which was most
	// of the time.
	holding := make(chan struct{})
	r.src.onApply = func(protocol.Action) {
		mu.Lock()
		syncs++
		mu.Unlock()
		once.Do(func() { close(holding) })
		<-slow
	}
	r.queue(t, protocol.OpRead, protocol.Args{PUID: 41})

	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, _, err := r.agent.Nudge(t.Context()); err != nil {
			t.Errorf("Nudge: %v", err)
		}
	}()
	<-holding

	// The others must not block behind it.
	deadline := time.Now().Add(5 * time.Second)
	blocked := 0
	for range 19 {
		start := time.Now()
		_, ran, err := r.agent.Nudge(t.Context())
		if err != nil {
			t.Fatalf("Nudge: %v", err)
		}
		if time.Since(start) > time.Second {
			blocked++
		}
		if ran {
			t.Errorf("a second nudge ran a sync while one was in flight")
		}
		if time.Now().After(deadline) {
			t.Fatal("nudges are blocking")
		}
	}
	if blocked > 0 {
		t.Errorf("%d nudges blocked on the running sync", blocked)
	}

	// The point of coalescing: twenty nudges, one sync. Counting this is what
	// makes the test about collapsing a burst rather than only about not
	// blocking.
	mu.Lock()
	got := syncs
	mu.Unlock()
	if got != 1 {
		t.Errorf("%d syncs ran for a burst of twenty nudges, want 1", got)
	}

	close(slow)
	wg.Wait()
}

func TestNudgeRunsWhenNothingElseIs(t *testing.T) {
	r := newRig(t)
	action := r.queue(t, protocol.OpRead, protocol.Args{PUID: 41})

	report, ran, err := r.agent.Nudge(t.Context())
	if err != nil {
		t.Fatalf("Nudge: %v", err)
	}
	if !ran {
		t.Fatalf("the first nudge should have synced")
	}
	if report.Applied != 1 {
		t.Errorf("report = %+v", report)
	}
	if got := r.src.timesApplied(action.ID); got != 1 {
		t.Errorf("applied %d times", got)
	}
}

// TestNudgeGoesRoundAgainForAPendingRequest: a command that arrived while a
// sync was running must not have to wait for the timer.
func TestNudgeGoesRoundAgainForAPendingRequest(t *testing.T) {
	r := newRig(t)

	// Mark a request as pending before the nudge starts its second look.
	first := r.queue(t, protocol.OpRead, protocol.Args{PUID: 41})
	var once sync.Once
	r.src.onApply = func(protocol.Action) {
		once.Do(func() {
			// A nudge arriving mid-sync leaves this marker.
			if err := os.WriteFile(filepath.Join(r.dir, "pending"), []byte("1\n"), 0o600); err != nil {
				t.Errorf("could not mark pending: %v", err)
			}
			// And the browser queues something new.
			r.queue(t, protocol.OpArchive, protocol.Args{PUID: 41})
		})
	}

	report, ran, err := r.agent.Nudge(t.Context())
	if err != nil {
		t.Fatalf("Nudge: %v", err)
	}
	if !ran {
		t.Fatal("the nudge did not sync")
	}
	if report.Applied != 2 {
		t.Errorf("report = %+v, want both actions applied in one nudge", report)
	}
	if got := r.src.timesApplied(first.ID); got != 1 {
		t.Errorf("the first action was applied %d times", got)
	}
}

func TestStatusWithoutASync(t *testing.T) {
	r := newRig(t)
	c, s, err := r.agent.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !c.LastSync.IsZero() || len(s.Unreported()) != 0 {
		t.Errorf("a fresh agent has no history: %+v", c)
	}
}

func TestServerFaultsAreClassified(t *testing.T) {
	r := newRig(t)

	// A server that answers with something cq cannot use.
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>gateway</html>"))
	}))
	defer broken.Close()

	ag, err := agent.New(agent.Options{
		Source: r.src, Server: broken.URL, Token: "t", Machine: "studio",
		State: t.TempDir(), Logger: slog.New(slog.DiscardHandler),
		Now: func() time.Time { return at },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Sync(t.Context()); !errors.Is(err, fault.ErrUnavailable) {
		t.Errorf("error = %v, want unavailable", err)
	}
}
