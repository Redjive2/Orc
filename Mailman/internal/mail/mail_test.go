package mail_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/mailman/internal/mail"
)

var testTime = time.Date(2026, 7, 24, 18, 31, 4, 512_000_000, time.UTC)

// countingEntropy yields distinct, reproducible bytes, so ids are pinned but
// never collide within a test. The counter is written little-endian, so its
// low, fast-moving bytes land first and a short read still varies.
type countingEntropy struct{ n uint64 }

func (e *countingEntropy) Read(p []byte) (int, error) {
	for i := 0; i < len(p); i += 8 {
		e.n++
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], e.n)
		copy(p[i:], buf[:])
	}
	return len(p), nil
}

type failingEntropy struct{}

func (failingEntropy) Read([]byte) (int, error) { return 0, errors.New("no entropy") }

func names(t *testing.T, raws ...string) []user.Name {
	t.Helper()
	list, err := user.ParseList(raws)
	if err != nil {
		t.Fatalf("ParseList(%v): %v", raws, err)
	}
	return list
}

// build makes a valid message, so each test can vary one thing about it.
func build(t *testing.T, opts ...func(*spec)) mail.Message {
	t.Helper()
	s := spec{
		kind:    mail.Ordinary,
		from:    "boss",
		to:      []string{"alice", "bob"},
		subject: "RE: work",
		sent:    testTime,
		body:    []byte("Ship it.\n"),
	}
	for _, o := range opts {
		o(&s)
	}

	id, err := mail.NewID(s.sent, &countingEntropy{})
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	from, err := user.Parse(s.from)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s.from, err)
	}

	var convo mail.ID
	if s.convo {
		convo = id
	}
	m, err := mail.New(id, s.kind, from, names(t, s.to...), names(t, s.cc...),
		s.subject, convo, s.index, s.sent, s.body)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

type spec struct {
	kind    mail.Kind
	from    string
	to      []string
	cc      []string
	subject string
	sent    time.Time
	body    []byte
	convo   bool
	index   int
}

func withCC(cc ...string) func(*spec)  { return func(s *spec) { s.cc = cc } }
func withBody(b string) func(*spec)    { return func(s *spec) { s.body = []byte(b) } }
func withSubject(v string) func(*spec) { return func(s *spec) { s.subject = v } }
func withKind(k mail.Kind) func(*spec) { return func(s *spec) { s.kind = k } }
func inConvo(index int) func(*spec)    { return func(s *spec) { s.convo, s.index = true, index } }
func withTo(to ...string) func(*spec)  { return func(s *spec) { s.to = to } }

func TestIDEncodesItsSendTime(t *testing.T) {
	id, err := mail.NewID(testTime, &countingEntropy{})
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	back, err := id.Sent()
	if err != nil {
		t.Fatalf("Sent: %v", err)
	}
	if want := clock.Normalise(testTime); !back.Equal(want) {
		t.Errorf("id %s decodes to %s, want %s", id, back, want)
	}
	if len(id.String()) != 25 {
		t.Errorf("id %q is %d characters, want 25", id, len(id.String()))
	}
	if got := id.Short(); len(got) != 8 {
		t.Errorf("Short() = %q, want 8 characters", got)
	}
}

// TestIDsSortByTime is what lets a directory listing answer "most recent"
// without an index.
func TestIDsSortByTime(t *testing.T) {
	entropy := &countingEntropy{}
	var ids []string
	for i := range 20 {
		at := testTime.Add(time.Duration(i) * time.Millisecond)
		id, err := mail.NewID(at, entropy)
		if err != nil {
			t.Fatalf("NewID: %v", err)
		}
		ids = append(ids, id.String())
	}
	for i := 1; i < len(ids); i++ {
		if ids[i-1] >= ids[i] {
			t.Fatalf("ids are not in lexical send order: %q then %q", ids[i-1], ids[i])
		}
	}
}

// TestIDsAreUniqueWithinAMicrosecond is the collision case the random suffix
// exists for: two agents sending at the same instant with no coordination.
func TestIDsAreUniqueWithinAMicrosecond(t *testing.T) {
	entropy := &countingEntropy{}
	seen := make(map[string]bool, 256)
	for range 256 {
		id, err := mail.NewID(testTime, entropy)
		if err != nil {
			t.Fatalf("NewID: %v", err)
		}
		if seen[id.String()] {
			t.Fatalf("id %s was minted twice at the same instant", id)
		}
		seen[id.String()] = true
	}
}

