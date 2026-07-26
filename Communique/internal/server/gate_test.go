package server_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"orc/cq/internal/auth"
	"orc/cq/internal/protocol"
	"orc/cq/internal/server"
	"orc/cq/internal/store"
)

var at = time.Date(2026, 7, 24, 18, 31, 4, 0, time.UTC)

const password = "correct horse battery"

// harness is a configured server and everything needed to talk to it.
type harness struct {
	*server.Server
	state *store.Store
	creds *auth.Store
	token string
	now   time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()

	state, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := auth.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := creds.SetPassword(password, at); err != nil {
		t.Fatal(err)
	}
	token, _, err := creds.NewToken("studio", at)
	if err != nil {
		t.Fatal(err)
	}

	h := &harness{state: state, creds: creds, token: token, now: at}
	srv, err := server.New(server.Options{
		State: state, Creds: creds, Admin: true,
		Logger: slog.New(slog.DiscardHandler),
		Now:    func() time.Time { return h.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	h.Server = srv
	return h
}

// do sends a request and returns the recorded response.
func (h *harness) do(t *testing.T, method, path string, body string, mut ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, reader)
	r.RemoteAddr = "10.0.0.1:5555"
	for _, m := range mut {
		m(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// login exchanges the password for a cookie and a CSRF token.
func (h *harness) login(t *testing.T) (cookie, csrf string) {
	t.Helper()
	w := h.do(t, "POST", "/login", "password="+password, func(r *http.Request) {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Accept", "application/json")
	})
	if w.Code != http.StatusOK {
		t.Fatalf("login: status %d, body %s", w.Code, w.Body)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == "cq_session" {
			cookie = c.Value
		}
	}
	if cookie == "" {
		t.Fatalf("login set no session cookie")
	}

	got := h.do(t, "GET", "/api/v1/session", "", h.withCookie(cookie))
	if got.Code != http.StatusOK {
		t.Fatalf("session: status %d, body %s", got.Code, got.Body)
	}
	var view struct {
		CSRF string `json:"csrf"`
	}
	decodeInto(t, got.Body.Bytes(), &view)
	return cookie, view.CSRF
}

func (h *harness) withCookie(cookie string) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "cq_session", Value: cookie}) }
}

func (h *harness) withToken() func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+h.token) }
}

// TestNothingIsVisibleWithoutLogging is the milestone gate.
//
// It walks every registered route rather than a hand-written list, so an
// endpoint added without a credential is caught here rather than shipped. The
// exemption list is asserted to be exactly three, by name.
func TestNothingIsVisibleWithoutLogging(t *testing.T) {
	h := newHarness(t)
	patterns := h.Patterns()

	var public []string
	for pattern, class := range patterns {
		if class == "public" {
			public = append(public, pattern)
		}
	}
	want := map[string]bool{
		"GET /login":         true,
		"POST /login":        true,
		"GET /api/v1/health": true,
	}
	if len(public) != len(want) {
		t.Errorf("%d routes are public, want %d: %v", len(public), len(want), public)
	}
	for _, p := range public {
		if !want[p] {
			t.Errorf("route %q is public and should not be", p)
		}
	}

	for pattern, class := range patterns {
		if class == "public" {
			continue
		}
		t.Run(pattern, func(t *testing.T) {
			method, path, ok := strings.Cut(pattern, " ")
			if !ok {
				t.Fatalf("unparseable pattern %q", pattern)
			}
			path = concretePath(path)

			w := h.do(t, method, path, "{}")
			switch {
			case strings.HasPrefix(path, "/api/"):
				if w.Code != http.StatusUnauthorized {
					t.Errorf("status %d, want 401 without a credential (body %s)", w.Code, w.Body)
				}
			default:
				if w.Code != http.StatusSeeOther {
					t.Errorf("status %d, want a redirect to the login page", w.Code)
				}
				if got := w.Header().Get("Location"); got != "/login" {
					t.Errorf("Location = %q, want /login", got)
				}
			}
			if strings.Contains(w.Body.String(), "communiqué\n\nthe interface") {
				t.Errorf("the application was served to a stranger")
			}
		})
	}
}

