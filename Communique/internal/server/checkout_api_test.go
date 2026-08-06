package server_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"orc/cq/internal/upgrade"
)

// Whether the button can be pressed, asked before pressing it.
//
// The route is polled by a page, which is what most of this is about: it must be
// cheap, it must not touch the checkout, and it must not be reachable by anything
// that only holds a machine's sync token.

func readCheckout(t *testing.T, h *harness, cookie string) upgrade.Status {
	t.Helper()
	w := h.do(t, "GET", "/api/v1/upgrade/checkout", "", h.withCookie(cookie))
	if w.Code != http.StatusOK {
		t.Fatalf("asking: %d %s", w.Code, w.Body)
	}
	var got upgrade.Status
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v\n%s", err, w.Body)
	}
	return got
}

// A server with no checkout is a server that installs binaries rather than building
// one. It is a verdict, not a fault: answering 500 would report the diagnosis as a
// failure of the diagnosis.
func TestAServerWithNoCheckoutSaysSoRatherThanFailing(t *testing.T) {
	h := newHarness(t)
	cookie, _ := h.login(t)

	got := readCheckout(t, h, cookie)
	if got.Verdict != upgrade.Stop {
		t.Errorf("a server with nothing to build from says %q", got.Verdict)
	}
	if len(got.Reasons) == 0 {
		t.Fatal("a stop with no reason is a red light nobody can act on")
	}
	if got.Reasons[0].Fix == "" {
		t.Error("the reason says what is wrong and not what to do about it")
	}
}

// It discloses paths, branch names and what is uncommitted on the machine serving
// the site. A sync token belongs to an agent machine, which has no business reading
// any of that — the upgrade *button* takes either credential because either is the
// operator asking, and this is not the button.
func TestTheCheckoutIsForASessionAndNotForAToken(t *testing.T) {
	h := newHarness(t)
	if w := h.do(t, "GET", "/api/v1/upgrade/checkout", "", h.withToken()); w.Code == http.StatusOK {
		t.Errorf("a machine token read the server's checkout: %d", w.Code)
	}
	if w := h.do(t, "GET", "/api/v1/upgrade/checkout", ""); w.Code == http.StatusOK {
		t.Errorf("a stranger read the server's checkout: %d", w.Code)
	}
}

// The page polls this, so ten tabs must not be ten inspections. The lock is held
// across the check as well, so concurrent askers wait for one answer rather than
// racing into the same repository — which is what this really pins: no request here
// may fail, hang, or answer differently because another was in flight.
func TestPollingTheCheckoutIsCheapAndConsistent(t *testing.T) {
	h := newHarness(t)
	cookie, _ := h.login(t)

	first := readCheckout(t, h, cookie)
	done := make(chan upgrade.Status, 8)
	for range cap(done) {
		go func() { done <- readCheckout(t, h, cookie) }()
	}
	for range cap(done) {
		if got := <-done; got.Verdict != first.Verdict {
			t.Errorf("a concurrent ask answered %q, want %q", got.Verdict, first.Verdict)
		}
	}
}
