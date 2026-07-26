// Package auth holds cq's two credentials and the sessions they open.
//
// The two are verified differently, and the difference is the point:
//
//   - The **sync token** is machine-minted, 256 bits, and presented on every
//     sync. Guessing it is already infeasible, so a slow KDF would buy nothing
//     and cost work on every request. HMAC-SHA256 over a stored salt.
//   - The **operator password** is human-chosen and verified once per session.
//     It is the one low-entropy secret in Orc, so it gets a real KDF —
//     PBKDF2-HMAC-SHA512, in the standard library since Go 1.24, which keeps cq
//     dependency-free.
//
// Mailman's plan reaches the opposite conclusion from the same reasoning, for
// the opposite kind of secret. Both are right.
package auth

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"orc/cq/internal/atomic"
	"orc/cq/internal/fault"
)

// Parameters chosen once and recorded in every stored record, so they can be
// raised later without invalidating what is already written.
const (
	// PBKDF2Iterations follows OWASP's guidance for PBKDF2-HMAC-SHA512.
	PBKDF2Iterations = 210_000
	// PBKDF2KeyLen is SHA-512's output size; there is nothing to gain by asking
	// for less and nothing but confusion by asking for more.
	PBKDF2KeyLen = 64

	// SaltBytes is the per-record salt.
	SaltBytes = 32
	// TokenSecretBytes is the entropy in a sync token.
	TokenSecretBytes = 32
	// SessionBytes is the entropy in a session cookie and in a CSRF token.
	SessionBytes = 32

	// MinPasswordRunes is a floor, not a policy. cq refuses to be configured
	// with something that is obviously not a password; it does not lecture.
	MinPasswordRunes = 8
	// MaxPasswordBytes bounds the KDF's input so a huge body cannot make the
	// login endpoint expensive.
	MaxPasswordBytes = 1024
)

const (
	dirMode  os.FileMode = 0o700
	fileMode os.FileMode = 0o600
)

// enc is the alphabet for every secret cq hands out: URL-safe, unpadded, so a
// token survives a query string, a header, and a shell without quoting.
var enc = base64.RawURLEncoding

// Store holds cq's credentials on disk. It is rooted at the same directory as
// the state store, but is a separate type: credentials and mail have nothing to
// say to each other, and keeping them apart means nothing that handles a
// snapshot can reach a password digest by accident.
type Store struct {
	root string
}

// Open prepares a credential store at root, creating its directories.
func Open(root string) (*Store, error) {
	if root == "" {
		return nil, fault.Usage{Reason: "empty store path"}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fault.IO{Op: "resolve", Subject: root, Err: err}
	}
	for _, dir := range []string{"", "tokens", "sessions"} {
		if err := atomic.MkdirAll(filepath.Join(abs, dir), dirMode); err != nil {
			return nil, err
		}
	}
	return &Store{root: abs}, nil
}

// Root returns the credential store's directory.
func (s *Store) Root() string { return s.root }

func (s *Store) path(parts ...string) string {
	return filepath.Join(append([]string{s.root}, parts...)...)
}

// Configured reports whether cq has both credentials it needs to serve.
// `cq serve` refuses to start when this is false: a login gate with no password
// behind it is not a gate.
func (s *Store) Configured() error {
	switch {
	case !s.HasPassword() && !s.HasToken():
		return fault.Usage{Reason: "no operator password and no sync token are set; run `cq admin operator` and `cq admin token`"}
	case !s.HasPassword():
		return fault.Usage{Reason: "no operator password is set; run `cq admin operator`"}
	case !s.HasToken():
		return fault.Usage{Reason: "no sync token is set; run `cq admin token`"}
	default:
		return nil
	}
}

// --- the operator password -----------------------------------------------

// Operator is the stored form of the login password. The password itself is
// never written anywhere.
type Operator struct {
	Algo       string    `json:"algo"`
	Iterations int       `json:"iterations"`
	Salt       string    `json:"salt"`
	Digest     string    `json:"digest"`
	Updated    time.Time `json:"updated"`
}

