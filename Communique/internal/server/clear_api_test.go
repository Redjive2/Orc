package server_test

import (
	"net/http"
	"testing"

	"orc/cq/internal/protocol"
	"orc/cq/internal/store"
)

// Clearing the queue.
//
// The queue is a log as much as a queue: an action that is done stays on the
// list, and after a busy afternoon the rows worth reading — the ones that failed —
// are somewhere below fifty that did not. Clearing them one at a time is a chore
// rather than housekeeping.

// settle puts one action into a finished state, through the real sync endpoints
// rather than by writing the store — so what these tests clear is what an agent
// actually leaves behind.
func settle(t *testing.T, h *harness, cookie, csrf string, ok bool) string {
	t.Helper()
	result := protocol.Result{OK: ok, At: at}
	if !ok {
		result.Error = "it was refused"
	}
	return failOne(t, h, cookie, csrf, result)
}

func statesOf(t *testing.T, h *harness, cookie string) map[store.State]int {
	t.Helper()
	got := map[store.State]int{}
	for _, e := range queueOf(t, h, cookie) {
		got[e.State]++
	}
	return got
}

// TestClearTakesTheDoneOnesByDefault, and leaves everything that still says
// something. A tidy-up that threw away the reason an action failed would make the
// housekeeping command the destructive one.
func TestClearTakesTheDoneOnesByDefault(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login(t)
	putSnapshot(t, h, "studio")

	settle(t, h, cookie, csrf, true)
	settle(t, h, cookie, csrf, true)
	settle(t, h, cookie, csrf, false)

	before := statesOf(t, h, cookie)
	if before[store.Done] != 2 || before[store.Failed] != 1 {
		t.Fatalf("the setup is wrong: %+v", before)
	}

	w := h.do(t, "POST", "/api/v1/queue/clear", `{}`, h.withCookie(cookie), withCSRF(csrf))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var view struct {
		Cleared int `json:"cleared"`
		Left    int `json:"left"`
	}
	decodeInto(t, w.Body.Bytes(), &view)
	if view.Cleared != 2 {
		t.Errorf("cleared %d, want the 2 done ones", view.Cleared)
	}

	after := statesOf(t, h, cookie)
	if after[store.Done] != 0 {
		t.Errorf("a done action survived: %+v", after)
	}
	if after[store.Failed] != 1 {
		t.Errorf("the refused action was swept away with them: %+v", after)
	}
}

// TestClearCanBeAskedForMore, when the operator has read the reasons and wants
// the list empty.
func TestClearCanBeAskedForMore(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login(t)
	putSnapshot(t, h, "studio")

	settle(t, h, cookie, csrf, true)
	settle(t, h, cookie, csrf, false)

	w := h.do(t, "POST", "/api/v1/queue/clear", `{"states":["done","failed"]}`,
		h.withCookie(cookie), withCSRF(csrf))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := queueOf(t, h, cookie); len(got) != 0 {
		t.Errorf("%d entries survived a clear of every settled state", len(got))
	}
}

// TestClearWillNotTakeSomethingInFlight is the rule the whole thing rests on.
//
// An action the agent may still report on cannot be dropped: the report would have
// nowhere to land, and the operator would be left believing something happened
// that may not have. The refusal names the state rather than quietly doing
// nothing, because a caller asking to clear `queued` has a wrong idea about what
// the queue is.
func TestClearWillNotTakeSomethingInFlight(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login(t)
	putSnapshot(t, h, "studio")

	// One done, and then one still waiting. In that order: settling runs a real
	// sync, and a sync collects *everything* pending — so an action queued first
	// would be collected along with it and never be the waiting one this needs.
	settle(t, h, cookie, csrf, true)
	if w := h.do(t, "POST", "/api/v1/messages/41/read", `{"machine":"studio"}`,
		h.withCookie(cookie), withCSRF(csrf)); w.Code != http.StatusAccepted {
		t.Fatal(w.Body)
	}

	if w := h.do(t, "POST", "/api/v1/queue/clear", `{"states":["queued"]}`,
		h.withCookie(cookie), withCSRF(csrf)); w.Code == http.StatusOK {
		t.Errorf("clearing an in-flight state was accepted")
	}
	// And the default sweep leaves it alone rather than failing over it.
	if w := h.do(t, "POST", "/api/v1/queue/clear", `{}`,
		h.withCookie(cookie), withCSRF(csrf)); w.Code != http.StatusOK {
		t.Fatalf("the default sweep failed because something was in flight: %s", w.Body)
	}
	if got := statesOf(t, h, cookie); got[store.Queued] != 1 {
		t.Errorf("the waiting action did not survive: %+v", got)
	}
}

// TestClearingNothingIsNotAFailure. Pressing the button on an already-tidy queue
// should say so, not error.
func TestClearingNothingIsNotAFailure(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login(t)
	putSnapshot(t, h, "studio")

	w := h.do(t, "POST", "/api/v1/queue/clear", `{}`, h.withCookie(cookie), withCSRF(csrf))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var view struct {
		Cleared int `json:"cleared"`
	}
	decodeInto(t, w.Body.Bytes(), &view)
	if view.Cleared != 0 {
		t.Errorf("cleared %d from an empty queue", view.Cleared)
	}
}

// TestADoneActionCanBeDroppedOnItsOwn: the per-row control the browser lacked.
func TestADoneActionCanBeDroppedOnItsOwn(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login(t)
	putSnapshot(t, h, "studio")

	id := settle(t, h, cookie, csrf, true)
	w := h.do(t, "DELETE", "/api/v1/queue/"+id, "", h.withCookie(cookie), withCSRF(csrf))
	if w.Code != http.StatusOK {
		t.Fatalf("dropping a done action: %d %s", w.Code, w.Body)
	}
	if got := queueOf(t, h, cookie); len(got) != 0 {
		t.Errorf("the done action survived: %+v", got)
	}
}