func TestNewIDRejectsBadInput(t *testing.T) {
	if _, err := mail.NewID(testTime, failingEntropy{}); !errors.Is(err, fault.ErrIO) {
		t.Errorf("NewID without entropy = %v, want an i/o fault", err)
	}
	for _, at := range []time.Time{
		time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC),
		{},
	} {
		if _, err := mail.NewID(at, &countingEntropy{}); !errors.Is(err, fault.ErrInternal) {
			t.Errorf("NewID(%s) = %v, want an internal fault", at, err)
		}
	}
}

func TestParseID(t *testing.T) {
	good, err := mail.NewID(testTime, &countingEntropy{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mail.ParseID(good.String()); err != nil {
		t.Errorf("ParseID of a minted id: %v", err)
	}

	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"too short", "0006"},
		{"too long", good.String() + "0"},
		{"no dash", strings.Replace(good.String(), "-", "0", 1)},
		{"dash in the wrong place", "0006-2b3c4d5e6f708190a1b2"},
		{"uppercase hex", strings.ToUpper(good.String())},
		{"non-hex", strings.Replace(good.String(), good.String()[0:1], "z", 1)},
		{"spaces", strings.Repeat(" ", 25)},
		{"path traversal", "../../../../../etc/passwd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := mail.ParseID(tc.raw); !errors.Is(err, fault.ErrParse) {
				t.Errorf("ParseID(%q) = %v, want a parse fault", tc.raw, err)
			}
		})
	}
}

func TestKind(t *testing.T) {
	for _, tc := range []struct {
		kind mail.Kind
		text string
	}{
		{mail.Ordinary, "mail"},
		{mail.Notice, "cc"},
	} {
		if got := tc.kind.String(); got != tc.text {
			t.Errorf("%v.String() = %q, want %q", tc.kind, got, tc.text)
		}
		back, err := mail.ParseKind(tc.text)
		if err != nil || back != tc.kind {
			t.Errorf("ParseKind(%q) = %v, %v", tc.text, back, err)
		}
		if !tc.kind.Valid() {
			t.Errorf("%v should be valid", tc.kind)
		}
	}
	if _, err := mail.ParseKind("urgent"); !errors.Is(err, fault.ErrParse) {
		t.Errorf("ParseKind(\"urgent\") = %v, want a parse fault", err)
	}
	if mail.Kind(99).Valid() {
		t.Error("Kind(99) should not be valid")
	}
}

func TestAccessorsReturnCopies(t *testing.T) {
	m := build(t, withCC("carol"))

	to := m.To()
	to[0] = user.Name{}
	if m.To()[0].Zero() {
		t.Error("To() handed out the internal slice")
	}
	cc := m.CC()
	cc[0] = user.Name{}
	if m.CC()[0].Zero() {
		t.Error("CC() handed out the internal slice")
	}
	body := m.Body()
	if len(body) > 0 {
		body[0] = 'X'
	}
	if m.Body()[0] == 'X' {
		t.Error("Body() handed out the internal slice")
	}
}

func TestRecipientsAndParticipants(t *testing.T) {
	m := build(t, withCC("carol"))

	if got := user.Names(m.Recipients()); strings.Join(got, ",") != "alice,bob,carol" {
		t.Errorf("Recipients() = %v", got)
	}
	// Participants adds the sender, at the front, without duplicating anyone.
	if got := user.Names(m.Participants()); strings.Join(got, ",") != "boss,alice,bob,carol" {
		t.Errorf("Participants() = %v", got)
	}

	// A sender who is also a recipient must appear exactly once.
	self := build(t, withTo("boss", "alice"))
	if got := user.Names(self.Participants()); strings.Join(got, ",") != "boss,alice" {
		t.Errorf("Participants() with a self-addressed message = %v", got)
	}
}

