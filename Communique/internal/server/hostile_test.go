package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"orc/cq/internal/server"
)

// TestTheRestOfTheReadEndpoints covers the two the API walk does not exercise
// directly.
func TestTheRestOfTheReadEndpoints(t *testing.T) {
	h := newHarness(t)
	putSnapshot(t, h, "studio")
	cookie, _ := h.login(t)

	t.Run("machines", func(t *testing.T) {
		w := h.do(t, "GET", "/api/v1/machines", "", h.withCookie(cookie))
		if w.Code != http.StatusOK {
			t.Fatalf("status %d, body %s", w.Code, w.Body)
		}
		var v struct {
			Machines []struct {
				Machine string `json:"machine"`
			} `json:"machines"`
		}
		decodeInto(t, w.Body.Bytes(), &v)
		if len(v.Machines) != 1 || v.Machines[0].Machine != "studio" {
			t.Errorf("machines = %+v", v.Machines)
		}
	})

	t.Run("the application page", func(t *testing.T) {
		w := h.do(t, "GET", "/", "", h.withCookie(cookie))
		if w.Code != http.StatusOK {
			t.Fatalf("status %d", w.Code)
		}
		if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
			t.Errorf("Content-Type = %q", got)
		}
		if !strings.Contains(w.Body.String(), "communiqué") {
			t.Errorf("body = %s", w.Body)
		}
	})
}

// TestLoginFailureCanAnswerJSON, for a client that asked for it rather than a
// browser following a form.
func TestLoginFailureCanAnswerJSON(t *testing.T) {
	h := newHarness(t)
	w := h.do(t, "POST", "/login", "password=wrong", func(r *http.Request) {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Accept", "application/json")
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", w.Code)
	}
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	decodeInto(t, w.Body.Bytes(), &e)
	if e.Error.Code != "unauthenticated" || e.Error.Message != "not authenticated" {
		t.Errorf("error = %+v, want a uniform refusal", e.Error)
	}
}

// TestEveryReadEndpointReportsAnUnreadableStore drives the whole read surface
// against a store it cannot use. Each must fail loudly: a mirror that answered
// "no mail" when it simply could not look would be the worst kind of wrong.
func TestEveryReadEndpointReportsAnUnreadableStore(t *testing.T) {
	if !modeBitsBite() {
		t.Skip("this machine cannot make a file unreadable to its owner")
	}
	h := newHarness(t)
	putSnapshot(t, h, "studio")
	cookie, csrf := h.login(t)

	dir := filepath.Join(h.state.Root(), "machines")
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	for _, path := range []string{
		"/api/v1/session",
		"/api/v1/machines",
		"/api/v1/inbox",
		"/api/v1/archive",
		"/api/v1/messages/41",
		"/api/v1/messages/41/check",
		"/api/v1/convos/c-1",
		"/api/v1/tasks",
		"/api/v1/tasks/fix-the-parser",
		"/api/v1/admin/state",
	} {
		t.Run(path, func(t *testing.T) {
			w := h.do(t, "GET", path, "", h.withCookie(cookie))
			if w.Code == http.StatusOK {
				t.Errorf("answered 200 from a store it cannot read: %s", w.Body)
			}
		})
	}

	w := h.do(t, "POST", "/api/v1/messages", `{"to":["bob"],"subject":"s","body":"b"}`,
		h.withCookie(cookie), func(r *http.Request) { r.Header.Set("X-CSRF-Token", csrf) })
	if w.Code == http.StatusAccepted {
		t.Errorf("a write was accepted against an unusable store")
	}
}

func TestSyncReportsAnUnwritableStore(t *testing.T) {
	if !modeBitsBite() {
		t.Skip("this machine cannot make a file unreadable to its owner")
	}
	h := newHarness(t)
	dir := filepath.Join(h.state.Root(), "machines")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	w := h.do(t, "POST", "/api/v1/sync", syncBody(t, "studio"), h.withToken())
	if w.Code == http.StatusOK {
		t.Errorf("a sync that could not store its snapshot answered 200")
	}
}

// TestInternalDetailNeverReachesTheClient: a store path or an invariant's text
// is the operator's business, not a caller's.
func TestInternalDetailNeverReachesTheClient(t *testing.T) {
	if !modeBitsBite() {
		t.Skip("this machine cannot make a file unreadable to its owner")
	}
	h := newHarness(t)
	putSnapshot(t, h, "studio")
	cookie, _ := h.login(t)

	dir := filepath.Join(h.state.Root(), "machines")
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	w := h.do(t, "GET", "/api/v1/inbox", "", h.withCookie(cookie))
	body := w.Body.String()
	for _, leak := range []string{h.state.Root(), "permission denied", "store."} {
		if strings.Contains(body, leak) {
			t.Errorf("the response leaked %q: %s", leak, body)
		}
	}
}

// TestIdleStreamsSendAHeartbeat, so a proxy in the middle does not decide a
// quiet connection is a dead one.
func TestIdleStreamsSendAHeartbeat(t *testing.T) {
	h := newHarnessWithHeartbeat(t, 20*time.Millisecond)
	cookie, _ := h.login(t)

	srv := httptest.NewServer(h.Server)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: "cq_session", Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	reader := bufio.NewReader(resp.Body)
	if got := readEvent(t, reader); got != "ready" {
		t.Fatalf("first event = %q", got)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("reading the stream: %v", err)
		}
		if strings.HasPrefix(line, ": keep-alive") {
			return
		}
	}
	t.Fatal("no heartbeat arrived on an idle stream")
}