// concretePath fills in path parameters so a pattern can actually be requested.
func concretePath(p string) string {
	p = strings.ReplaceAll(p, "{puid}", "1")
	p = strings.ReplaceAll(p, "{cuid}", "c")
	p = strings.ReplaceAll(p, "{name}", "t")
	return p
}

// TestUnknownPathsAlsoNeedASession keeps the server from being mapped by
// watching which unknown paths 404 and which redirect.
func TestUnknownPathsAlsoNeedASession(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/nope", "/api/v1/nope", "/admin", "/.env", "/api/../login"} {
		w := h.do(t, "GET", path, "")
		if w.Code == http.StatusNotFound {
			t.Errorf("%s answered 404 to a stranger, revealing that it does not exist", path)
		}
		if w.Code == http.StatusOK {
			t.Errorf("%s answered a stranger with content", path)
		}
	}
}

// TestTokenRoutesRefuseASessionAndViceVersa checks the two credentials are not
// interchangeable: a browser cannot drive a sync, and an agent cannot read mail.
func TestTokenRoutesRefuseASessionAndViceVersa(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login(t)

	w := h.do(t, "POST", "/api/v1/sync", "{}", h.withCookie(cookie),
		func(r *http.Request) { r.Header.Set("X-CSRF-Token", csrf) })
	if w.Code != http.StatusUnauthorized {
		t.Errorf("a session drove the sync endpoint: status %d", w.Code)
	}

	w = h.do(t, "GET", "/api/v1/inbox", "", h.withToken())
	if w.Code != http.StatusUnauthorized {
		t.Errorf("a sync token read the inbox: status %d", w.Code)
	}
}

func TestBearerRejections(t *testing.T) {
	h := newHarness(t)
	body := syncBody(t, "studio")

	for _, tc := range []struct{ name, header string }{
		{"absent", ""},
		{"wrong scheme", "Basic " + strings.Repeat("a", 20)},
		{"empty value", "Bearer "},
		{"nonsense", "Bearer not-a-token"},
		{"wrong secret", "Bearer 00000000000000ff.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := h.do(t, "POST", "/api/v1/sync", body, func(r *http.Request) {
				if tc.header != "" {
					r.Header.Set("Authorization", tc.header)
				}
			})
			if w.Code != http.StatusUnauthorized {
				t.Errorf("status %d, want 401", w.Code)
			}
			if strings.Contains(w.Body.String(), "digest") || strings.Contains(w.Body.String(), "mismatch") {
				t.Errorf("the refusal explained itself too well: %s", w.Body)
			}
		})
	}
}

// TestCSRFIsRequiredForStateChanges: a valid session is not enough for a write.
func TestCSRFIsRequiredForStateChanges(t *testing.T) {
	h := newHarness(t)
	putSnapshot(t, h, "studio")
	cookie, csrf := h.login(t)

	w := h.do(t, "POST", "/api/v1/messages/41/read", "", h.withCookie(cookie))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("a write without a csrf token succeeded: status %d", w.Code)
	}

	w = h.do(t, "POST", "/api/v1/messages/41/read", "", h.withCookie(cookie),
		func(r *http.Request) { r.Header.Set("X-CSRF-Token", "wrong") })
	if w.Code != http.StatusUnauthorized {
		t.Errorf("a write with a wrong csrf token succeeded: status %d", w.Code)
	}

	w = h.do(t, "POST", "/api/v1/messages/41/read", "", h.withCookie(cookie),
		func(r *http.Request) { r.Header.Set("X-CSRF-Token", csrf) })
	if w.Code != http.StatusAccepted {
		t.Errorf("a correct write was refused: status %d, body %s", w.Code, w.Body)
	}
}

// TestCrossOriginWritesAreRefused covers the forgery route a CSRF token alone
// would not, including the login form, which has no session to carry one.
func TestCrossOriginWritesAreRefused(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login(t)

	w := h.do(t, "POST", "/api/v1/messages/41/read", "", h.withCookie(cookie),
		func(r *http.Request) {
			r.Header.Set("X-CSRF-Token", csrf)
			r.Header.Set("Origin", "https://evil.example")
		})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("a cross-origin write succeeded: status %d", w.Code)
	}

	w = h.do(t, "POST", "/login", "password="+password, func(r *http.Request) {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://evil.example")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
	})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("a cross-origin login succeeded: status %d", w.Code)
	}

	// A same-origin write is unaffected.
	w = h.do(t, "POST", "/api/v1/messages/41/read", "", h.withCookie(cookie),
		func(r *http.Request) {
			r.Header.Set("X-CSRF-Token", csrf)
			r.Header.Set("Origin", "http://"+r.Host)
		})
	if w.Code == http.StatusUnauthorized {
		t.Errorf("a same-origin write was refused")
	}
}

