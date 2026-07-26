package auth_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"orc/cq/internal/auth"
	"orc/cq/internal/fault"
)

var at = time.Date(2026, 7, 24, 18, 31, 4, 0, time.UTC)

const password = "correct horse battery"

func open(t *testing.T) *auth.Store {
	t.Helper()
	s, err := auth.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestOpenRejectsAnEmptyPath(t *testing.T) {
	if _, err := auth.Open(""); !errors.Is(err, fault.ErrUsage) {
		t.Errorf("error = %v, want a usage fault", err)
	}
}

// TestServeRefusesToStartUnconfigured is the milestone gate: a login gate with
// nothing behind it is not a gate.
func TestServeRefusesToStartUnconfigured(t *testing.T) {
	s := open(t)

	err := s.Configured()
	if !errors.Is(err, fault.ErrUsage) {
		t.Fatalf("a fresh store should refuse to serve, got %v", err)
	}
	for _, want := range []string{"operator", "token"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q should say which credential is missing", err)
		}
	}

	if err := s.SetPassword(password, at); err != nil {
		t.Fatal(err)
	}
	err = s.Configured()
	if !errors.Is(err, fault.ErrUsage) || !strings.Contains(err.Error(), "token") {
		t.Errorf("with only a password set, the missing token should be named: %v", err)
	}

	if _, _, err := s.NewToken("agent", at); err != nil {
		t.Fatal(err)
	}
	if err := s.Configured(); err != nil {
		t.Errorf("a fully configured store should serve: %v", err)
	}
}

func TestPasswordRoundTrip(t *testing.T) {
	s := open(t)
	if s.HasPassword() {
		t.Errorf("a fresh store has no password")
	}
	if err := s.SetPassword(password, at); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if !s.HasPassword() {
		t.Errorf("HasPassword should now be true")
	}
	if err := s.VerifyPassword(password); err != nil {
		t.Errorf("the correct password was rejected: %v", err)
	}
	if err := s.VerifyPassword(password + "!"); !errors.Is(err, fault.ErrUnauthenticated) {
		t.Errorf("a wrong password should be unauthenticated, got %v", err)
	}
}

// TestPasswordIsNeverStored checks the file holds a digest and nothing else.
func TestPasswordIsNeverStored(t *testing.T) {
	s := open(t)
	if err := s.SetPassword(password, at); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(s.Root(), "operator.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), password) {
		t.Errorf("the password is in the store:\n%s", raw)
	}
	for _, word := range strings.Fields(password) {
		if strings.Contains(string(raw), word) {
			t.Errorf("part of the password is in the store: %q", word)
		}
	}
	if !strings.Contains(string(raw), "pbkdf2-hmac-sha512") {
		t.Errorf("the record should name its algorithm:\n%s", raw)
	}
}

func TestPasswordFloor(t *testing.T) {
	s := open(t)
	for _, tc := range []struct{ name, pw, want string }{
		{"too short", "short", "at least"},
		{"only whitespace", "          ", "whitespace"},
		{"absurdly long", strings.Repeat("x", auth.MaxPasswordBytes+1), "at most"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := s.SetPassword(tc.pw, at)
			if !errors.Is(err, fault.ErrUsage) {
				t.Fatalf("error = %v, want a usage fault", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message %q should mention %q", err, tc.want)
			}
		})
	}
}

// TestVerificationFailsClosed is the rule that matters most in this package: no
// damaged or missing record may become "no password required".
func TestVerificationFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		{"absent", ""},
		{"empty file", `{}`},
		{"unknown algorithm", `{"algo":"rot13","iterations":210000,"salt":"AAAAAAAAAAAAAAAAAAAAAA","digest":"AAAA","updated":"2026-07-24T18:31:04Z"}`},
		{"derisory iterations", `{"algo":"pbkdf2-hmac-sha512","iterations":1,"salt":"AAAAAAAAAAAAAAAAAAAAAA","digest":"AAAA","updated":"2026-07-24T18:31:04Z"}`},
		{"unreadable salt", `{"algo":"pbkdf2-hmac-sha512","iterations":210000,"salt":"!!!","digest":"AAAA","updated":"2026-07-24T18:31:04Z"}`},
		{"missing digest", `{"algo":"pbkdf2-hmac-sha512","iterations":210000,"salt":"AAAAAAAAAAAAAAAAAAAAAA","digest":"","updated":"2026-07-24T18:31:04Z"}`},
		{"truncated json", `{"algo":"pbkdf2`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := open(t)
			if tc.content != "" {
				path := filepath.Join(s.Root(), "operator.json")
				if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if s.HasPassword() {
				t.Errorf("a %s record must not count as configured", tc.name)
			}
			for _, attempt := range []string{"", password, "anything"} {
				if err := s.VerifyPassword(attempt); !errors.Is(err, fault.ErrUnauthenticated) {
					t.Errorf("a %s record accepted %q: %v", tc.name, attempt, err)
				}
			}
		})
	}
}

