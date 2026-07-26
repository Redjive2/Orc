package cli_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
	"orc/cq/internal/store"
)

var when = time.Date(2026, 7, 25, 4, 0, 0, 0, time.UTC)

// stock puts one settled action in the server's queue and returns its id.
func stock(t *testing.T, h *harness, op protocol.Op, args protocol.Args, result protocol.Result) protocol.ActionID {
	t.Helper()
	s, err := store.Open(h.state)
	if err != nil {
		t.Fatal(err)
	}
	action, err := s.Enqueue("studio", op, args, when)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSent([]protocol.ActionID{action.ID}, when); err != nil {
		t.Fatal(err)
	}
	result.ActionID, result.At = action.ID, when
	if err := s.Complete([]protocol.Result{result}); err != nil {
		t.Fatal(err)
	}
	return action.ID
}

func refusedRead(t *testing.T, h *harness) protocol.ActionID {
	t.Helper()
	return stock(t, h, protocol.OpRead, protocol.Args{PUID: 41},
		protocol.Result{OK: false, Error: "mailman said no"})
}

func TestQueueListsWhatBecameOfEachAction(t *testing.T) {
	h := newHarness(t)
	id := refusedRead(t, h)

	got := h.run(t, "", "queue").mustSucceed(t)
	for _, want := range []string{string(id)[:8], "failed", "read", "mailman said no"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("the listing should mention %q:\n%s", want, got.stdout)
		}
	}
	// And it says what can be done about it, because a list of rows does not.
	if !strings.Contains(got.stdout, "queue retry") {
		t.Errorf("an unresolved action should come with its remedy:\n%s", got.stdout)
	}
}

func TestQueueOnAnEmptyStoreSaysSo(t *testing.T) {
	h := newHarness(t)
	got := h.run(t, "", "queue").mustSucceed(t)
	if !strings.Contains(got.stdout, "empty") {
		t.Errorf("stdout = %q", got.stdout)
	}
}

// TestQueueRetryNamesTheNewAction: the identifier changes, and hiding that would
// leave the operator watching a row that will never move.
func TestQueueRetryNamesTheNewAction(t *testing.T) {
	h := newHarness(t)
	id := refusedRead(t, h)

	got := h.run(t, "", "queue", "retry", string(id)[:8]).mustSucceed(t)
	if !strings.Contains(got.stdout, "queued again as") {
		t.Fatalf("stdout = %q", got.stdout)
	}
	if strings.Contains(got.stdout, string(id)[:8]) {
		t.Errorf("the reply names the old action, not the new one:\n%s", got.stdout)
	}

	// Both are in the queue now: the retry, and the record of what failed.
	listing := h.run(t, "", "queue").mustSucceed(t)
	if !strings.Contains(listing.stdout, "queued") || !strings.Contains(listing.stdout, "failed") {
		t.Errorf("listing = %q", listing.stdout)
	}
}

// TestQueueAcceptsAPrefix: the listing prints eight characters, so requiring all
// thirty-two would make the command unusable from what is on screen.
func TestQueueAcceptsAPrefix(t *testing.T) {
	for _, form := range []struct {
		name  string
		typed func(protocol.ActionID) string
	}{
		{"four characters", func(id protocol.ActionID) string { return string(id)[:4] }},
		{"as printed", func(id protocol.ActionID) string { return string(id)[:8] }},
		{"in full", func(id protocol.ActionID) string { return string(id) }},
		{"upper case", func(id protocol.ActionID) string { return strings.ToUpper(string(id)[:8]) }},
		{"with stray spaces", func(id protocol.ActionID) string { return "  " + string(id)[:8] + " " }},
	} {
		t.Run(form.name, func(t *testing.T) {
			h := newHarness(t)
			id := refusedRead(t, h)
			typed := form.typed(id)
			if got := h.run(t, "", "queue", "retry", typed); got.code != fault.ExitOK {
				t.Errorf("%q was not accepted: exit %d\n%s", typed, got.code, got.stderr)
			}
		})
	}
}

// An ambiguous prefix lists the candidates in full, so every line of the refusal
// is itself a usable argument.
//
// The ids are random, so the shared prefix is found rather than assumed: with
// more actions than there are hex digits, two of them must begin with the same
// character. That makes the test certain instead of usually-skipped.
func TestAnAmbiguousPrefixListsTheCandidates(t *testing.T) {
	h := newHarness(t)

	seen := map[byte][]protocol.ActionID{}
	var shared byte
	for range 17 { // one more than the sixteen possible first characters
		id := refusedRead(t, h)
		first := string(id)[0]
		seen[first] = append(seen[first], id)
		if len(seen[first]) == 2 {
			shared = first
			break
		}
	}
	if shared == 0 {
		t.Fatal("seventeen ids over sixteen possible first characters must collide")
	}

	got := h.run(t, "", "queue", "retry", string(shared))
	if got.code != fault.ExitAmbiguous {
		t.Fatalf("exit %d, want ambiguous\n%s", got.code, got.stderr)
	}
	for _, id := range seen[shared] {
		if !strings.Contains(got.stderr, string(id)) {
			t.Errorf("the refusal should name %s in full:\n%s", id, got.stderr)
		}
	}
	// Nothing was retried: an ambiguous instruction is not carried out on a guess.
	listing := h.run(t, "", "queue").mustSucceed(t)
	if strings.Contains(listing.stdout, "queued") {
		t.Errorf("an ambiguous prefix queued something:\n%s", listing.stdout)
	}
}