// TestFetchMetadataDecidesWhereItCan.
//
// Sec-Fetch-Site is set by the browser and cannot be forged by a page, so it
// is trusted over Origin — which matters for a document whose origin is opaque
// and reads as "null", a value that is neither same-origin nor identifiably
// hostile.
func TestFetchMetadataDecidesWhereItCan(t *testing.T) {
	h := newHarness(t)
	putSnapshot(t, h, "studio")
	cookie, csrf := h.login(t)

	write := func(site, origin string) int {
		return h.do(t, "POST", "/api/v1/messages/41/read", "", h.withCookie(cookie),
			func(r *http.Request) {
				r.Header.Set("X-CSRF-Token", csrf)
				if site != "" {
					r.Header.Set("Sec-Fetch-Site", site)
				}
				if origin != "" {
					r.Header.Set("Origin", origin)
				}
			}).Code
	}

	for _, tc := range []struct {
		name   string
		site   string
		origin string
		want   int
	}{
		{"same origin", "same-origin", "http://example.test", http.StatusAccepted},
		{"an opaque origin the browser calls same-origin", "same-origin", "null", http.StatusAccepted},
		{"a typed URL", "none", "", http.StatusAccepted},
		{"cross site", "cross-site", "http://example.test", http.StatusUnauthorized},
		{"same site, different origin", "same-site", "http://other.test", http.StatusUnauthorized},
		{"no metadata, opaque origin", "", "null", http.StatusUnauthorized},
		{"no metadata, no origin", "", "", http.StatusAccepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := write(tc.site, tc.origin); got != tc.want {
				t.Errorf("status %d, want %d", got, tc.want)
			}
		})
	}
}

func TestLogoutEndsTheSessionOnTheServer(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login(t)

	w := h.do(t, "POST", "/api/v1/logout", "", h.withCookie(cookie),
		func(r *http.Request) { r.Header.Set("X-CSRF-Token", csrf) })
	if w.Code != http.StatusOK {
		t.Fatalf("logout: status %d, body %s", w.Code, w.Body)
	}
	if got := h.do(t, "GET", "/api/v1/session", "", h.withCookie(cookie)); got.Code != http.StatusUnauthorized {
		t.Errorf("the cookie still works after logout: status %d", got.Code)
	}
}

func TestExpiredSessionsStopWorking(t *testing.T) {
	h := newHarness(t)
	cookie, _ := h.login(t)

	h.now = at.Add(server.SessionLifetime + time.Hour)
	if got := h.do(t, "GET", "/api/v1/session", "", h.withCookie(cookie)); got.Code != http.StatusUnauthorized {
		t.Errorf("an expired session still works: status %d", got.Code)
	}
}

// TestNewRefusesToStartUnconfigured is the other half of the gate: a login page
// with no password behind it is not a gate.
func TestNewRefusesToStartUnconfigured(t *testing.T) {
	root := t.TempDir()
	state, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := auth.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	opts := server.Options{State: state, Creds: creds, Logger: slog.New(slog.DiscardHandler)}

	if _, err := server.New(opts); err == nil {
		t.Fatalf("a server with no credentials started")
	}
	if err := creds.SetPassword(password, at); err != nil {
		t.Fatal(err)
	}
	if _, err := server.New(opts); err == nil {
		t.Fatalf("a server with no sync token started")
	}
	if _, _, err := creds.NewToken("studio", at); err != nil {
		t.Fatal(err)
	}
	if _, err := server.New(opts); err != nil {
		t.Errorf("a configured server refused to start: %v", err)
	}
}