func TestPasswordFailuresAreIndistinguishable(t *testing.T) {
	s := open(t)
	if err := s.SetPassword(password, at); err != nil {
		t.Fatal(err)
	}
	wrong := s.VerifyPassword("not it")

	empty := open(t)
	missing := empty.VerifyPassword("not it")

	if fault.Public(wrong) != fault.Public(missing) {
		t.Errorf("a wrong password and an unconfigured server are distinguishable: %q vs %q",
			fault.Public(wrong), fault.Public(missing))
	}
	if fault.Public(wrong) != "not authenticated" {
		t.Errorf("public message = %q, want no detail", fault.Public(wrong))
	}
}

func TestTokenRoundTrip(t *testing.T) {
	s := open(t)
	if s.HasToken() {
		t.Errorf("a fresh store has no token")
	}

	secret, rec, err := s.NewToken("studio", at)
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if !s.HasToken() {
		t.Errorf("HasToken should now be true")
	}

	got, err := s.VerifyToken(secret)
	if err != nil {
		t.Fatalf("the minted token was rejected: %v", err)
	}
	if got.ID != rec.ID || got.Label != "studio" {
		t.Errorf("verified record = %+v, want %+v", got, rec)
	}
}

// TestTokenSecretIsNeverStored checks the secret exists only in the one string
// NewToken returns.
func TestTokenSecretIsNeverStored(t *testing.T) {
	s := open(t)
	secret, rec, err := s.NewToken("studio", at)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(s.Root(), "tokens", rec.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	_, secretPart, _ := strings.Cut(secret, ".")
	if strings.Contains(string(raw), secretPart) {
		t.Errorf("the token secret is in the store:\n%s", raw)
	}
}

func TestTokenRejections(t *testing.T) {
	s := open(t)
	good, rec, err := s.NewToken("studio", at)
	if err != nil {
		t.Fatal(err)
	}
	_, goodSecret, _ := strings.Cut(good, ".")

	for _, tc := range []struct{ name, presented string }{
		{"empty", ""},
		{"no separator", "deadbeefdeadbeef"},
		{"bad id", "zzzz." + goodSecret},
		{"unknown id", "00000000000000ff." + goodSecret},
		{"unreadable secret", rec.ID + ".!!!!"},
		{"wrong length secret", rec.ID + ".AAAA"},
		{"wrong secret", rec.ID + "." + strings.Repeat("A", len(goodSecret))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.VerifyToken(tc.presented); !errors.Is(err, fault.ErrUnauthenticated) {
				t.Errorf("error = %v, want unauthenticated", err)
			}
		})
	}
}

func TestTokenRecordsAreListedAndRemovable(t *testing.T) {
	s := open(t)
	if list, err := s.Tokens(); err != nil || len(list) != 0 {
		t.Errorf("a fresh store lists no tokens: %v %v", list, err)
	}

	_, first, err := s.NewToken("studio", at)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.NewToken("laptop", at); err != nil {
		t.Fatal(err)
	}

	list, err := s.Tokens()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("listed %d tokens, want 2", len(list))
	}
	for _, tok := range list {
		if tok.Digest == "" || tok.Salt == "" {
			t.Errorf("a listed record is incomplete: %+v", tok)
		}
	}

	if err := s.RemoveToken(first.ID); err != nil {
		t.Fatalf("RemoveToken: %v", err)
	}
	if list, err = s.Tokens(); err != nil || len(list) != 1 {
		t.Errorf("after removal: %v %v", list, err)
	}
	if err := s.RemoveToken("nonsense"); !errors.Is(err, fault.ErrUsage) {
		t.Errorf("an invalid id should be a usage fault, got %v", err)
	}
}

