package server_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"orc/cq/internal/protocol"
	"orc/cq/internal/server"
	"orc/cq/internal/upgrade"
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

// The answer to the button is a promise: the build runs after the response, on a
// goroutine that ends with the process going away. So the outcome used to reach a
// log file and nowhere else — a failed build left the page saying "pulling,
// building, and restarting" indefinitely, which reads exactly like a build still
// running and exactly like a restart about to happen.
//
// The one case nobody could see is the one case the server stays up to explain.
func TestAFailedSelfUpgradeCanBeAskedAbout(t *testing.T) {
	h := newHarness(t)
	restarted := make(chan struct{}, 1)
	// A source that is not a git checkout: the refusal is immediate and needs no
	// commands run, which keeps this about the reporting rather than about git.
	h.Server = mustServe(t, h, server.Options{
		Upgrade: upgrade.Options{Source: t.TempDir()},
		Restart: func() { restarted <- struct{}{} },
	})

	cookie, csrf := h.login(t)
	if w := h.do(t, "POST", "/api/v1/upgrade", `{}`, h.withCookie(cookie), withCSRF(csrf)); w.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202: %s", w.Code, w.Body)
	}

	var last struct {
		State string `json:"state"`
		Error string `json:"error"`
	}
	for range 200 {
		w := h.do(t, "GET", "/api/v1/upgrade", "", h.withCookie(cookie))
		if w.Code != http.StatusOK {
			t.Fatalf("asking: %d %s", w.Code, w.Body)
		}
		var got struct {
			Last *struct {
				State string `json:"state"`
				Error string `json:"error"`
			} `json:"last"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding: %v\n%s", err, w.Body)
		}
		if got.Last != nil && got.Last.State != "building" {
			last.State, last.Error = got.Last.State, got.Last.Error
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if last.State != "failed" {
		t.Fatalf("the failed build reported state %q", last.State)
	}
	if last.Error == "" {
		t.Error("a failed build recorded no reason, which is the whole point of recording it")
	}
	// And it did not restart. A restart after a failed build brings nothing up at
	// all, which is worse than staying on the old binary.
	select {
	case <-restarted:
		t.Error("the server restarted into a build that never happened")
	default:
	}
}

// mustServe rebuilds the harness's server with extra options, keeping its store and
// credentials so a login still works.
func mustServe(t *testing.T, h *harness, extra server.Options) *server.Server {
	t.Helper()
	extra.State, extra.Creds, extra.Admin = h.state, h.creds, true
	extra.Logger = slog.New(slog.DiscardHandler)
	extra.Now = func() time.Time { return h.now }
	srv, err := server.New(extra)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}