func TestNewRejectsInvalidMessages(t *testing.T) {
	id, err := mail.NewID(testTime, &countingEntropy{})
	if err != nil {
		t.Fatal(err)
	}
	boss, _ := user.MustParse("boss")
	to := names(t, "alice")

	for _, tc := range []struct {
		name string
		call func() (mail.Message, error)
	}{
		{"no recipients", func() (mail.Message, error) {
			return mail.New(id, mail.Ordinary, boss, nil, nil, "s", mail.ID{}, 0, testTime, nil)
		}},
		{"zero sender", func() (mail.Message, error) {
			return mail.New(id, mail.Ordinary, user.Name{}, to, nil, "s", mail.ID{}, 0, testTime, nil)
		}},
		{"zero id", func() (mail.Message, error) {
			return mail.New(mail.ID{}, mail.Ordinary, boss, to, nil, "s", mail.ID{}, 0, testTime, nil)
		}},
		{"undefined kind", func() (mail.Message, error) {
			return mail.New(id, mail.Kind(9), boss, to, nil, "s", mail.ID{}, 0, testTime, nil)
		}},
		{"empty subject", func() (mail.Message, error) {
			return mail.New(id, mail.Ordinary, boss, to, nil, "", mail.ID{}, 0, testTime, nil)
		}},
		{"index without convo", func() (mail.Message, error) {
			return mail.New(id, mail.Ordinary, boss, to, nil, "s", mail.ID{}, 3, testTime, nil)
		}},
		{"convo without index", func() (mail.Message, error) {
			return mail.New(id, mail.Ordinary, boss, to, nil, "s", id, 0, testTime, nil)
		}},
		{"recipient in both to and cc", func() (mail.Message, error) {
			return mail.New(id, mail.Ordinary, boss, to, to, "s", mail.ID{}, 0, testTime, nil)
		}},
		{"sent time disagrees with id", func() (mail.Message, error) {
			return mail.New(id, mail.Ordinary, boss, to, nil, "s", mail.ID{}, 0, testTime.Add(time.Hour), nil)
		}},
		{"body with a NUL", func() (mail.Message, error) {
			return mail.New(id, mail.Ordinary, boss, to, nil, "s", mail.ID{}, 0, testTime, []byte("a\x00b"))
		}},
		{"subject with a newline", func() (mail.Message, error) {
			return mail.New(id, mail.Ordinary, boss, to, nil, "a\nb", mail.ID{}, 0, testTime, nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := tc.call(); err == nil {
				t.Errorf("New succeeded and produced %s, want a failure", got.ID())
			}
		})
	}
}

func TestCheckSubject(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    string
		ok   bool
	}{
		{"plain", "RE: work", true},
		{"unicode", "日本語の件", true},
		{"emoji", "🚀 ship", true},
		{"longest", strings.Repeat("a", 256), true},
		{"empty", "", false},
		{"whitespace only", "   ", false},
		{"too long", strings.Repeat("a", 257), false},
		{"newline", "a\nb", false},
		{"carriage return", "a\rb", false},
		{"tab", "a\tb", false},
		{"escape", "a\x1bb", false},
		{"nul", "a\x00b", false},
		{"invalid utf8", "a\xffb", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := mail.CheckSubject(tc.s)
			if tc.ok && err != nil {
				t.Errorf("CheckSubject(%q) = %v, want nil", tc.s, err)
			}
			if !tc.ok && !errors.Is(err, fault.ErrUsage) {
				t.Errorf("CheckSubject(%q) = %v, want a usage fault", tc.s, err)
			}
		})
	}
}

func TestCheckBody(t *testing.T) {
	if err := mail.CheckBody(nil); err != nil {
		t.Errorf("an empty body should be allowed, got %v", err)
	}
	if err := mail.CheckBody(bytes.Repeat([]byte("a"), mail.MaxBody+1)); !errors.Is(err, fault.ErrUsage) {
		t.Errorf("an oversized body = %v, want a usage fault", err)
	}
	if err := mail.CheckBody([]byte("a\x00b")); !errors.Is(err, fault.ErrUsage) {
		t.Errorf("a body with a NUL = %v, want a usage fault", err)
	}
	if err := mail.CheckBody([]byte{0xff, 0xfe}); !errors.Is(err, fault.ErrUsage) {
		t.Errorf("an invalid-UTF-8 body = %v, want a usage fault", err)
	}
}

func TestInConvoRebinds(t *testing.T) {
	m := build(t)
	root, err := mail.NewID(testTime.Add(-time.Hour), &countingEntropy{})
	if err != nil {
		t.Fatal(err)
	}

	moved, err := m.InConvo(root, 2)
	if err != nil {
		t.Fatalf("InConvo: %v", err)
	}
	got, ok := moved.Convo()
	if !ok || got.String() != root.String() {
		t.Errorf("Convo() = %s, %v", got, ok)
	}
	if moved.Index() != 2 {
		t.Errorf("Index() = %d, want 2", moved.Index())
	}

	// The original is untouched.
	if _, ok := m.Convo(); ok {
		t.Error("InConvo mutated the message it was called on")
	}

	// A message already in a conversation is never moved.
	if _, err := moved.InConvo(root, 3); !errors.Is(err, fault.ErrConflict) {
		t.Errorf("re-binding a threaded message = %v, want a conflict", err)
	}
	// An invalid index is refused rather than stored.
	if _, err := m.InConvo(root, 0); err == nil {
		t.Error("InConvo with index 0 should fail")
	}
}