func TestNewRequiresItsDependencies(t *testing.T) {
	root := t.TempDir()
	state, _ := store.Open(root)
	creds, _ := auth.Open(root)
	log := slog.New(slog.DiscardHandler)

	for _, tc := range []struct {
		name string
		opts server.Options
	}{
		{"no state", server.Options{Creds: creds, Logger: log}},
		{"no credentials", server.Options{State: state, Logger: log}},
		{"no logger", server.Options{State: state, Creds: creds}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := server.New(tc.opts); err == nil {
				t.Errorf("a server started without its %s", tc.name)
			}
		})
	}
}

// TestSecurityHeadersAreOnEveryResponse, including the ones a stranger sees.
func TestSecurityHeadersAreOnEveryResponse(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/login", "/api/v1/health", "/nope"} {
		w := h.do(t, "GET", path, "")
		for header, want := range map[string]string{
			"X-Content-Type-Options": "nosniff",
			"Referrer-Policy":        "no-referrer",
			"X-Frame-Options":        "DENY",
		} {
			if got := w.Header().Get(header); got != want {
				t.Errorf("%s: %s = %q, want %q", path, header, got, want)
			}
		}
		csp := w.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'self'") {
			t.Errorf("%s: content policy = %q", path, csp)
		}
		if strings.Contains(csp, "unsafe-inline") {
			t.Errorf("%s: the content policy allows inline code: %q", path, csp)
		}
	}
}

// TestLoginPageDoesNotLeakTheApplication checks a stranger receives a password
// box and nothing else.
func TestLoginPageDoesNotLeakTheApplication(t *testing.T) {
	h := newHarness(t)
	w := h.do(t, "GET", "/login", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `type="password"`) {
		t.Errorf("the login page has no password box")
	}
	for _, leak := range []string{"/api/v1/inbox", "/api/v1/admin", "app.js", "inbox"} {
		if strings.Contains(body, leak) {
			t.Errorf("the login page mentions %q", leak)
		}
	}
	// Its stylesheet is permitted by hash, not by relaxing the policy.
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "sha256-") || strings.Contains(csp, "unsafe-inline") {
		t.Errorf("login content policy = %q", csp)
	}
}

func TestLoginRateLimitSlowsGuessing(t *testing.T) {
	h := newHarness(t)
	form := func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }

	w := h.do(t, "POST", "/login", "password=wrong", form)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", w.Code)
	}
	w = h.do(t, "POST", "/login", "password="+password, form)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("a second attempt was not slowed: status %d", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Errorf("a rate-limited response should say how long to wait")
	}

	// Waiting clears it, and the correct password then works.
	h.now = at.Add(time.Minute)
	w = h.do(t, "POST", "/login", "password="+password, form)
	if w.Code != http.StatusSeeOther {
		t.Errorf("after waiting, a correct login failed: status %d", w.Code)
	}
}

func TestAlreadyLoggedInSkipsTheLoginPage(t *testing.T) {
	h := newHarness(t)
	cookie, _ := h.login(t)
	w := h.do(t, "GET", "/login", "", h.withCookie(cookie))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Errorf("status %d, Location %q; want a redirect to the app", w.Code, w.Header().Get("Location"))
	}
}

func TestHealthAnswersAStrangerAndSaysNothing(t *testing.T) {
	h := newHarness(t)
	putSnapshot(t, h, "studio")

	w := h.do(t, "GET", "/api/v1/health", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	body := w.Body.String()
	for _, leak := range []string{"studio", "redjive", "boss", "machine"} {
		if strings.Contains(body, leak) {
			t.Errorf("health leaked %q: %s", leak, body)
		}
	}
}

func putSnapshot(t *testing.T, h *harness, machine protocol.MachineID) {
	t.Helper()
	if err := h.state.PutSnapshot(sampleSnapshot(machine), "cq/test", h.now); err != nil {
		t.Fatal(err)
	}
}

// newHarnessWithAdmin builds a second server over an existing store, so the
// admin switch can be tested without a second login.
func newHarnessWithAdmin(t *testing.T, from *harness, admin bool) *harness {
	t.Helper()
	h := &harness{state: from.state, creds: from.creds, token: from.token, now: from.now}
	srv, err := server.New(server.Options{
		State: from.state, Creds: from.creds, Admin: admin,
		Logger: slog.New(slog.DiscardHandler),
		Now:    func() time.Time { return h.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	h.Server = srv
	return h
}