func TestTouchTokenRecordsUse(t *testing.T) {
	s := open(t)
	_, rec, err := s.NewToken("studio", at)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.LastSeen.IsZero() {
		t.Errorf("a new token has not been seen")
	}
	later := at.Add(time.Hour)
	if err := s.TouchToken(rec.ID, later); err != nil {
		t.Fatalf("TouchToken: %v", err)
	}
	list, err := s.Tokens()
	if err != nil {
		t.Fatal(err)
	}
	if !list[0].LastSeen.Equal(later) {
		t.Errorf("last seen = %v, want %v", list[0].LastSeen, later)
	}
	if err := s.TouchToken("00000000000000ff", later); err == nil {
		t.Errorf("touching an unknown token should fail")
	}
}

func TestTokenLabelIsBounded(t *testing.T) {
	s := open(t)
	if _, _, err := s.NewToken(strings.Repeat("x", 65), at); !errors.Is(err, fault.ErrUsage) {
		t.Errorf("error = %v, want a usage fault", err)
	}
	if _, _, err := s.NewToken("studio", time.Time{}); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("a zero timestamp should be internal, got %v", err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	s := open(t)
	cookie, rec, err := s.NewSession(at, time.Hour)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if cookie == "" || rec.CSRF == "" {
		t.Fatalf("session = %q %+v", cookie, rec)
	}

	got, err := s.Session(cookie, at.Add(time.Minute))
	if err != nil {
		t.Fatalf("a live session was rejected: %v", err)
	}
	if got.CSRF != rec.CSRF {
		t.Errorf("csrf token changed between create and lookup")
	}

	if err := got.CheckCSRF(rec.CSRF); err != nil {
		t.Errorf("the matching csrf token was rejected: %v", err)
	}
	if err := got.CheckCSRF("wrong"); !errors.Is(err, fault.ErrUnauthenticated) {
		t.Errorf("a wrong csrf token should be unauthenticated, got %v", err)
	}
	if err := got.CheckCSRF(""); !errors.Is(err, fault.ErrUnauthenticated) {
		t.Errorf("a missing csrf token should be unauthenticated, got %v", err)
	}

	if err := s.EndSession(cookie); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	if _, err := s.Session(cookie, at.Add(time.Minute)); !errors.Is(err, fault.ErrUnauthenticated) {
		t.Errorf("a logged-out session should be gone, got %v", err)
	}
}

// TestSessionCookieIsNotStored means reading the store cannot hand out a live
// session.
func TestSessionCookieIsNotStored(t *testing.T) {
	s := open(t)
	cookie, rec, err := s.NewSession(at, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(s.Root(), "sessions", rec.Hash+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), cookie) {
		t.Errorf("the session cookie is in the store:\n%s", raw)
	}
}

func TestExpiredSessionsAreRefusedAndRemoved(t *testing.T) {
	s := open(t)
	cookie, rec, err := s.NewSession(at, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	after := at.Add(time.Hour)
	if _, err := s.Session(cookie, after); !errors.Is(err, fault.ErrUnauthenticated) {
		t.Errorf("an expired session should be refused, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Root(), "sessions", rec.Hash+".json")); !os.IsNotExist(err) {
		t.Errorf("an expired session should be deleted as it is found")
	}
}

func TestSessionRejections(t *testing.T) {
	s := open(t)
	for _, tc := range []struct{ name, cookie string }{
		{"empty", ""},
		{"not base64", "!!!!"},
		{"wrong length", "AAAA"},
		{"unknown", strings.Repeat("A", 43)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Session(tc.cookie, at); !errors.Is(err, fault.ErrUnauthenticated) {
				t.Errorf("error = %v, want unauthenticated", err)
			}
		})
	}
	if err := s.EndSession(""); err != nil {
		t.Errorf("ending an absent session should succeed, got %v", err)
	}
	if _, _, err := s.NewSession(at, 0); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("a non-positive lifetime should be internal, got %v", err)
	}
	if _, _, err := s.NewSession(time.Time{}, time.Hour); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("a zero timestamp should be internal, got %v", err)
	}
}

