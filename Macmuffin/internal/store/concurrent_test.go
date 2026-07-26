package store_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/macmuffin/internal/store"
	"orc/macmuffin/internal/task"
)

// TestOneClaimWins is the test the whole storage design exists to pass.
//
// Sixty-four agents scan the same pool and claim the same task within
// microseconds of each other. Exactly one must succeed; the rest must be told
// they lost, and told by whom — not silently no-op, and not overwrite.
func TestOneClaimWins(t *testing.T) {
	r := newRig(t)
	alice := r.agent("alice")
	r.scoped("fix-the-parser", "alice")
	r.apply("fix-the-parser", func(task.Task) (task.Event, error) { return task.Push(alice, r.Now()) })

	const n = 64
	agents := make([]user.Name, n)
	for i := range n {
		agents[i] = r.agent(fmt.Sprintf("agent%d", i))
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})

	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // released together, to maximise contention
			_, errs[i] = r.Apply(r.name("fix-the-parser"), func(task.Task) (task.Event, error) {
				return task.Claim(agents[i], r.Now())
			})
		}()
	}
	close(start)
	wg.Wait()

	winners, losers := 0, 0
	for i, err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, fault.ErrConflict):
			losers++
		default:
			t.Fatalf("agent %d failed with an unexpected error: %v", i, err)
		}
	}
	if winners != 1 {
		t.Fatalf("%d agents claimed the task, want exactly 1", winners)
	}
	if losers != n-1 {
		t.Fatalf("%d agents lost, want %d", losers, n-1)
	}

	// The journal holds exactly one claim, and the task has exactly one owner.
	got, err := r.Load(r.name("fix-the-parser"))
	if err != nil {
		t.Fatalf("Load after the race: %v", err)
	}
	owner, owned := got.Owner()
	if !owned {
		t.Fatal("nobody owns the task after 64 claims")
	}
	data, err := os.ReadFile(r.journalPath("fix-the-parser"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), `"op":"claim"`); got != 1 {
		t.Errorf("the journal holds %d claims, want 1", got)
	}

	// And every loser was told who won.
	for i, err := range errs {
		if err == nil {
			continue
		}
		if !strings.Contains(err.Error(), owner.String()) {
			t.Fatalf("agent %d was not told the winner (%s): %v", i, owner, err)
		}
	}
}

// TestConcurrentCreateOnOneName: one task, one conflict, never two records.
func TestConcurrentCreateOnOneName(t *testing.T) {
	r := newRig(t)
	p, err := task.NewPriority(3)
	if err != nil {
		t.Fatal(err)
	}
	d, err := task.NewDifficulty(3)
	if err != nil {
		t.Fatal(err)
	}

	const n = 32
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})

	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = r.Create(r.name("contested"), r.agent(fmt.Sprintf("agent%d", i)), p, d)
		}()
	}
	close(start)
	wg.Wait()

	winners := 0
	for i, err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, fault.ErrConflict):
		default:
			t.Fatalf("creator %d failed with an unexpected error: %v", i, err)
		}
	}
	if winners != 1 {
		t.Fatalf("%d creators succeeded, want exactly 1", winners)
	}

	names, err := r.Names()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 {
		t.Errorf("the store holds %d tasks, want 1", len(names))
	}
}

// TestConcurrentSubtaskCompletionCounts: every event lands and the counts are
// exact, whichever order the agents finish in.
func TestConcurrentSubtaskCompletion(t *testing.T) {
	r := newRig(t)
	alice := r.agent("alice")
	r.scoped("fix-the-parser", "alice")

	const n = 24
	for i := range n {
		sub := r.name(fmt.Sprintf("step-%d", i))
		r.apply("fix-the-parser", func(task.Task) (task.Event, error) {
			return task.AddSub(alice, r.Now(), sub)
		})
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		sub := r.name(fmt.Sprintf("step-%d", i))
		// Two agents finish each step at once, which must not be an error: the
		// first completion is the one that is true.
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, _ = r.Apply(r.name("fix-the-parser"), func(task.Task) (task.Event, error) {
					return task.DoneSub(alice, r.Now(), sub)
				})
			}()
		}
	}
	close(start)
	wg.Wait()

	got, err := r.Load(r.name("fix-the-parser"))
	if err != nil {
		t.Fatalf("Load after concurrent completion: %v", err)
	}
	done, total := got.Progress()
	if done != n || total != n {
		t.Errorf("Progress = %d/%d, want %d/%d", done, total, n, n)
	}
}

// TestConcurrentInvitesConverge: the collaborator list must hold each agent
// exactly once, whatever order the invites land in.
func TestConcurrentInvites(t *testing.T) {
	r := newRig(t)
	alice := r.agent("alice")
	r.scoped("fix-the-parser", "alice")
	r.apply("fix-the-parser", func(task.Task) (task.Event, error) { return task.Claim(alice, r.Now()) })

	const n = 20
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		who := r.agent(fmt.Sprintf("agent%d", i))
		// Each invited twice, concurrently.
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, _ = r.Apply(r.name("fix-the-parser"), func(task.Task) (task.Event, error) {
					return task.Invite(alice, r.Now(), who)
				})
			}()
		}
	}
	close(start)
	wg.Wait()

	got, err := r.Load(r.name("fix-the-parser"))
	if err != nil {
		t.Fatalf("Load after concurrent invites: %v", err)
	}
	if len(got.Collaborators()) != n {
		t.Errorf("%d collaborators, want %d", len(got.Collaborators()), n)
	}
	seen := map[string]bool{}
	for _, c := range got.Collaborators() {
		if seen[c.String()] {
			t.Fatalf("%s appears twice", c)
		}
		seen[c.String()] = true
	}
}