// Validate checks a stored record is usable. A record that is malformed,
// truncated, or of an unknown algorithm fails closed: there is no path here on
// which a damaged file becomes "no password required".
func (o Operator) Validate() error {
	if o.Algo != "pbkdf2-hmac-sha512" {
		return fault.Field("Operator", "algo", "unknown algorithm %q", o.Algo)
	}
	if o.Iterations < 1000 {
		return fault.Field("Operator", "iterations", "%d is too few to be a real setting", o.Iterations)
	}
	salt, err := enc.DecodeString(o.Salt)
	if err != nil || len(salt) < 16 {
		return fault.Field("Operator", "salt", "salt is missing or too short")
	}
	digest, err := enc.DecodeString(o.Digest)
	if err != nil || len(digest) == 0 {
		return fault.Field("Operator", "digest", "digest is missing or unreadable")
	}
	return nil
}

// SetPassword writes a new operator record, replacing any existing one.
func (s *Store) SetPassword(password string, at time.Time) error {
	if err := checkPassword(password); err != nil {
		return err
	}
	salt := make([]byte, SaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return fault.IO{Op: "read", Subject: "system randomness", Err: err}
	}
	digest, err := pbkdf2.Key(sha512.New, password, salt, PBKDF2Iterations, PBKDF2KeyLen)
	if err != nil {
		return fault.Internal{Where: "auth.SetPassword", Detail: "key derivation failed: " + err.Error()}
	}

	rec := Operator{
		Algo:       "pbkdf2-hmac-sha512",
		Iterations: PBKDF2Iterations,
		Salt:       enc.EncodeToString(salt),
		Digest:     enc.EncodeToString(digest),
		Updated:    at,
	}
	if err := rec.Validate(); err != nil {
		return err
	}
	return atomic.WriteJSON(s.path("operator.json"), rec, fileMode)
}

// HasPassword reports whether an operator record exists and is usable. It is
// what `cq serve` checks before it agrees to start.
func (s *Store) HasPassword() bool {
	_, err := s.operator()
	return err == nil
}

func (s *Store) operator() (Operator, error) {
	var rec Operator
	if err := atomic.ReadJSON(s.path("operator.json"), &rec); err != nil {
		return Operator{}, err
	}
	if err := rec.Validate(); err != nil {
		return Operator{}, err
	}
	return rec, nil
}

// VerifyPassword reports whether the password matches the stored record.
//
// Every failure — no record, a corrupt record, the wrong password — returns the
// same fault, because telling a caller which one it was tells them half the
// answer. The reason is kept for the operator's log, not the response.
func (s *Store) VerifyPassword(password string) error {
	rec, err := s.operator()
	if err != nil {
		return fault.Unauthenticated{Reason: "no usable operator record: " + err.Error()}
	}
	if len(password) > MaxPasswordBytes {
		return fault.Unauthenticated{Reason: "password exceeds the accepted length"}
	}
	salt, err := enc.DecodeString(rec.Salt)
	if err != nil {
		return fault.Unauthenticated{Reason: "operator salt is unreadable"}
	}
	want, err := enc.DecodeString(rec.Digest)
	if err != nil {
		return fault.Unauthenticated{Reason: "operator digest is unreadable"}
	}
	got, err := pbkdf2.Key(sha512.New, password, salt, rec.Iterations, len(want))
	if err != nil {
		return fault.Unauthenticated{Reason: "key derivation failed"}
	}
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return fault.Unauthenticated{Reason: "password mismatch"}
	}
	return nil
}

func checkPassword(password string) error {
	if n := len([]rune(password)); n < MinPasswordRunes {
		return fault.Usage{Reason: fmt.Sprintf("password must be at least %d characters", MinPasswordRunes)}
	}
	if len(password) > MaxPasswordBytes {
		return fault.Usage{Reason: fmt.Sprintf("password must be at most %d bytes", MaxPasswordBytes)}
	}
	if strings.TrimSpace(password) == "" {
		return fault.Usage{Reason: "password is only whitespace"}
	}
	return nil
}

// --- sync tokens ---------------------------------------------------------