func TestSweepSessionsRemovesExpiredAndUnreadable(t *testing.T) {
	s := open(t)
	live, _, err := s.NewSession(at, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.NewSession(at, time.Minute); err != nil {
		t.Fatal(err)
	}
	// A record nothing can authenticate with is also one worth sweeping.
	if err := os.WriteFile(filepath.Join(s.Root(), "sessions", "junk.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	n, err := s.SweepSessions(at.Add(30 * time.Minute))
	if err != nil {
		t.Fatalf("SweepSessions: %v", err)
	}
	if n != 2 {
		t.Errorf("swept %d sessions, want 2", n)
	}
	if _, err := s.Session(live, at.Add(30*time.Minute)); err != nil {
		t.Errorf("the live session was swept: %v", err)
	}
}

func TestSessionRecordValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		rec  auth.Session
		want string
	}{
		{"bad hash", auth.Session{Hash: "short", CSRF: strings.Repeat("c", 20), Created: at, Expires: at.Add(time.Hour)}, "SHA-256"},
		{"short csrf", auth.Session{Hash: strings.Repeat("a", 64), CSRF: "x", Created: at, Expires: at.Add(time.Hour)}, "csrf"},
		{"no lifetime", auth.Session{Hash: strings.Repeat("a", 64), CSRF: strings.Repeat("c", 20)}, "lifetime"},
		{"expires first", auth.Session{Hash: strings.Repeat("a", 64), CSRF: strings.Repeat("c", 20), Created: at, Expires: at.Add(-time.Hour)}, "expires before"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.rec.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, should mention %q", err, tc.want)
			}
		})
	}
}

func TestTokenRecordValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		rec  auth.Token
		want string
	}{
		{"bad id", auth.Token{ID: "nope"}, "16 hex"},
		{"short salt", auth.Token{ID: strings.Repeat("a", 16), Salt: "AAAA"}, "salt"},
		{"wrong digest size", auth.Token{ID: strings.Repeat("a", 16), Salt: strings.Repeat("A", 43), Digest: "AAAA"}, "digest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.rec.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, should mention %q", err, tc.want)
			}
		})
	}
}

func TestLimiterSlowsRepeatedFailures(t *testing.T) {
	l := auth.NewLimiter()
	now := at

	if ok, _ := l.Allow("1.2.3.4", now); !ok {
		t.Fatalf("a fresh source should be allowed")
	}

	l.Fail("1.2.3.4", now)
	ok, wait := l.Allow("1.2.3.4", now)
	if ok {
		t.Errorf("a source that just failed should wait")
	}
	if wait <= 0 {
		t.Errorf("wait = %v, want a positive delay", wait)
	}

	// The delay grows.
	l.Fail("1.2.3.4", now)
	l.Fail("1.2.3.4", now)
	_, longer := l.Allow("1.2.3.4", now)
	if longer <= wait {
		t.Errorf("delay did not grow: %v then %v", wait, longer)
	}

	// It is capped.
	for range 40 {
		l.Fail("1.2.3.4", now)
	}
	_, capped := l.Allow("1.2.3.4", now)
	if capped > l.Max {
		t.Errorf("delay %v exceeds the cap %v", capped, l.Max)
	}

	// Another source is unaffected.
	if ok, _ := l.Allow("5.6.7.8", now); !ok {
		t.Errorf("one source's failures should not slow another")
	}

	// Success clears it.
	l.Succeed("1.2.3.4")
	if ok, _ := l.Allow("1.2.3.4", now); !ok {
		t.Errorf("a successful attempt should clear the history")
	}

	// Waiting long enough clears it too.
	l.Fail("9.9.9.9", now)
	if ok, _ := l.Allow("9.9.9.9", now.Add(2*l.Max)); !ok {
		t.Errorf("the delay should expire")
	}
}

func TestLimiterSweepsOldSources(t *testing.T) {
	l := auth.NewLimiter()
	l.Fail("1.2.3.4", at)
	l.Sweep(at.Add(10 * l.Max))
	if ok, _ := l.Allow("1.2.3.4", at); !ok {
		t.Errorf("a swept source should start clean")
	}
}

func TestLimiterIsSafeUnderConcurrency(t *testing.T) {
	l := auth.NewLimiter()
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			source := string(rune('a' + i%4))
			l.Fail(source, at)
			l.Allow(source, at)
			l.Succeed(source)
			l.Sweep(at)
		}()
	}
	wg.Wait()
}
