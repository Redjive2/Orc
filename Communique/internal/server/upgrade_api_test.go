package server_test

import (
	"net/http"
	"testing"

	"orc/cq/internal/protocol"
)

// The upgrade endpoint. Two halves, because the two machines are reachable in
// opposite directions: this server upgrades itself, and every agent machine gets a
// queued action it applies on its next sync.

// TestUpgradeQueuesEveryAgentMachine is the half the server cannot do itself.
func TestUpgradeQueuesEveryAgentMachine(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login(t)
	putSnapshot(t, h, "studio")
	putSnapshot(t, h, "laptop")

	w := h.do(t, "POST", "/api/v1/upgrade", `{}`, h.withCookie(cookie), withCSRF(csrf))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", w.Code, w.Body)
	}

	queued := map[string]bool{}
	for _, e := range queueOf(t, h, cookie) {
		if e.Action.Op == protocol.OpUpgrade {
			queued[string(e.Action.Machine)] = true
		}
	}
	for _, want := range []string{"studio", "laptop"} {
		if !queued[want] {
			t.Errorf("%s was not queued an upgrade", want)
		}
	}
}

// TestUpgradeCanBeNarrowed: one machine, or none at all.
func TestUpgradeCanBeNarrowed(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login(t)
	putSnapshot(t, h, "studio")
	putSnapshot(t, h, "laptop")

	w := h.do(t, "POST", "/api/v1/upgrade", `{"machines":["studio"]}`,
		h.withCookie(cookie), withCSRF(csrf))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	for _, e := range queueOf(t, h, cookie) {
		if e.Action.Op == protocol.OpUpgrade && string(e.Action.Machine) != "studio" {
			t.Errorf("%s was queued despite not being asked for", e.Action.Machine)
		}
	}
}

// TestUpgradeSaysWhyItWillNotRestart is the honest half.
//
// A server nothing is supervising cannot restart itself, and a button that took
// the site down and left it down would be worse than no button. It says so in the
// response rather than in a log nobody is reading — and it still queues the
// agents, because those do not depend on this machine at all.
func TestUpgradeSaysWhyItWillNotRestart(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login(t)
	putSnapshot(t, h, "studio")

	w := h.do(t, "POST", "/api/v1/upgrade", `{}`, h.withCookie(cookie), withCSRF(csrf))
	var view struct {
		Queued     []string `json:"queued"`
		Server     string   `json:"server"`
		Restarting bool     `json:"restarting"`
	}
	decodeInto(t, w.Body.Bytes(), &view)

	if view.Restarting {
		t.Errorf("an unsupervised server said it would restart")
	}
	if view.Server == "" {
		t.Fatalf("it did not say what the server would do")
	}
	if len(view.Queued) != 1 {
		t.Errorf("the agents were not queued: %+v", view.Queued)
	}
}

// TestUpgradeTakesATokenOrASession.
//
// The admin panel is a session; `cq upgrade` on an agent machine has a sync token
// and no password. Both are credentials the operator handed out on purpose, and
// the narrower gate — Orc's `upgrade` permission — is checked by the client that
// has an Orc fleet to ask.
func TestUpgradeTakesATokenOrASession(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login(t)
	putSnapshot(t, h, "studio")

	if w := h.do(t, "POST", "/api/v1/upgrade", `{}`, h.withToken()); w.Code != http.StatusAccepted {
		t.Errorf("a sync token was refused: %d %s", w.Code, w.Body)
	}
	if w := h.do(t, "POST", "/api/v1/upgrade", `{}`, h.withCookie(cookie), withCSRF(csrf)); w.Code != http.StatusAccepted {
		t.Errorf("a logged-in session was refused: %d %s", w.Code, w.Body)
	}
	// And neither is still nothing.
	if w := h.do(t, "POST", "/api/v1/upgrade", `{}`); w.Code == http.StatusAccepted {
		t.Errorf("an unauthenticated request was accepted")
	}
	// A session without its CSRF token is a cross-site post, and is refused like
	// every other write.
	if w := h.do(t, "POST", "/api/v1/upgrade", `{}`, h.withCookie(cookie)); w.Code == http.StatusAccepted {
		t.Errorf("a session without a CSRF token was accepted")
	}
}
