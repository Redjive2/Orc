package store_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/mailman/internal/mail"
	"orc/mailman/internal/store"
)

// TestConcurrentDeliveryLosesNothing is the test the whole storage design
// exists to pass. Sixty-four goroutines deliver to one mailbox at once; every
// message must arrive exactly once, with a puid that belongs to it alone.
func TestConcurrentDeliveryLosesNothing(t *testing.T) {
	h := newHarness(t, "boss", "alice")
	alice := h.name("alice")

	const n = 64
	ids := make([]mail.ID, n)
	for i := range n {
		ids[i] = h.message("boss", []string{"alice"}, fmt.Sprintf("s%d", i), "b").ID()
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	puids := make([]int, n)
	start := make(chan struct{})

	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release them together, to maximise contention
			puids[i], errs[i] = h.Deliver(alice, ids[i])
		}()
	}
	close(start)
	wg.Wait()

	seen := make(map[int]int, n)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("delivery %d failed: %v", i, err)
		}
		if other, dup := seen[puids[i]]; dup {
			t.Fatalf("puid %d was given to both message %d and message %d", puids[i], other, i)
		}
		seen[puids[i]] = i
	}

	st, err := h.Replay(alice)
	if err != nil {
		t.Fatalf("Replay after concurrent delivery: %v", err)
	}
	if st.Len() != n {
		t.Fatalf("mailbox holds %d messages, want %d", st.Len(), n)
	}
	if st.NextPUID() != n {
		t.Errorf("NextPUID = %d, want %d", st.NextPUID(), n)
	}
	for i, id := range ids {
		if _, ok := st.Lookup(id); !ok {
			t.Errorf("message %d is missing from the mailbox", i)
		}
	}
}

// TestConcurrentMarksConverge: several agents marking the same mail read,
// archived, and pruned at once must leave a journal that still replays.
func TestConcurrentMarksConverge(t *testing.T) {
	h := newHarness(t, "boss", "alice")
	alice := h.name("alice")

	const n = 16
	ids := make([]mail.ID, n)
	for i := range n {
		ids[i] = h.message("boss", []string{"alice"}, fmt.Sprintf("s%d", i), "b").ID()
		if _, err := h.Deliver(alice, ids[i]); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, id := range ids {
		for range 4 { // four agents racing on each message
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_ = h.Mark(alice, id, store.OpRead)
				_ = h.Mark(alice, id, store.OpArchive)
			}()
		}
	}
	close(start)
	wg.Wait()

	st, err := h.Replay(alice)
	if err != nil {
		t.Fatalf("Replay after concurrent marks: %v", err)
	}
	for i, id := range ids {
		e, ok := st.Lookup(id)
		if !ok {
			t.Fatalf("message %d vanished", i)
		}
		if e.Unread() {
			t.Errorf("message %d is still unread", i)
		}
		if !e.Archived {
			t.Errorf("message %d is not archived", i)
		}
	}
}

// TestConcurrentReceiptsAllLand: two recipients reading the same message at the
// same moment must both be recorded. Receipts are per-user files precisely so
// this cannot contend.
func TestConcurrentReceiptsAllLand(t *testing.T) {
	h := newHarness(t, "boss", "alice", "bob", "carol")
	m := h.message("boss", []string{"alice", "bob", "carol"}, "s", "b")

	readers := []string{"alice", "bob", "carol"}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, who := range readers {
		for range 3 { // each reader also races with itself
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_ = h.PutReceipt(m.ID(), h.name(who), h.clock.Now())
			}()
		}
	}
	close(start)
	wg.Wait()

	got, err := h.Receipts(m.ID())
	if err != nil {
		t.Fatalf("Receipts: %v", err)
	}
	if len(got) != len(readers) {
		t.Fatalf("got %d receipts, want %d", len(got), len(readers))
	}
	for i, want := range []string{"alice", "bob", "carol"} {
		if got[i].User.String() != want {
			t.Errorf("receipt %d is for %s, want %s", i, got[i].User, want)
		}
	}
}