// Token is the stored form of a sync token. The secret is never written.
type Token struct {
	ID       string    `json:"id"`
	Label    string    `json:"label,omitempty"`
	Salt     string    `json:"salt"`
	Digest   string    `json:"digest"`
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"last_seen,omitempty"`
}

// Validate checks a stored token record is usable.
func (t Token) Validate() error {
	if !isHex(t.ID, 16) {
		return fault.Field("Token", "id", "id %q must be 16 hex characters", t.ID)
	}
	salt, err := enc.DecodeString(t.Salt)
	if err != nil || len(salt) < 16 {
		return fault.Field("Token", "salt", "salt is missing or too short")
	}
	digest, err := enc.DecodeString(t.Digest)
	if err != nil || len(digest) != sha256.Size {
		return fault.Field("Token", "digest", "digest is missing or the wrong size")
	}
	if t.Created.IsZero() {
		return fault.Field("Token", "created", "creation time is missing")
	}
	return nil
}

// NewToken mints a token, stores its digest, and returns the secret exactly
// once. The returned string is the only time it exists outside the caller.
//
// It is `<id>.<secret>`: the id is not secret and makes verification a direct
// lookup rather than a scan of every record, and the secret carries the entropy.
func (s *Store) NewToken(label string, at time.Time) (string, Token, error) {
	if at.IsZero() {
		return "", Token{}, fault.Internal{Where: "auth.NewToken", Detail: "zero timestamp"}
	}
	if len([]rune(label)) > 64 {
		return "", Token{}, fault.Usage{Reason: "token label must be at most 64 characters"}
	}

	idBytes := make([]byte, 8)
	secret := make([]byte, TokenSecretBytes)
	salt := make([]byte, SaltBytes)
	for _, b := range [][]byte{idBytes, secret, salt} {
		if _, err := rand.Read(b); err != nil {
			return "", Token{}, fault.IO{Op: "read", Subject: "system randomness", Err: err}
		}
	}

	id := hex.EncodeToString(idBytes)
	rec := Token{
		ID:      id,
		Label:   label,
		Salt:    enc.EncodeToString(salt),
		Digest:  enc.EncodeToString(digestToken(secret, salt)),
		Created: at,
	}
	if err := rec.Validate(); err != nil {
		return "", Token{}, err
	}
	if err := atomic.MkdirAll(s.path("tokens"), dirMode); err != nil {
		return "", Token{}, err
	}
	if err := atomic.WriteJSON(s.tokenPath(id), rec, fileMode); err != nil {
		return "", Token{}, err
	}
	return id + "." + enc.EncodeToString(secret), rec, nil
}

func (s *Store) tokenPath(id string) string { return s.path("tokens", id+".json") }

// VerifyToken checks a presented sync token and reports which record it was.
//
// As with the password, every failure returns the same fault: a caller learns
// only that the token was not accepted.
func (s *Store) VerifyToken(presented string) (Token, error) {
	reject := func(reason string) (Token, error) {
		return Token{}, fault.Unauthenticated{Reason: reason}
	}

	id, secretText, ok := strings.Cut(presented, ".")
	if !ok || !isHex(id, 16) {
		return reject("malformed token")
	}
	secret, err := enc.DecodeString(secretText)
	if err != nil || len(secret) != TokenSecretBytes {
		return reject("malformed token secret")
	}

	var rec Token
	if err := atomic.ReadJSON(s.tokenPath(id), &rec); err != nil {
		return reject("no such token: " + err.Error())
	}
	if err := rec.Validate(); err != nil {
		return reject("unusable token record: " + err.Error())
	}
	salt, err := enc.DecodeString(rec.Salt)
	if err != nil {
		return reject("token salt is unreadable")
	}
	want, err := enc.DecodeString(rec.Digest)
	if err != nil {
		return reject("token digest is unreadable")
	}
	if subtle.ConstantTimeCompare(digestToken(secret, salt), want) != 1 {
		return reject("token mismatch")
	}
	return rec, nil
}

// TouchToken records that a token was used. A failure to record it is not a
// reason to refuse a sync that has already authenticated.
func (s *Store) TouchToken(id string, at time.Time) error {
	var rec Token
	if err := atomic.ReadJSON(s.tokenPath(id), &rec); err != nil {
		return err
	}
	rec.LastSeen = at
	if err := rec.Validate(); err != nil {
		return err
	}
	return atomic.WriteJSON(s.tokenPath(id), rec, fileMode)
}

// Tokens lists the stored token records, without their secrets, in a stable
// order.
func (s *Store) Tokens() ([]Token, error) {
	entries, err := os.ReadDir(s.path("tokens"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fault.IO{Op: "list", Subject: s.path("tokens"), Err: err}
	}
	var out []Token
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var rec Token
		if err := atomic.ReadJSON(filepath.Join(s.path("tokens"), e.Name()), &rec); err != nil {
			return nil, err
		}
		if err := rec.Validate(); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	slices.SortFunc(out, func(a, b Token) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// HasToken reports whether any usable sync token exists.
func (s *Store) HasToken() bool {
	tokens, err := s.Tokens()
	return err == nil && len(tokens) > 0
}

// RemoveToken deletes a token record.
func (s *Store) RemoveToken(id string) error {
	if !isHex(id, 16) {
		return fault.Usage{Reason: fmt.Sprintf("token id %q must be 16 hex characters", id)}
	}
	return atomic.Remove(s.tokenPath(id))
}

func digestToken(secret, salt []byte) []byte {
	mac := hmac.New(sha256.New, salt)
	mac.Write(secret)
	return mac.Sum(nil)
}

// --- sessions ------------------------------------------------------------

// Session is a logged-in browser. The cookie value is not stored: the file is
// named for its hash, so reading the store does not hand out live sessions.
type Session struct {
	Hash    string    `json:"hash"`
	CSRF    string    `json:"csrf"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
}