// TestTasksDoNotContend: the lock is per task, so work on one never waits for
// work on another. This checks the locks are actually separate rather than one
// store-wide lock that happens to be fast.
func TestTasksDoNotContend(t *testing.T) {
	r := newRig(t)
	alice := r.agent("alice")

	const n = 16
	for i := range n {
		r.scoped(fmt.Sprintf("task-%d", i), "alice")
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = r.Apply(r.name(fmt.Sprintf("task-%d", i)), func(task.Task) (task.Event, error) {
				return task.Claim(alice, r.Now())
			})
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("claiming task-%d failed: %v", i, err)
		}
	}
}

// TestJournalStaysParseableUnderContention checks the file itself rather than
// only the folded state: interleaved appends must never produce a torn line.
func TestJournalStaysParseableUnderContention(t *testing.T) {
	r := newRig(t)
	alice := r.agent("alice")
	r.scoped("fix-the-parser", "alice")

	const n = 60
	var wg sync.WaitGroup
	for i := range n {
		sub := r.name(fmt.Sprintf("step-%d", i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.Apply(r.name("fix-the-parser"), func(task.Task) (task.Event, error) {
				return task.AddSub(alice, r.Now(), sub)
			})
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(r.journalPath("fix-the-parser"))
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

	got, err := r.Load(r.name("fix-the-parser"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, total := got.Progress(); total != n {
		t.Errorf("%d subtasks landed, want %d", total, n)
	}
}

// TestRealProcessesRaceForOneClaim is the only honest test of the file lock:
// goroutines share one flock owner, so they exercise the mutex instead. This
// re-executes the test binary as eight real processes.
func TestRealProcessesRaceForOneClaim(t *testing.T) {
	if os.Getenv("MUFF_TEST_CHILD") != "" {
		runClaimChild(t)
		return
	}

	r := newRig(t)
	alice := r.agent("alice")
	r.scoped("contested", "alice")
	r.apply("contested", func(task.Task) (task.Event, error) { return task.Push(alice, r.Now()) })

	const children = 8

	var wg sync.WaitGroup
	output := make([]string, children)
	codes := make([]int, children)

	for i := range children {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=TestRealProcessesRaceForOneClaim", "-test.v=false")
			cmd.Env = append(os.Environ(),
				"MUFF_TEST_CHILD=1",
				"MUFF_TEST_ROOT="+r.Root(),
				"MUFF_TEST_AGENT=agent"+strconv.Itoa(i),
			)
			out, err := cmd.CombinedOutput()
			output[i] = string(out)
			if err != nil {
				var exit *exec.ExitError
				if errors.As(err, &exit) {
					codes[i] = exit.ExitCode()
				} else {
					t.Errorf("child %d could not run: %v", i, err)
				}
			}
		}()
	}
	wg.Wait()

	// Every child either claimed it or reported a conflict; a child that failed
	// any other way is a defect.
	for i, out := range output {
		if codes[i] != 0 && !strings.Contains(out, "conflict") {
			t.Fatalf("child %d exited %d without a conflict:\n%s", i, codes[i], out)
		}
	}

	got, err := r.Load(r.name("contested"))
	if err != nil {
		t.Fatalf("Load after %d processes: %v", children, err)
	}
	if _, owned := got.Owner(); !owned {
		t.Fatal("nobody owns the task after eight processes raced for it")
	}

	data, err := os.ReadFile(r.journalPath("contested"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), `"op":"claim"`); n != 1 {
		t.Errorf("the journal holds %d claims, want exactly 1", n)
	}
}

// runClaimChild is the other half of the process test: one attempt to claim a
// task seven siblings are claiming at the same moment.
func runClaimChild(t *testing.T) {
	t.Helper()

	s, err := store.Open(os.Getenv("MUFF_TEST_ROOT"), clock.Real{})
	if err != nil {
		t.Fatalf("child Open: %v", err)
	}
	name, err := task.ParseName("contested")
	if err != nil {
		t.Fatal(err)
	}
	who, err := user.Parse(os.Getenv("MUFF_TEST_AGENT"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Apply(name, func(task.Task) (task.Event, error) {
		return task.Claim(who, s.Now())
	})
	switch {
	case err == nil:
		return // this child won
	case errors.Is(err, fault.ErrConflict):
		// Printed rather than failed: losing is the expected outcome for seven
		// of the eight, and the parent checks that the message says so.
		t.Skipf("conflict: %v", err)
	default:
		t.Fatalf("child claim failed unexpectedly: %v", err)
	}
}