// TestConcurrentConvoAppendsAreOrdered: two replies at the same instant must
// get distinct indices rather than both claiming the same position.
func TestConcurrentConvoAppendsAreOrdered(t *testing.T) {
	h := newHarness(t, "boss", "alice")
	root := h.message("boss", []string{"alice"}, "work", "start")
	if _, err := h.OpenConvo(root, "work"); err != nil {
		t.Fatal(err)
	}

	const n = 32
	ids := make([]mail.ID, n)
	for i := range n {
		ids[i] = h.message("alice", []string{"boss"}, "RE: work", fmt.Sprintf("reply %d", i)).ID()
	}

	var wg sync.WaitGroup
	indices := make([]int, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			indices[i], errs[i] = h.AddToConvo(root.ID(), ids[i])
		}()
	}
	close(start)
	wg.Wait()

	seen := make(map[int]bool, n)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("append %d failed: %v", i, err)
		}
		if seen[indices[i]] {
			t.Fatalf("index %d was handed out twice", indices[i])
		}
		seen[indices[i]] = true
	}

	c, err := h.Convo(root.ID())
	if err != nil {
		t.Fatalf("Convo after concurrent appends: %v", err)
	}
	if c.Len() != n+1 { // the root plus every reply
		t.Fatalf("the conversation holds %d messages, want %d", c.Len(), n+1)
	}
}