// Validate checks a stored session record is usable.
func (sn Session) Validate() error {
	if !isHex(sn.Hash, sha256.Size*2) {
		return fault.Field("Session", "hash", "hash is not a SHA-256 digest")
	}
	if len(sn.CSRF) < 16 {
		return fault.Field("Session", "csrf", "csrf token is missing or too short")
	}
	if sn.Created.IsZero() || sn.Expires.IsZero() {
		return fault.Field("Session", "expires", "session carries no lifetime")
	}
	if !sn.Expires.After(sn.Created) {
		return fault.Field("Session", "expires", "session expires before it was created")
	}
	return nil
}

// NewSession opens a session and returns the cookie value exactly once.
func (s *Store) NewSession(at time.Time, lifetime time.Duration) (string, Session, error) {
	if at.IsZero() {
		return "", Session{}, fault.Internal{Where: "auth.NewSession", Detail: "zero timestamp"}
	}
	if lifetime <= 0 {
		return "", Session{}, fault.Internal{Where: "auth.NewSession", Detail: "non-positive lifetime"}
	}

	raw := make([]byte, SessionBytes)
	csrf := make([]byte, SessionBytes)
	for _, b := range [][]byte{raw, csrf} {
		if _, err := rand.Read(b); err != nil {
			return "", Session{}, fault.IO{Op: "read", Subject: "system randomness", Err: err}
		}
	}

	cookie := enc.EncodeToString(raw)
	rec := Session{
		Hash:    hashSession(cookie),
		CSRF:    enc.EncodeToString(csrf),
		Created: at,
		Expires: at.Add(lifetime),
	}
	if err := rec.Validate(); err != nil {
		return "", Session{}, err
	}
	if err := atomic.MkdirAll(s.path("sessions"), dirMode); err != nil {
		return "", Session{}, err
	}
	if err := atomic.WriteJSON(s.sessionPath(rec.Hash), rec, fileMode); err != nil {
		return "", Session{}, err
	}
	return cookie, rec, nil
}

func (s *Store) sessionPath(hash string) string { return s.path("sessions", hash+".json") }

// Session looks up a cookie value and returns its record if it is still live.
//
// An expired session is deleted as it is found, so the store does not
// accumulate them and a stale cookie cannot be revived by a clock change.
func (s *Store) Session(cookie string, now time.Time) (Session, error) {
	reject := func(reason string) (Session, error) {
		return Session{}, fault.Unauthenticated{Reason: reason}
	}
	if cookie == "" {
		return reject("no session cookie")
	}
	if raw, err := enc.DecodeString(cookie); err != nil || len(raw) != SessionBytes {
		return reject("malformed session cookie")
	}

	hash := hashSession(cookie)
	var rec Session
	if err := atomic.ReadJSON(s.sessionPath(hash), &rec); err != nil {
		return reject("no such session")
	}
	if err := rec.Validate(); err != nil {
		return reject("unusable session record: " + err.Error())
	}
	if !now.Before(rec.Expires) {
		_ = atomic.Remove(s.sessionPath(hash))
		return reject("session expired")
	}
	return rec, nil
}

