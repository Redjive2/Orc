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
// exemption list is asserted by name, and every entry has to earn its place:
// two are the login itself, one says the process is alive, and one is the tab
// icon a browser fetches before anybody has logged in.
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
		// Requested unprompted, with no session and no referrer, and the login
		// page is where recognising the tab is worth most. It discloses that this
		// is cq, which that page already says in words.
		"GET /favicon.ico": true,
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

	// One mistype costs nothing: the right password straight after it works. This
	// is the case the guard is *not* for, and the one a person meets first — on a
	// phone especially, where the retry is immediate.
	if got := h.do(t, "POST", "/login", "password="+password, form); got.Code != http.StatusSeeOther {
		t.Fatalf("a correct password after one mistype was refused: status %d", got.Code)
	}

	// Guessing is what is slowed. Two consecutive failures, and the next attempt
	// waits — even with the right password, since a guesser would have one too.
	h.do(t, "POST", "/login", "password=wrong", form)
	h.do(t, "POST", "/login", "password=wrong", form)
	w = h.do(t, "POST", "/login", "password="+password, form)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("repeated guessing was not slowed: status %d", w.Code)
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

// TestTheFaviconIsServedToAStranger.
//
// A browser asks for it with no session and no referrer, and it is the one
// static file that answers. Three things have to hold: it arrives, it arrives as
// an icon, and it is allowed to be cached — it is the largest thing cq serves,
// and the no-cache header every other route carries would re-send it on every
// page load.
func TestTheFaviconIsServedToAStranger(t *testing.T) {
	h := newHarness(t)

	rec := h.do(t, "GET", "/favicon.ico", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a stranger's browser gets no icon", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/x-icon" {
		t.Errorf("Content-Type = %q, want image/x-icon", got)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "max-age") {
		t.Errorf("Cache-Control = %q; the icon is re-sent on every page load", got)
	}

	// The ICO magic: a zero reserved word, then type 1.
	body := rec.Body.Bytes()
	if len(body) < 4 || body[0] != 0 || body[1] != 0 || body[2] != 1 || body[3] != 0 {
		t.Errorf("what was served is not an icon (first bytes %v)", body[:min(4, len(body))])
	}
}

// TestABrowserNeverGetsRawJSONFromTheLoginForm.
//
// Both refusals on the login path — rate limiting and the origin check — happen in
// middleware, before `postLogin` runs, and both used to answer with the API's JSON
// error. In a browser that replaces the password box with `{"error":{…}}`, leaving
// somebody with nothing to type into and no way back except editing the URL.
//
// It is a phone that meets this first: the keyboard is small, the retry after a
// mistype is immediate, and the password manager will refill and resubmit for you.
func TestLoginRefusalsAreThePageForABrowser(t *testing.T) {
	h := newHarness(t)
	// What a browser sends when a form is submitted: fetch metadata saying this is
	// a navigation, and an Accept that asks for a document.
	browser := func(r *http.Request) {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		r.Header.Set("Sec-Fetch-Mode", "navigate")
		r.Header.Set("Sec-Fetch-Dest", "document")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
	}

	// Guess until the limiter refuses.
	var w *httptest.ResponseRecorder
	for range 3 {
		w = h.do(t, "POST", "/login", "password=wrong", browser)
	}
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429 — the limiter should have refused by now", w.Code)
	}
	body := w.Body.String()
	if strings.HasPrefix(strings.TrimSpace(body), "{") {
		t.Errorf("a browser was answered with raw JSON:\n%s", body)
	}
	// The form is still there, so the next attempt is a thing somebody can make.
	if !strings.Contains(body, `type="password"`) {
		t.Errorf("the refusal did not leave a password box:\n%s", body)
	}
	// And it says how long, since this is the one refusal meant to be retried.
	if !strings.Contains(body, "try again in") {
		t.Errorf("the refusal does not say how long to wait:\n%s", body)
	}

	// A cross-site form post is refused the same way, for the same reason.
	h.now = at.Add(time.Hour)
	w = h.do(t, "POST", "/login", "password=wrong", func(r *http.Request) {
		browser(r)
		r.Header.Set("Sec-Fetch-Site", "cross-site")
	})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("a cross-site login post gave %d, want 401", w.Code)
	}
	if strings.HasPrefix(strings.TrimSpace(w.Body.String()), "{") {
		t.Errorf("a cross-site refusal was raw JSON:\n%s", w.Body.String())
	}
}

// And a program still gets JSON, with a status it can branch on: 429 for "slow
// down" is a different instruction from 401 for "wrong password".
func TestLoginRefusalsStayJSONForAnAPIClient(t *testing.T) {
	h := newHarness(t)
	api := func(r *http.Request) {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Accept", "application/json")
		r.Header.Set("Sec-Fetch-Mode", "cors")
	}

	var w *httptest.ResponseRecorder
	for range 3 {
		w = h.do(t, "POST", "/login", "password=wrong", api)
	}
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status %d, want 429", w.Code)
	}
	if !strings.HasPrefix(strings.TrimSpace(w.Body.String()), "{") {
		t.Errorf("an API client did not get JSON:\n%s", w.Body.String())
	}
	if w.Header().Get("Retry-After") == "" {
		t.Errorf("no Retry-After on a rate-limited response")
	}
}

// TestOneMistypeCostsNothing. The limiter's whole design note says an operator who
// mistypes once notices nothing; before this it was refused on the very next attempt,
// which is what a phone does within a second of getting it wrong.
func TestAMistypeDoesNotLockYouOut(t *testing.T) {
	h := newHarness(t)
	form := func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }

	if got := h.do(t, "POST", "/login", "password=wrong", form); got.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong password gave %d, want 401", got.Code)
	}
	// Immediately, on the same clock: no waiting, no second page to get past.
	if got := h.do(t, "POST", "/login", "password="+password, form); got.Code != http.StatusSeeOther {
		t.Errorf("the correct password straight after a mistype gave %d, want 303", got.Code)
	}
}