func TestQueueRefusesAnUnknownAction(t *testing.T) {
	h := newHarness(t)
	if got := h.run(t, "", "queue", "retry", "zzzzzzzz"); got.code != fault.ExitNotFound {
		t.Errorf("exit %d, want not found\n%s", got.code, got.stderr)
	}
}

func TestQueueRefusesRetryingWhatWorked(t *testing.T) {
	h := newHarness(t)
	id := stock(t, h, protocol.OpRead, protocol.Args{PUID: 1}, protocol.Result{OK: true})

	got := h.run(t, "", "queue", "retry", string(id)[:8])
	if got.code != fault.ExitConflict {
		t.Fatalf("exit %d, want a conflict\n%s", got.code, got.stderr)
	}
	if !strings.Contains(got.stderr, "already worked") {
		t.Errorf("stderr = %q", got.stderr)
	}
	// A conflict about no named thing is a sentence, not a sentence with a colon
	// in front of it. fault.Conflict used to render an empty subject as ": ".
	if strings.Contains(got.stderr, "cq: :") {
		t.Errorf("the message has a stray prefix: %q", got.stderr)
	}
}

// TestQueueWillNotRepeatAnInterruptedSend is the safety rule at the surface the
// operator actually touches.
func TestQueueWillNotRepeatAnInterruptedSend(t *testing.T) {
	h := newHarness(t)
	id := stock(t, h, protocol.OpSend,
		protocol.Args{To: []string{"bob"}, Subject: "s", Body: "b"},
		protocol.Result{OK: false, InDoubt: true, Error: "interrupted; it may or may not have been applied"})

	got := h.run(t, "", "queue", "retry", string(id)[:8])
	if got.code != fault.ExitConflict {
		t.Fatalf("exit %d, want a conflict — an interrupted send must not be repeated\n%s", got.code, got.stderr)
	}
	if !strings.Contains(got.stderr, "sent mail") {
		t.Errorf("the refusal should say what to check: %q", got.stderr)
	}

	// The listing shows it as in doubt, not as failed: they read alike and demand
	// opposite responses.
	listing := h.run(t, "", "queue").mustSucceed(t)
	if !strings.Contains(listing.stdout, "in_doubt") {
		t.Errorf("listing = %q", listing.stdout)
	}
}

func TestQueueDropForgetsAnAction(t *testing.T) {
	h := newHarness(t)
	id := refusedRead(t, h)

	h.run(t, "", "queue", "drop", string(id)[:8]).mustSucceed(t)
	if got := h.run(t, "", "queue").mustSucceed(t); strings.Contains(got.stdout, string(id)[:8]) {
		t.Errorf("the dropped action is still listed:\n%s", got.stdout)
	}
	// Twice is quiet, so a stale screen does not produce an error.
	h.run(t, "", "queue", "drop", string(id)).mustSucceed(t)
}

func TestQueueJSONIsForAnotherProgram(t *testing.T) {
	h := newHarness(t)
	refusedRead(t, h)

	got := h.run(t, "", "queue", "--json").mustSucceed(t)
	var entries []store.Entry
	if err := json.Unmarshal([]byte(got.stdout), &entries); err != nil {
		t.Fatalf("not json: %v\n%s", err, got.stdout)
	}
	if len(entries) != 1 || entries[0].State != store.Failed {
		t.Errorf("entries = %+v", entries)
	}
	if strings.Contains(got.stdout, "\x1b[") {
		t.Errorf("colour would be corruption in machine output:\n%q", got.stdout)
	}
	// An empty queue is an empty array, not null: a caller that iterates should
	// not have to special-case nothing.
	empty := newHarness(t).run(t, "", "queue", "--json").mustSucceed(t)
	if strings.TrimSpace(empty.stdout) != "[]" {
		t.Errorf("empty queue = %q", empty.stdout)
	}
}

func TestQueueRejectsBadUsage(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{
		{"queue", "retry"},
		{"queue", "retry", "a", "b"},
		{"queue", "drop"},
		{"queue", "nonsense"},
	} {
		if got := h.run(t, "", args...); got.code != fault.ExitUsage {
			t.Errorf("%v exited %d, want a usage error", args, got.code)
		}
	}
}