// EndSession destroys a session, so logging out is a fact on the server rather
// than a discarded cookie.
func (s *Store) EndSession(cookie string) error {
	if cookie == "" {
		return nil
	}
	return atomic.Remove(s.sessionPath(hashSession(cookie)))
}

// SweepSessions deletes every expired session and reports how many went.
func (s *Store) SweepSessions(now time.Time) (int, error) {
	entries, err := os.ReadDir(s.path("sessions"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fault.IO{Op: "list", Subject: s.path("sessions"), Err: err}
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.path("sessions"), e.Name())
		var rec Session
		if err := atomic.ReadJSON(path, &rec); err != nil || rec.Validate() != nil || !now.Before(rec.Expires) {
			// A record that cannot be read is also a record that can never
			// authenticate anyone, so sweeping it is the right outcome.
			if err := atomic.Remove(path); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// CheckCSRF compares a presented CSRF token against the session's, in constant
// time.
func (sn Session) CheckCSRF(presented string) error {
	if presented == "" {
		return fault.Unauthenticated{Reason: "no csrf token"}
	}
	if subtle.ConstantTimeCompare([]byte(presented), []byte(sn.CSRF)) != 1 {
		return fault.Unauthenticated{Reason: "csrf token mismatch"}
	}
	return nil
}

func hashSession(cookie string) string {
	sum := sha256.Sum256([]byte(cookie))
	return hex.EncodeToString(sum[:])
}

// --- rate limiting -------------------------------------------------------

// Limiter slows repeated failures from one source.
//
// It is deliberately not a token bucket: the thing worth slowing is *guessing*,
// so the delay grows with consecutive failures and resets the moment one
// succeeds. A legitimate operator who mistypes once notices nothing.
//
// That last sentence is a requirement, not a description of what falls out. The
// first failure is recorded and imposes no wait; the delay starts from the second
// consecutive one. Without that, one typo followed by an immediate retry — which is
// what a phone does, where the keyboard is small and the password manager refills
// the box for you — is refused, and the refusal is the first thing a new operator
// meets. Guessing is unaffected: an attacker's second attempt is where the
// exponential starts, and it doubles from there.
type Limiter struct {
	mu       sync.Mutex
	attempts map[string]attempt

	// Base is the delay after the first failure; each further failure doubles
	// it, up to Max.
	Base time.Duration
	Max  time.Duration
}

type attempt struct {
	failures int
	next     time.Time
}

// NewLimiter returns a limiter with sensible delays.
func NewLimiter() *Limiter {
	return &Limiter{
		attempts: make(map[string]attempt),
		Base:     time.Second,
		Max:      time.Minute,
	}
}

// Allow reports whether source may try now, and if not, how long it must wait.
func (l *Limiter) Allow(source string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	a, ok := l.attempts[source]
	if !ok || now.After(a.next) || now.Equal(a.next) {
		return true, 0
	}
	return false, a.next.Sub(now)
}

// Fail records a failed attempt and lengthens the wait.
func (l *Limiter) Fail(source string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()

	a := l.attempts[source]
	a.failures++
	if a.failures < 2 {
		// Recorded, so the next failure is the second — but no wait. One mistype
		// costs nothing.
		l.attempts[source] = a
		return
	}
	delay := l.Base << min(a.failures-2, 16)
	if delay > l.Max || delay <= 0 {
		delay = l.Max
	}
	a.next = now.Add(delay)
	l.attempts[source] = a
}

// Succeed clears a source's history.
func (l *Limiter) Succeed(source string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, source)
}

// Sweep forgets sources whose wait has long passed, so the map cannot grow
// without bound under a spray of attempts from many addresses.
func (l *Limiter) Sweep(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for source, a := range l.attempts {
		if now.Sub(a.next) > l.Max {
			delete(l.attempts, source)
		}
	}
}

func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
