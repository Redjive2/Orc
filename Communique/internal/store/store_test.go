package store_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
	"orc/cq/internal/store"
)

var at = time.Date(2026, 7, 24, 18, 31, 4, 0, time.UTC)

func open(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func snapshot(machine protocol.MachineID) protocol.Snapshot {
	return protocol.Snapshot{
		Machine: machine,
		User:    "redjive",
		TakenAt: at,
		Inbox: []protocol.Message{{
			PUID: 41, MID: "019a-1", Sent: at, From: "boss",
			To: []string{"redjive"}, Subject: "RE: work", Body: "hello",
		}},
	}
}

func TestOpenCreatesAndReopens(t *testing.T) {
	root := t.TempDir()
	s, err := store.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, dir := range []string{"machines", "queue", "tokens", "sessions"} {
		info, err := os.Stat(filepath.Join(root, dir))
		if err != nil || !info.IsDir() {
			t.Errorf("%s was not created: %v", dir, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "version")); err != nil {
		t.Errorf("version file was not written: %v", err)
	}
	if _, err := store.Open(s.Root()); err != nil {
		t.Errorf("reopening an existing store failed: %v", err)
	}
}

func TestOpenRefusesAnUnknownFormat(t *testing.T) {
	root := t.TempDir()
	if _, err := store.Open(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "version"), []byte("99\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := store.Open(root)
	if !errors.Is(err, fault.ErrParse) {
		t.Fatalf("error = %v, want a parse fault", err)
	}
	if !strings.Contains(err.Error(), "version 99") {
		t.Errorf("message %q should name the version it found", err)
	}

	if err := os.WriteFile(filepath.Join(root, "version"), []byte("banana"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(root); !errors.Is(err, fault.ErrParse) {
		t.Errorf("a non-numeric version should be a parse fault, got %v", err)
	}
}

func TestOpenRejectsAnEmptyPath(t *testing.T) {
	if _, err := store.Open(""); !errors.Is(err, fault.ErrUsage) {
		t.Errorf("error = %v, want a usage fault", err)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	s := open(t)
	if err := s.PutSnapshot(snapshot("studio"), "cq/0.1", at); err != nil {
		t.Fatalf("PutSnapshot: %v", err)
	}

	got, meta, err := s.Snapshot("studio")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got.User != "redjive" || len(got.Inbox) != 1 || got.Inbox[0].PUID != 41 {
		t.Errorf("snapshot did not round trip: %+v", got)
	}
	if !meta.LastSync.Equal(at) || meta.Agent != "cq/0.1" || meta.Protocol != protocol.Version {
		t.Errorf("meta = %+v", meta)
	}
}

func TestSnapshotReplacesWholesale(t *testing.T) {
	s := open(t)
	if err := s.PutSnapshot(snapshot("studio"), "cq/0.1", at); err != nil {
		t.Fatal(err)
	}

	second := snapshot("studio")
	second.Inbox = nil
	second.TakenAt = at.Add(time.Minute)
	if err := s.PutSnapshot(second, "cq/0.1", at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	got, _, err := s.Snapshot("studio")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Inbox) != 0 {
		t.Errorf("the previous snapshot's mail survived: %+v", got.Inbox)
	}
}

// TestSnapshotSurvivesACrashBetweenWrites is the milestone gate. PutSnapshot
// writes two files; a process killed between them must leave readable data.
func TestSnapshotSurvivesACrashBetweenWrites(t *testing.T) {
	s := open(t)
	if err := s.PutSnapshot(snapshot("studio"), "cq/0.1", at); err != nil {
		t.Fatal(err)
	}

	// Exactly the state a crash after the first write leaves behind.
	if err := os.Remove(filepath.Join(s.Root(), "machines", "studio", "meta.json")); err != nil {
		t.Fatal(err)
	}

	got, meta, err := s.Snapshot("studio")
	if err != nil {
		t.Fatalf("a snapshot with no metadata should still read: %v", err)
	}
	if len(got.Inbox) != 1 {
		t.Errorf("the mail was lost: %+v", got)
	}
	if !meta.LastSync.IsZero() {
		t.Errorf("the sync time should be unknown, not invented: %v", meta.LastSync)
	}
}

func TestSnapshotRefusesInvalidData(t *testing.T) {
	s := open(t)
	bad := snapshot("studio")
	bad.User = "Not A Name"
	if err := s.PutSnapshot(bad, "cq/0.1", at); !errors.Is(err, fault.ErrParse) {
		t.Errorf("error = %v, want a parse fault", err)
	}
	if err := s.PutSnapshot(snapshot("studio"), "cq/0.1", time.Time{}); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("a zero timestamp should be internal, got %v", err)
	}
}

func TestSnapshotOfAnUnknownMachine(t *testing.T) {
	s := open(t)
	if _, _, err := s.Snapshot("never-synced"); !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("error = %v, want not-found", err)
	}
	if _, _, err := s.Snapshot("Not A Machine"); !errors.Is(err, fault.ErrParse) {
		t.Errorf("an invalid id should be a parse fault, got %v", err)
	}
}

func TestSnapshotRefusesACorruptFile(t *testing.T) {
	s := open(t)
	if err := s.PutSnapshot(snapshot("studio"), "cq/0.1", at); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root(), "machines", "studio", "snapshot.json")
	if err := os.WriteFile(path, []byte(`{"machine":"studio","user":"","taken_at":"2026-07-24T18:31:04Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Snapshot("studio"); !errors.Is(err, fault.ErrParse) {
		t.Errorf("error = %v, want a parse fault rather than a half-sensible read", err)
	}
}

func TestMachinesListsAndIgnoresJunk(t *testing.T) {
	s := open(t)
	for _, m := range []protocol.MachineID{"studio", "laptop"} {
		if err := s.PutSnapshot(snapshot(m), "cq/0.1", at); err != nil {
			t.Fatal(err)
		}
	}
	// Something else left things in the store; a listing must not fail over it.
	if err := os.MkdirAll(filepath.Join(s.Root(), "machines", "Not A Machine"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Root(), "machines", "stray.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := s.Machines()
	if err != nil {
		t.Fatalf("Machines: %v", err)
	}
	if len(got) != 2 || got[0] != "laptop" || got[1] != "studio" {
		t.Errorf("machines = %v, want [laptop studio]", got)
	}
}

func TestQueueLifecycle(t *testing.T) {
	s := open(t)

	read, err := s.Enqueue("studio", protocol.OpRead, protocol.Args{PUID: 41}, at)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	reply, err := s.Enqueue("studio", protocol.OpReply,
		protocol.Args{PUID: 41, Subject: "RE", Body: "yes"}, at)
	if err != nil {
		t.Fatal(err)
	}
	elsewhere, err := s.Enqueue("laptop", protocol.OpRead, protocol.Args{PUID: 7}, at)
	if err != nil {
		t.Fatal(err)
	}

	if read.Seq != 1 || reply.Seq != 2 || elsewhere.Seq != 3 {
		t.Errorf("sequences = %d %d %d, want 1 2 3", read.Seq, reply.Seq, elsewhere.Seq)
	}
	if read.ID == reply.ID {
		t.Errorf("two actions share an id")
	}

	pending, err := s.Pending("studio")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 || pending[0].ID != read.ID || pending[1].ID != reply.ID {
		t.Fatalf("pending = %+v, want the two studio actions in order", pending)
	}

	// Collected, but not yet reported: still pending, so a lost sync does not
	// strand the user's reply.
	if err := s.MarkSent([]protocol.ActionID{read.ID, reply.ID}, at); err != nil {
		t.Fatal(err)
	}
	pending, err = s.Pending("studio")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Errorf("a sent action should still be re-delivered, got %d", len(pending))
	}

	done := at.Add(time.Second)
	err = s.Complete([]protocol.Result{
		{ActionID: read.ID, OK: true, At: done},
		{ActionID: reply.ID, OK: false, Error: `no such user "carol"`, At: done},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	pending, err = s.Pending("studio")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("settled actions should not be re-delivered, got %+v", pending)
	}

	entries, err := s.Queue()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("queue holds %d entries, want 3", len(entries))
	}
	if entries[0].State != store.Done {
		t.Errorf("read state = %q, want done", entries[0].State)
	}
	if entries[1].State != store.Failed || !strings.Contains(entries[1].Error, "carol") {
		t.Errorf("reply entry = %+v, want failed with its reason", entries[1])
	}
	if entries[2].State != store.Queued {
		t.Errorf("the other machine's action was disturbed: %+v", entries[2])
	}
}

func TestCompleteIsIdempotentAndToleratesStaleResults(t *testing.T) {
	s := open(t)
	a, err := s.Enqueue("studio", protocol.OpRead, protocol.Args{PUID: 1}, at)
	if err != nil {
		t.Fatal(err)
	}
	done := at.Add(time.Second)
	first := []protocol.Result{{ActionID: a.ID, OK: true, At: done}}
	if err := s.Complete(first); err != nil {
		t.Fatal(err)
	}

	// A re-delivered result must not overwrite the settled one.
	later := []protocol.Result{{ActionID: a.ID, OK: false, Error: "changed its mind", At: done.Add(time.Minute)}}
	if err := s.Complete(later); err != nil {
		t.Fatal(err)
	}
	entries, err := s.Queue()
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].State != store.Done {
		t.Errorf("state = %q, want the first result to stand", entries[0].State)
	}

	// A result for an action the store no longer has is not an error: refusing
	// the batch would strand every other action in it.
	stale := []protocol.Result{{ActionID: protocol.ActionID(strings.Repeat("f", 32)), OK: true, At: done}}
	if err := s.Complete(stale); err != nil {
		t.Errorf("a stale result should be ignored, got %v", err)
	}
}

func TestCompleteRefusesADishonestResult(t *testing.T) {
	s := open(t)
	err := s.Complete([]protocol.Result{{ActionID: protocol.ActionID(strings.Repeat("a", 32)), OK: false, At: at}})
	if !errors.Is(err, fault.ErrParse) {
		t.Errorf("a failure with no reason should be refused, got %v", err)
	}
}

func TestPruneKeepsWhatHasNotHappened(t *testing.T) {
	s := open(t)
	settled, err := s.Enqueue("studio", protocol.OpRead, protocol.Args{PUID: 1}, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enqueue("studio", protocol.OpRead, protocol.Args{PUID: 2}, at); err != nil {
		t.Fatal(err)
	}
	done := at.Add(time.Second)
	if err := s.Complete([]protocol.Result{{ActionID: settled.ID, OK: true, At: done}}); err != nil {
		t.Fatal(err)
	}

	// Too early: nothing goes.
	n, err := s.Prune(done)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("pruned %d entries, want 0", n)
	}

	n, err = s.Prune(done.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("pruned %d entries, want 1", n)
	}
	entries, err := s.Queue()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].State != store.Queued {
		t.Errorf("an unapplied action was pruned: %+v", entries)
	}
}

// TestConcurrentEnqueueAssignsDistinctSequences is what keeps two browser tabs
// from writing the same queue file.
func TestConcurrentEnqueueAssignsDistinctSequences(t *testing.T) {
	s := open(t)
	const n = 24

	var wg sync.WaitGroup
	seqs := make([]uint64, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, err := s.Enqueue("studio", protocol.OpRead, protocol.Args{PUID: i}, at)
			seqs[i], errs[i] = a.Seq, err
		}()
	}
	wg.Wait()

	seen := map[uint64]bool{}
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("Enqueue: %v", errs[i])
		}
		if seen[seqs[i]] {
			t.Fatalf("sequence %d was assigned twice", seqs[i])
		}
		seen[seqs[i]] = true
	}
	entries, err := s.Queue()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != n {
		t.Errorf("queue holds %d entries, want %d", len(entries), n)
	}
}

func TestQueueRefusesACorruptEntry(t *testing.T) {
	s := open(t)
	if _, err := s.Enqueue("studio", protocol.OpRead, protocol.Args{PUID: 1}, at); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(s.Root(), "queue"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("unexpected queue contents: %v", err)
	}
	path := filepath.Join(s.Root(), "queue", entries[0].Name())
	if err := os.WriteFile(path, []byte(`{"action":{},"state":"nonsense"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Queue(); !errors.Is(err, fault.ErrParse) {
		t.Errorf("error = %v, want a parse fault", err)
	}
}

func TestEnqueueValidatesItsArguments(t *testing.T) {
	s := open(t)
	if _, err := s.Enqueue("studio", protocol.OpRead, protocol.Args{PUID: 1}, time.Time{}); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("a zero timestamp should be internal, got %v", err)
	}
	if _, err := s.Enqueue("studio", "detonate", protocol.Args{}, at); !errors.Is(err, fault.ErrParse) {
		t.Errorf("an unknown op should be a parse fault, got %v", err)
	}
	if _, err := s.Enqueue("Not A Machine", protocol.OpRead, protocol.Args{PUID: 1}, at); !errors.Is(err, fault.ErrParse) {
		t.Errorf("an invalid machine should be a parse fault, got %v", err)
	}
	if _, err := s.Pending("Not A Machine"); !errors.Is(err, fault.ErrParse) {
		t.Errorf("Pending should validate its machine, got %v", err)
	}
}

// TestAQueuedActionCarriesNoTimes pins the encoding, not the Go value.
//
// The bug this replaces was invisible from Go: a queued entry had zero times,
// Validate was satisfied, and every test passed — while the JSON served to the
// browser said "sent_at":"0001-01-01T00:00:00Z", which reads as an action sent
// two thousand years ago rather than one not sent yet.
func TestALegacyZeroTimeIsNotServedOnward(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	action, err := st.Enqueue("studio", protocol.OpRead, protocol.Args{PUID: 1}, at)
	if err != nil {
		t.Fatal(err)
	}

	// Rewrite the file the way an older cq wrote it: explicit zero times.
	found, err := filepath.Glob(filepath.Join(dir, "queue", "*.json"))
	if err != nil || len(found) != 1 {
		t.Fatalf("queue files = %v (%v)", found, err)
	}
	legacy := `{"action":` + mustJSON(t, action) +
		`,"state":"queued","sent_at":"0001-01-01T00:00:00Z","completed":"0001-01-01T00:00:00Z"}`
	if err := os.WriteFile(found[0], []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, err := st.Queue()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].SentAt != nil || entries[0].Completed != nil {
		t.Errorf("a legacy zero time survived the read: %+v", entries[0])
	}

	// And it does not come back out on the wire either.
	out := mustJSON(t, entries[0])
	for _, field := range []string{"sent_at", "completed"} {
		if strings.Contains(out, field) {
			t.Errorf("re-encoding carried %s onward: %s", field, out)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAQueuedActionCarriesNoTimes(t *testing.T) {
	queued, err := json.Marshal(store.Entry{
		Action: protocol.Action{
			ID: protocol.ActionID(strings.Repeat("b", 32)), Seq: 1, Machine: "studio",
			Op: protocol.OpRead, Args: protocol.Args{PUID: 1}, Queued: at,
		},
		State: store.Queued,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"sent_at", "completed"} {
		if strings.Contains(string(queued), field) {
			t.Errorf("a queued action carries %s: %s", field, queued)
		}
	}
}

func TestEntryValidation(t *testing.T) {
	sound := protocol.Action{
		ID: protocol.ActionID(strings.Repeat("a", 32)), Seq: 1, Machine: "studio",
		Op: protocol.OpRead, Args: protocol.Args{PUID: 1}, Queued: at,
	}
	for _, tc := range []struct {
		name string
		e    store.Entry
		want string
	}{
		{"unknown state", store.Entry{Action: sound, State: "nonsense"}, "unknown state"},
		{"failed with no reason", store.Entry{Action: sound, State: store.Failed, Completed: &at}, "carries no reason"},
		{"done with an error", store.Entry{Action: sound, State: store.Done, Completed: &at, Error: "hmm"}, "carries an error"},
		{"done with no time", store.Entry{Action: sound, State: store.Done}, "no completion time"},
		// A file written before these times were optional holds an explicit
		// zero, which means the same thing as nothing at all.
		{"done with an explicit zero time", store.Entry{Action: sound, State: store.Done,
			Completed: new(time.Time)}, "no completion time"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.e.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, should mention %q", err, tc.want)
			}
		})
	}
	if err := (store.Entry{Action: sound, State: store.Queued}).Validate(); err != nil {
		t.Errorf("a sound queued entry was rejected: %v", err)
	}
}

func TestStateHelpers(t *testing.T) {
	for _, s := range []store.State{store.Queued, store.Sent, store.Done, store.Failed} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	if store.State("nonsense").Valid() {
		t.Errorf("an invented state should not be valid")
	}
	if !store.Queued.Pending() || !store.Sent.Pending() {
		t.Errorf("queued and sent are both still pending")
	}
	if store.Done.Pending() || store.Failed.Pending() {
		t.Errorf("settled states are not pending")
	}
}