// TestConcurrentProcessesShareTheStore is the only honest test of the file
// lock: goroutines share one flock owner, so they exercise the mutex rather
// than the lock. This re-executes the test binary as several real processes.
func TestConcurrentProcessesShareTheStore(t *testing.T) {
	if os.Getenv("MAILMAN_TEST_CHILD") != "" {
		runChild(t)
		return
	}

	root := t.TempDir()
	c := clock.NewFake(epoch, time.Millisecond)
	s, err := store.Open(root, c)
	if err != nil {
		t.Fatal(err)
	}

	alice, err := user.Parse("alice")
	if err != nil {
		t.Fatal(err)
	}
	key, err := user.NewKey(&entropy{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(alice, key); err != nil {
		t.Fatal(err)
	}

	const children = 8
	const each = 8

	var wg sync.WaitGroup
	output := make([]string, children)
	failures := make([]error, children)

	for i := range children {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=TestConcurrentProcessesShareTheStore", "-test.v=false")
			cmd.Env = append(os.Environ(),
				"MAILMAN_TEST_CHILD=1",
				"MAILMAN_TEST_ROOT="+root,
				"MAILMAN_TEST_SEED="+strconv.Itoa(i),
				"MAILMAN_TEST_COUNT="+strconv.Itoa(each),
			)
			out, err := cmd.CombinedOutput()
			output[i], failures[i] = string(out), err
		}()
	}
	wg.Wait()

	for i, err := range failures {
		if err != nil {
			t.Fatalf("child %d failed: %v\n%s", i, err, output[i])
		}
	}

	// Every child's deliveries must be present, each with a puid of its own.
	st, err := s.Replay(alice)
	if err != nil {
		t.Fatalf("Replay after %d processes: %v", children, err)
	}
	if want := children * each; st.Len() != want {
		t.Fatalf("mailbox holds %d messages, want %d — mail was lost to a race", st.Len(), want)
	}

	seen := make(map[int]string, st.Len())
	for _, e := range st.Entries() {
		if other, dup := seen[e.PUID]; dup {
			t.Fatalf("puid %d belongs to both %s and %s", e.PUID, other, e.MID)
		}
		seen[e.PUID] = e.MID.String()
	}
	if st.NextPUID() != st.Len() {
		t.Errorf("NextPUID = %d with %d messages; puids are not contiguous", st.NextPUID(), st.Len())
	}
}

// runChild is the other half of the process test: it delivers a few messages
// into a store several of its siblings are writing to at the same moment.
func runChild(t *testing.T) {
	t.Helper()
	root := os.Getenv("MAILMAN_TEST_ROOT")
	seed, err := strconv.Atoi(os.Getenv("MAILMAN_TEST_SEED"))
	if err != nil {
		t.Fatalf("bad seed: %v", err)
	}
	count, err := strconv.Atoi(os.Getenv("MAILMAN_TEST_COUNT"))
	if err != nil {
		t.Fatalf("bad count: %v", err)
	}

	s, err := store.Open(root, clock.Real{})
	if err != nil {
		t.Fatalf("child Open: %v", err)
	}
	alice, err := user.Parse("alice")
	if err != nil {
		t.Fatal(err)
	}
	boss, err := user.Parse("boss")
	if err != nil {
		t.Fatal(err)
	}

	// Each child's ids come from a distinct entropy stream, so a collision here
	// would be a real defect rather than a test artefact.
	//
	// The seed goes in bits 24..31 because an id takes only the low four bytes
	// of the counter: a seed placed any higher would be invisible to it, and
	// every child would mint the same ids. That is not hypothetical — the first
	// version of this test did exactly that, and Put's write-once guard is what
	// caught it.
	e := &entropy{n: uint64(seed) << 24}

	for i := range count {
		at := clock.Real{}.Now()
		id, err := mail.NewID(at, e)
		if err != nil {
			t.Fatalf("child NewID: %v", err)
		}
		m, err := mail.New(id, mail.Ordinary, boss, []user.Name{alice}, nil,
			fmt.Sprintf("child %d message %d", seed, i), mail.ID{}, 0, at, []byte("body"))
		if err != nil {
			t.Fatalf("child New: %v", err)
		}
		if err := s.Put(m); err != nil {
			t.Fatalf("child Put: %v", err)
		}
		if _, err := s.Deliver(alice, id); err != nil {
			t.Fatalf("child Deliver: %v", err)
		}
	}
}

// TestPruneRacingOpen: a reader must either see the whole message or a clean
// not-found, never a half-read file.
func TestPruneRacingOpen(t *testing.T) {
	h := newHarness(t, "boss", "alice")
	alice := h.name("alice")

	const n = 24
	ids := make([]mail.ID, n)
	for i := range n {
		ids[i] = h.message("boss", []string{"alice"}, fmt.Sprintf("s%d", i), strings.Repeat("body ", 200)).ID()
		if _, err := h.Deliver(alice, ids[i]); err != nil {
			t.Fatal(err)
		}
		if err := h.Mark(alice, ids[i], store.OpArchive); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	bad := make(chan error, n*2)

	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := h.Delete(id); err != nil {
				bad <- fmt.Errorf("delete: %w", err)
			}
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := h.Get(id)
			// Either outcome is correct. A parse fault would mean a reader saw a
			// partially written or partially removed file, which is the failure
			// the atomic-write discipline exists to prevent.
			if err != nil && !errors.Is(err, fault.ErrNotFound) {
				bad <- fmt.Errorf("get: %w", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(bad)

	for err := range bad {
		t.Errorf("racing prune and open: %v", err)
	}
}

// TestJournalStaysParseableUnderContention writes hard from many goroutines and
// then checks the file itself, rather than only the folded state: interleaved
// appends must never produce a torn line.
func TestJournalStaysParseableUnderContention(t *testing.T) {
	h := newHarness(t, "boss", "alice")
	alice := h.name("alice")

	const n = 100
	ids := make([]mail.ID, n)
	for i := range n {
		ids[i] = h.message("boss", []string{"alice"}, fmt.Sprintf("s%d", i), "b").ID()
	}

	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.Deliver(alice, ids[i]); err != nil {
				return
			}
			_ = h.Mark(alice, ids[i], store.OpRead)
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(h.JournalPathFor("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatal("the journal does not end on a complete line")
	}
	for i, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if !strings.HasPrefix(line, `{"op":`) || !strings.HasSuffix(line, "}") {
			t.Fatalf("journal line %d is torn: %q", i+1, line)
		}
	}

	st, err := h.Replay(alice)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if st.Len() != n {
		t.Errorf("mailbox holds %d messages, want %d", st.Len(), n)
	}
	if st.Skipped() != 0 {
		t.Errorf("%d bytes were skipped; nothing was interrupted", st.Skipped())
	}
}

// TestStoreSurvivesAConcurrentOpen: several processes opening a fresh store at
// once must not race each other into a broken version marker.
func TestStoreSurvivesAConcurrentOpen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")

	var wg sync.WaitGroup
	errs := make([]error, 16)
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = store.Open(root, clock.NewFake(epoch, time.Millisecond))
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent Open %d failed: %v", i, err)
		}
	}
}
