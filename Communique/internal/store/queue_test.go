package store_test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
	"orc/cq/internal/store"
)

// settled queues an action and drives it to one settled state.
func settled(t *testing.T, s *store.Store, op protocol.Op, args protocol.Args, result protocol.Result) protocol.Action {
	t.Helper()
	action, err := s.Enqueue("studio", op, args, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSent([]protocol.ActionID{action.ID}, at); err != nil {
		t.Fatal(err)
	}
	result.ActionID, result.At = action.ID, at
	if err := s.Complete([]protocol.Result{result}); err != nil {
		t.Fatal(err)
	}
	return action
}

func failedAction(t *testing.T, s *store.Store, op protocol.Op) protocol.Action {
	t.Helper()
	return settled(t, s, op, protocol.Args{PUID: 1},
		protocol.Result{OK: false, Error: "mailman said no"})
}

func doubtfulAction(t *testing.T, s *store.Store, op protocol.Op, args protocol.Args) protocol.Action {
	t.Helper()
	return settled(t, s, op, args,
		protocol.Result{OK: false, InDoubt: true, Error: "interrupted; it may or may not have been applied"})
}

// TestRetryMintsANewIdentifier is the whole design, and it is not cosmetic.
//
// The agent journals every action id it touches and skips the ones it
// recognises — that is what makes delivery exactly-once in effect. Re-queueing
// the same id would produce an action that is collected, sent, and then
// deliberately ignored: something that looks like a retry and never is one.
func TestRetryMintsANewIdentifier(t *testing.T) {
	s := open(t)
	first := failedAction(t, s, protocol.OpRead)

	again, err := s.Retry(first.ID, at)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID == first.ID {
		t.Fatal("the retry reused the action id, so the agent would skip it as already applied")
	}
	if again.Op != first.Op || again.Args.PUID != first.Args.PUID || again.Machine != first.Machine {
		t.Errorf("the retry is not the same request: %+v vs %+v", again, first)
	}
	if again.Seq <= first.Seq {
		t.Errorf("the retry should be later in the queue: %d after %d", again.Seq, first.Seq)
	}
}

// The original stays exactly as it was: it is the record that this was tried and
// refused, and tidying it away would lose the only evidence of what happened.
func TestRetryLeavesTheHistoryAlone(t *testing.T) {
	s := open(t)
	first := failedAction(t, s, protocol.OpRead)
	if _, err := s.Retry(first.ID, at); err != nil {
		t.Fatal(err)
	}

	got, err := s.Entry(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.Failed || got.Error == "" {
		t.Errorf("the original entry was altered: %+v", got)
	}
}

// TestAnInterruptedSendIsNotRepeated is the one that matters.
//
// An action in doubt was started and its end never recorded, so it may already
// have arrived. Retrying a send there means a real person reads the same message
// twice, and cq cannot tell whether that will happen.
func TestAnInterruptedSendIsNotRepeated(t *testing.T) {
	s := open(t)
	for _, op := range []protocol.Op{protocol.OpSend, protocol.OpReply} {
		args := protocol.Args{PUID: 1, Subject: "s", Body: "b"}
		if op == protocol.OpSend {
			args = protocol.Args{To: []string{"bob"}, Subject: "s", Body: "b"}
		}
		doubtful := doubtfulAction(t, s, op, args)

		_, err := s.Retry(doubtful.ID, at)
		if !errors.Is(err, fault.ErrConflict) {
			t.Fatalf("%s in doubt was retried: %v", op, err)
		}
		// And the refusal points at the one thing that settles it, since the
		// operator can check and cq cannot.
		if !strings.Contains(err.Error(), "sent mail") {
			t.Errorf("the refusal should say what to check: %v", err)
		}
	}
}

// An interrupted action that cannot have happened twice is retryable, because
// refusing it would leave the operator stuck for no reason.
func TestAnInterruptedIdempotentActionMayBeRetried(t *testing.T) {
	s := open(t)
	for _, op := range []protocol.Op{protocol.OpRead, protocol.OpArchive} {
		doubtful := doubtfulAction(t, s, op, protocol.Args{PUID: 1})
		if _, err := s.Retry(doubtful.ID, at); err != nil {
			t.Errorf("%s in doubt should be retryable: %v", op, err)
		}
	}
}

func TestRetryRefusesWhatDoesNotNeedIt(t *testing.T) {
	s := open(t)

	done := settled(t, s, protocol.OpRead, protocol.Args{PUID: 1}, protocol.Result{OK: true})
	if _, err := s.Retry(done.ID, at); !errors.Is(err, fault.ErrConflict) {
		t.Errorf("a successful action was retried: %v", err)
	}

	// Still waiting to be collected, so it has not been tried at all.
	queued, err := s.Enqueue("studio", protocol.OpRead, protocol.Args{PUID: 2}, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Retry(queued.ID, at); !errors.Is(err, fault.ErrConflict) {
		t.Errorf("an untried action was retried: %v", err)
	}
}

func TestRetryOfSomethingThatDoesNotExist(t *testing.T) {
	s := open(t)
	_, err := s.Retry(protocol.ActionID(strings.Repeat("f", 32)), at)
	if !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("error = %v, want not found", err)
	}
}

func TestDropRemovesASettledAction(t *testing.T) {
	s := open(t)
	gone := failedAction(t, s, protocol.OpRead)

	if err := s.Drop(gone.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Entry(gone.ID); !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("the entry is still there: %v", err)
	}

	// Dropping it twice is not an error: the end state asked for is the one
	// already in place, and a queue view with a stale row would otherwise show
	// a button that fails.
	if err := s.Drop(gone.ID); err != nil {
		t.Errorf("a second drop should be quiet: %v", err)
	}
}

// TestAWaitingActionCanBeCancelled.
//
// It has never left this machine: no agent has seen it, nothing was attempted,
// and removing it means it simply never goes. Refusing that was the difference
// between a queue and a list of regrets — a message written by mistake could
// only be watched on its way out.
func TestAWaitingActionCanBeCancelled(t *testing.T) {
	s := open(t)

	queued, err := s.Enqueue("studio", protocol.OpSend,
		protocol.Args{To: []string{"bob"}, Subject: "oops", Body: "sent too soon"}, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Drop(queued.ID); err != nil {
		t.Fatalf("a waiting action would not cancel: %v", err)
	}
	if _, err := s.Entry(queued.ID); !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("the entry is still there: %v", err)
	}

	// And it is not handed to the next sync, which is the whole point.
	pending, err := s.Pending("studio")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range pending {
		if a.ID == queued.ID {
			t.Error("a cancelled action was still collected")
		}
	}
}

// TestDropRefusesAnActionWithTheAgent: it may be applying this second. Deleting
// it here would leave the agent doing something the server has forgotten, and
// then reporting a result for an action that no longer exists.
func TestDropRefusesAnActionWithTheAgent(t *testing.T) {
	s := open(t)

	queued, err := s.Enqueue("studio", protocol.OpRead, protocol.Args{PUID: 1}, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSent([]protocol.ActionID{queued.ID}, at); err != nil {
		t.Fatal(err)
	}

	err = s.Drop(queued.ID)
	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("an action already collected by a sync was dropped: %v", err)
	}
	if !strings.Contains(err.Error(), "wait for it to report") {
		t.Errorf("the refusal should say what to do instead: %v", err)
	}
	if _, err := s.Entry(queued.ID); err != nil {
		t.Errorf("the entry was removed anyway: %v", err)
	}
}

// TestADroppedActionDoesNotComeBack: the agent still reports a result for it,
// because it was collected before the drop. That report must not resurrect it.
func TestADroppedActionDoesNotComeBack(t *testing.T) {
	s := open(t)
	gone := failedAction(t, s, protocol.OpRead)
	if err := s.Drop(gone.ID); err != nil {
		t.Fatal(err)
	}

	err := s.Complete([]protocol.Result{{ActionID: gone.ID, OK: true, At: at}})
	if err != nil {
		t.Fatalf("a result for a dropped action should be ignored, not fail: %v", err)
	}
	if _, err := s.Entry(gone.ID); !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("the dropped action came back: %v", err)
	}
}

// TestIdempotenceIsDecidedPerOperation pins the judgement the retry rule rests
// on, so a new operation cannot inherit "safe to repeat" by accident.
func TestIdempotenceIsDecidedPerOperation(t *testing.T) {
	for _, tc := range []struct {
		op   protocol.Op
		want bool
	}{
		{protocol.OpRead, true},
		{protocol.OpArchive, true},
		{protocol.OpCC, true},
		{protocol.OpSend, false},
		{protocol.OpReply, false},
		{protocol.OpMakeDir, true},
		{protocol.OpWrite, false},
		{protocol.OpCreate, false},
		{protocol.OpDelete, false},
		{protocol.OpRemoveDir, false},
		{protocol.OpRemoveTree, false},
		{protocol.Op("something-new"), false},
	} {
		if got := tc.op.Idempotent(); got != tc.want {
			t.Errorf("%q.Idempotent() = %v, want %v", tc.op, got, tc.want)
		}
	}
}

// Every defined operation is classified, so adding one to Ops without deciding
// this fails here rather than silently defaulting.
func TestEveryOperationIsClassified(t *testing.T) {
	for _, op := range protocol.Ops {
		switch op {
		case protocol.OpRead, protocol.OpArchive, protocol.OpCC:
			if !op.Idempotent() {
				t.Errorf("%q should be idempotent", op)
			}
		case protocol.OpSend, protocol.OpReply:
			if op.Idempotent() {
				t.Errorf("%q must not be idempotent", op)
			}
		case protocol.OpMakeDir:
			// Making a directory that exists is making a directory.
			if !op.Idempotent() {
				t.Errorf("%q should be idempotent", op)
			}
		case protocol.OpWrite, protocol.OpCreate, protocol.OpDelete,
			protocol.OpRemoveDir, protocol.OpRemoveTree:
			// Not idempotent — the second application refuses rather than
			// repeating — but safe to retry, which is a different question and
			// the one `retryable` actually asks.
			if op.Idempotent() {
				t.Errorf("%q must not be idempotent", op)
			}
		case protocol.OpTaskDescribe, protocol.OpTaskDescribeClear,
			protocol.OpTaskScope, protocol.OpTaskWorktree, protocol.OpTaskStatus,
			protocol.OpTaskAssign, protocol.OpTaskInvite:
			// Each sets a value to what was asked for rather than stepping it, so
			// applying it twice lands in the same place — including a description,
			// which is prose but is still a value: the same words twice are the
			// same words.
			if !op.Idempotent() {
				t.Errorf("%q should be idempotent", op)
			}
		case protocol.OpTaskCreate, protocol.OpTaskSubtask, protocol.OpTaskPush,
			protocol.OpTaskClaim, protocol.OpTaskKick, protocol.OpTaskLeave,
			protocol.OpTaskComplete, protocol.OpTaskDelete:
			// Transitions with a precondition. A second push, claim, or complete
			// finds the task already moved and refuses — the right answer, but not
			// the same answer, so none may be retried after an unknown outcome.
			if op.Idempotent() {
				t.Errorf("%q must not be idempotent", op)
			}
		case protocol.OpOrcAssignRole, protocol.OpOrcAssignAuthority, protocol.OpOrcAssignPerm,
			protocol.OpOrcMove, protocol.OpOrcBudget, protocol.OpOrcTend, protocol.OpOrcToolkit,
			protocol.OpOrcFire, protocol.OpOrcRevoke, protocol.OpOrcEditPermission,
			protocol.OpOrcInstructSet, protocol.OpOrcInstructClear:
			// Each sets a state to what was asked for, so a repeat lands in the
			// same place. `tend` most of all: reconciling twice reconciles. An edit
			// carries the whole permission, so applying it twice writes the same
			// floor and the same clauses — and orc says "already that" rather than
			// journaling a second amendment.
			//
			// A prompt is a value too: setting a layer to what it already says
			// lands in the same place, and clearing one twice is cleared. And the
			// toolkit is `orc bootstrap`, which is documented as safe to run twice
			// precisely because it creates only what is not there.
			if !op.Idempotent() {
				t.Errorf("%q should be idempotent", op)
			}
		case protocol.OpOrcNewIdentity, protocol.OpOrcNewRole, protocol.OpOrcNewPermission,
			protocol.OpOrcRemoveIdentity, protocol.OpOrcRemoveRole, protocol.OpOrcRemovePerm,
			protocol.OpOrcGrant, protocol.OpOrcEmploy, protocol.OpOrcPoke, protocol.OpOrcRefresh,
			protocol.OpOrcWorkspace:
			// Creating twice conflicts, employing twice spends a budget twice, and
			// refreshing twice discards the conversation the first refresh started.
			//
			// A workspace move is two operations behind one verb: adopting a
			// directory lands in the same place however often it is applied, but
			// relocating copies files and the second application finds the source
			// where it left it. It is classified by the half that is not
			// idempotent, and guarded like the library's writes are — `from` is its
			// digest, so a retry against a moved-on world is refused rather than
			// repeated.
			if op.Idempotent() {
				t.Errorf("%q must not be idempotent", op)
			}
		case protocol.OpUpgrade:
			// Pulling and rebuilding twice lands on the same revision.
			if !op.Idempotent() {
				t.Errorf("%q should be idempotent", op)
			}
		case protocol.OpLibraryRoot:
			// It sets the root to the directory named rather than stepping it from
			// wherever it was, so applying it twice leaves the machine mirroring
			// the same place. Unlike a workspace move it copies nothing — the
			// files are already there; only which of them cq looks at changes.
			if !op.Idempotent() {
				t.Errorf("%q should be idempotent", op)
			}
		default:
			t.Errorf("%q is an operation nobody has decided about", op)
		}
	}
}

// TestAnInterruptedLibraryVerbMayBeRetried.
//
// Every one of them looks before it acts, so a second application refuses or
// finishes rather than repeating. That is exactly the case a retry exists for:
// "it may or may not have removed the folder" is when somebody most wants to try
// again and have the machine decide.
func TestAnInterruptedLibraryVerbMayBeRetried(t *testing.T) {
	s := open(t)
	for _, tc := range []struct {
		op   protocol.Op
		args protocol.Args
	}{
		{protocol.OpWrite, protocol.Args{Path: "a.go", Text: "x", Base: strings.Repeat("b", 64)}},
		{protocol.OpCreate, protocol.Args{Path: "new.go", Text: "x"}},
		{protocol.OpDelete, protocol.Args{Path: "a.go", Base: strings.Repeat("b", 64)}},
		{protocol.OpRemoveDir, protocol.Args{Path: "Docs/Empty"}},
		{protocol.OpRemoveTree, protocol.Args{Path: "Docs/Old", Paths: []string{"Docs/Old/a.md"}}},
	} {
		t.Run(string(tc.op), func(t *testing.T) {
			doubtful := doubtfulAction(t, s, tc.op, tc.args)
			if _, err := s.Retry(doubtful.ID, at); err != nil {
				t.Errorf("%s in doubt should be retryable: %v", tc.op, err)
			}
		})
	}
}

// TestCancellingDoesNotRaceASync is why the queue lock covers collection as well
// as allocation.
//
// The hazard is narrow and it is the one that matters: a cancel reads an action
// as "waiting", a sync collects it, and the removal then lands on something the
// agent has already been handed — deleting an action that is about to be applied
// and whose result will have nowhere to go. Whichever wins is fine; both winning
// is not.
func TestCancellingDoesNotRaceASync(t *testing.T) {
	for range 40 {
		s := open(t)
		action, err := s.Enqueue("studio", protocol.OpRead, protocol.Args{PUID: 1}, at)
		if err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		start := make(chan struct{})
		var dropErr, sentErr error

		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			dropErr = s.Drop(action.ID)
		}()
		go func() {
			defer wg.Done()
			<-start
			sentErr = s.MarkSent([]protocol.ActionID{action.ID}, at)
		}()
		close(start)
		wg.Wait()

		if sentErr != nil {
			t.Fatalf("MarkSent: %v", sentErr)
		}

		entry, err := s.Entry(action.ID)
		switch {
		case errors.Is(err, fault.ErrNotFound):
			// The cancel won: it is gone, and it must not have been handed out.
			if dropErr != nil {
				t.Fatalf("the action is gone but the cancel reported %v", dropErr)
			}
		case err != nil:
			t.Fatalf("Entry: %v", err)
		default:
			// The sync won: it is with the agent, and the cancel must have been
			// refused rather than quietly doing nothing.
			if entry.State != store.Sent {
				t.Fatalf("the action survived in state %q", entry.State)
			}
			if !errors.Is(dropErr, fault.ErrConflict) {
				t.Fatalf("the action went to the agent but the cancel reported %v", dropErr)
			}
		}
	}
}