// TestClientDisconnectEndsTheStream: a browser that walks away must not leave a
// listener behind.
func TestClientDisconnectEndsTheStream(t *testing.T) {
	h := newHarness(t)
	cookie, _ := h.login(t)

	srv := httptest.NewServer(h.Server)
	defer srv.Close()

	ctx, cancel := context.WithCancel(t.Context())
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: "cq_session", Value: cookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := readEvent(t, bufio.NewReader(resp.Body)); got != "ready" {
		t.Fatalf("first event = %q", got)
	}
	waitFor(t, func() bool { return h.Events().Listeners() == 1 })

	cancel()
	_ = resp.Body.Close()
	waitFor(t, func() bool { return h.Events().Listeners() == 0 })
}

func newHarnessWithHeartbeat(t *testing.T, heartbeat time.Duration) *harness {
	t.Helper()
	base := newHarness(t)
	h := &harness{state: base.state, creds: base.creds, token: base.token, now: base.now}
	srv, err := server.New(server.Options{
		State: base.state, Creds: base.creds, Admin: true,
		Logger:    slog.New(slog.DiscardHandler),
		Now:       func() time.Time { return h.now },
		Heartbeat: heartbeat,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.Server = srv
	return h
}

// TestOversizedBodiesAreRefusedPromptly guards the entry points that accept a
// document at all.
func TestOversizedBodiesAreRefusedPromptly(t *testing.T) {
	h := newHarness(t)
	putSnapshot(t, h, "studio")

	start := time.Now()
	w := h.do(t, "POST", "/api/v1/sync", `{"protocol":1,"agent":"`+strings.Repeat("x", 4096)+`"}`, h.withToken())
	if w.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", w.Code)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("refusing took %v", elapsed)
	}

	cookie, csrf := h.login(t)
	body, err := json.Marshal(map[string]any{
		"to": []string{"bob"}, "subject": "s", "body": strings.Repeat("x", 2<<20),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := h.do(t, "POST", "/api/v1/messages", string(body), h.withCookie(cookie),
		func(r *http.Request) { r.Header.Set("X-CSRF-Token", csrf) })
	if got.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400 for an oversized message", got.Code)
	}
}

// TestTheInterfaceIsServedAndSatisfiesItsOwnPolicy: every asset the shell asks
// for must exist, be behind the gate, and be permitted by the content policy —
// a page that its own policy blocks is a page that does not load.
func TestTheInterfaceIsServedAndSatisfiesItsOwnPolicy(t *testing.T) {
	h := newHarness(t)
	putSnapshot(t, h, "studio")
	cookie, _ := h.login(t)

	shell := h.do(t, "GET", "/", "", h.withCookie(cookie))
	if shell.Code != http.StatusOK {
		t.Fatalf("the shell: status %d", shell.Code)
	}
	body := shell.Body.String()

	csp := shell.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") || strings.Contains(csp, "unsafe-inline") {
		t.Errorf("content policy = %q", csp)
	}

	// Every asset the shell references.
	var refs []string
	for _, part := range strings.Split(body, `"`) {
		if strings.HasPrefix(part, "/assets/") {
			refs = append(refs, part)
		}
	}
	if len(refs) < 3 {
		t.Fatalf("the shell references %d assets, want the stylesheet, theme and script: %v", len(refs), refs)
	}

	for _, ref := range refs {
		t.Run(ref, func(t *testing.T) {
			// It exists for a logged-in caller…
			got := h.do(t, "GET", ref, "", h.withCookie(cookie))
			if got.Code != http.StatusOK {
				t.Fatalf("status %d", got.Code)
			}
			if got.Body.Len() == 0 {
				t.Errorf("the asset is empty")
			}
			// …and not for anyone else.
			if stranger := h.do(t, "GET", ref, ""); stranger.Code == http.StatusOK {
				t.Errorf("a stranger received %s", ref)
			}
		})
	}

	// The generated stylesheet carries the shared scheme's colours.
	css := h.do(t, "GET", "/assets/theme.css", "", h.withCookie(cookie))
	if !strings.Contains(css.Body.String(), "--canvas:") {
		t.Errorf("the theme is missing its surfaces:\n%s", css.Body)
	}
}

// modeBitsBite reports whether this machine can genuinely deny the process
// access to a file it owns.
//
// Two machines cannot, and the tests that chmod something all need one that
// can. Root is
// refused nothing. Windows has no mode bit for this at all: os.Chmod there
// toggles the read-only attribute and leaves reading alone, and does not make a
// directory unwritable — so the failure these tests provoke simply does not
// happen, and the assertion would fail for the wrong reason.
func modeBitsBite() bool {
	return os.Geteuid() != 0 && runtime.GOOS != "windows"
}
