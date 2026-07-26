package store_test

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/mailman/internal/mail"
	"orc/mailman/internal/store"
)

var epoch = time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

type entropy struct {
	mu sync.Mutex
	n  uint64
}

func (e *entropy) Read(p []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := 0; i < len(p); i += 8 {
		e.n++
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], e.n)
		copy(p[i:], buf[:])
	}
	return len(p), nil
}

// harness is an opened store with a deterministic clock and a few users.
type harness struct {
	*store.Store
	t       *testing.T
	clock   *clock.Fake
	entropy *entropy
	root    string
	keys    map[string]string
}

func newHarness(t *testing.T, users ...string) *harness {
	t.Helper()
	root := t.TempDir()
	c := clock.NewFake(epoch, time.Millisecond)

	s, err := store.Open(root, c)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	h := &harness{Store: s, t: t, clock: c, entropy: &entropy{}, root: root, keys: map[string]string{}}
	for _, name := range users {
		h.addUser(name)
	}
	return h
}

func (h *harness) addUser(name string) string {
	h.t.Helper()
	n := h.name(name)
	key, err := user.NewKey(rand.Reader)
	if err != nil {
		h.t.Fatalf("NewKey: %v", err)
	}
	if err := h.CreateUser(n, key); err != nil {
		h.t.Fatalf("CreateUser(%s): %v", name, err)
	}
	h.keys[name] = key
	return key
}

func (h *harness) name(s string) user.Name {
	h.t.Helper()
	n, err := user.Parse(s)
	if err != nil {
		h.t.Fatalf("Parse(%q): %v", s, err)
	}
	return n
}

// message builds and stores a message, returning it.
func (h *harness) message(from string, to []string, subject, body string) mail.Message {
	h.t.Helper()
	at := h.clock.Now()

	id, err := mail.NewID(at, h.entropy)
	if err != nil {
		h.t.Fatalf("NewID: %v", err)
	}
	recipients, err := user.ParseList(to)
	if err != nil {
		h.t.Fatalf("ParseList: %v", err)
	}
	m, err := mail.New(id, mail.Ordinary, h.name(from), recipients, nil, subject, mail.ID{}, 0, at, []byte(body))
	if err != nil {
		h.t.Fatalf("New: %v", err)
	}
	if err := h.Put(m); err != nil {
		h.t.Fatalf("Put: %v", err)
	}
	return m
}

func TestOpenCreatesTheLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nested", "store")
	if _, err := store.Open(root, clock.NewFake(epoch, time.Millisecond)); err != nil {
		t.Fatalf("Open: %v", err)
	}

	for _, dir := range []string{"users", "messages", "convos"} {
		info, err := os.Stat(filepath.Join(root, dir))
		if err != nil {
			t.Errorf("Open did not create %s: %v", dir, err)
			continue
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("%s is mode %04o, want 0700", dir, perm)
		}
	}

	data, err := os.ReadFile(filepath.Join(root, "version"))
	if err != nil {
		t.Fatalf("no version marker: %v", err)
	}
	if strings.TrimSpace(string(data)) != "1" {
		t.Errorf("version = %q", data)
	}
}

// TestOpenIsIdempotent: an agent's first command creates the store, and its
// second must not trip over what the first made.
func TestOpenIsIdempotent(t *testing.T) {
	root := t.TempDir()
	for range 3 {
		if _, err := store.Open(root, clock.NewFake(epoch, time.Millisecond)); err != nil {
			t.Fatalf("Open: %v", err)
		}
	}
}

func TestOpenRefusesAnUnknownVersion(t *testing.T) {
	for _, version := range []string{"2", "99", "nonsense", ""} {
		t.Run("version "+version, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "version"), []byte(version+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := store.Open(root, clock.NewFake(epoch, time.Millisecond))
			if !errors.Is(err, fault.ErrParse) {
				t.Fatalf("Open on version %q = %v, want a parse fault", version, err)
			}
		})
	}
}

func TestOpenRejectsBadArguments(t *testing.T) {
	if _, err := store.Open("", clock.Real{}); !errors.Is(err, fault.ErrUsage) {
		t.Errorf("Open(\"\") = %v, want a usage fault", err)
	}
	if _, err := store.Open(t.TempDir(), nil); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Open without a clock = %v, want an internal fault", err)
	}
}

func TestDefaultRoot(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		home string
		want string
	}{
		{"explicit", map[string]string{"MAILMAN_HOME": "/srv/mail"}, "/home/a", "/srv/mail"},
		{"xdg", map[string]string{"XDG_DATA_HOME": "/home/a/.local/share"}, "/home/a", "/home/a/.local/share/mailman"},
		{"home", nil, "/home/a", "/home/a/.mailman"},
		{"explicit wins over xdg", map[string]string{
			"MAILMAN_HOME": "/srv/mail", "XDG_DATA_HOME": "/x",
		}, "/home/a", "/srv/mail"},
		{"empty xdg falls through", map[string]string{"XDG_DATA_HOME": "  "}, "/home/a", "/home/a/.mailman"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.DefaultRoot(store.MapEnv(tc.env), tc.home)
			if err != nil {
				t.Fatalf("DefaultRoot: %v", err)
			}
			if got != filepath.Clean(tc.want) {
				t.Errorf("DefaultRoot = %q, want %q", got, tc.want)
			}
		})
	}

	if _, err := store.DefaultRoot(store.MapEnv(map[string]string{"MAILMAN_HOME": " "}), "/home/a"); !errors.Is(err, fault.ErrUsage) {
		t.Error("an empty MAILMAN_HOME should be a usage fault")
	}
	if _, err := store.DefaultRoot(store.MapEnv(nil), ""); !errors.Is(err, fault.ErrUsage) {
		t.Error("no home and no override should be a usage fault")
	}
}

func TestAuthenticate(t *testing.T) {
	h := newHarness(t, "alice", "bob")

	if err := h.Authenticate(h.name("alice"), h.keys["alice"]); err != nil {
		t.Errorf("the right key was refused: %v", err)
	}

	for _, tc := range []struct {
		name string
		user string
		key  string
	}{
		{"wrong key", "alice", h.keys["bob"]},
		{"empty key", "alice", ""},
		{"no such user", "dave", h.keys["alice"]},
		{"right key wrong user", "bob", h.keys["alice"]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := h.Authenticate(h.name(tc.user), tc.key)
			if !errors.Is(err, fault.ErrAuth) {
				t.Fatalf("Authenticate = %v, want an auth fault", err)
			}
			// Every failure must look the same from outside.
			if got := err.Error(); got != "authentication failed" {
				t.Errorf("message = %q, want the generic text", got)
			}
		})
	}

	if err := h.Authenticate(user.Name{}, "x"); !errors.Is(err, fault.ErrAuth) {
		t.Errorf("a zero user = %v, want an auth fault", err)
	}
}

// TestAuthenticateFailsClosedOnADamagedRecord is the property that matters
// most in this package: no corruption may become "no key required".
func TestAuthenticateFailsClosedOnADamagedRecord(t *testing.T) {
	h := newHarness(t, "alice")
	path := filepath.Join(h.Root(), "users", "alice", "user.json")

	for _, tc := range []struct {
		name    string
		content string
	}{
		{"empty", ""},
		{"empty object", "{}"},
		{"truncated", `{"version":1,"name":"alice"`},
		{"not json", "hello"},
		{"nul bytes", "\x00\x00\x00"},
		{"renamed to bob", `{"version":1,"name":"bob","algo":"hmac-sha256","salt":"","digest":"","created":"2026-01-01T00:00:00.000Z"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{h.keys["alice"], "", strings.Repeat("k", 32)} {
				if err := h.Authenticate(h.name("alice"), key); !errors.Is(err, fault.ErrAuth) {
					t.Fatalf("a %s record authenticated with %q: %v", tc.name, key, err)
				}
			}
		})
	}

	// Removing the record entirely must also fail closed.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := h.Authenticate(h.name("alice"), h.keys["alice"]); !errors.Is(err, fault.ErrAuth) {
		t.Errorf("a missing record authenticated: %v", err)
	}
}

func TestUsers(t *testing.T) {
	h := newHarness(t, "carol", "alice", "bob")

	got, err := h.Users()
	if err != nil {
		t.Fatalf("Users: %v", err)
	}
	if want := "alice,bob,carol"; strings.Join(user.Names(got), ",") != want {
		t.Errorf("Users = %v, want %s in order", user.Names(got), want)
	}

	// A stray directory that is not a user is skipped rather than breaking the
	// listing.
	if err := os.MkdirAll(filepath.Join(h.Root(), "users", "not a user"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h.Root(), "users", "dave"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err = h.Users()
	if err != nil {
		t.Fatalf("Users: %v", err)
	}
	if want := "alice,bob,carol"; strings.Join(user.Names(got), ",") != want {
		t.Errorf("Users = %v; a directory without a record is not a user", user.Names(got))
	}
}

func TestCreateAndDeleteUser(t *testing.T) {
	h := newHarness(t)
	alice := h.name("alice")
	key, err := user.NewKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	if ok, _ := h.HasUser(alice); ok {
		t.Fatal("the user should not exist yet")
	}
	if err := h.CreateUser(alice, key); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if ok, _ := h.HasUser(alice); !ok {
		t.Fatal("the user should exist now")
	}

	// Creating again must not silently revoke the existing key.
	if err := h.CreateUser(alice, key); !errors.Is(err, fault.ErrConflict) {
		t.Errorf("re-creating a user = %v, want a conflict", err)
	}
	if err := h.Authenticate(alice, key); err != nil {
		t.Errorf("the original key stopped working: %v", err)
	}

	if err := h.CreateUser(alice, "short"); !errors.Is(err, fault.ErrUsage) {
		t.Errorf("CreateUser with a short key = %v, want a usage fault", err)
	}

	if err := h.DeleteUser(alice); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if ok, _ := h.HasUser(alice); ok {
		t.Error("the user should be gone")
	}
	if err := h.DeleteUser(alice); !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("deleting a missing user = %v, want not found", err)
	}
}

func TestPutAndGet(t *testing.T) {
	h := newHarness(t, "boss", "alice")
	m := h.message("boss", []string{"alice"}, "RE: work", "Ship it.\n")

	back, err := h.Get(m.ID())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if back.BodyString() != "Ship it.\n" || back.Subject() != "RE: work" {
		t.Errorf("the message did not survive: %q / %q", back.Subject(), back.BodyString())
	}

	// Write-once.
	if err := h.Put(m); !errors.Is(err, fault.ErrConflict) {
		t.Errorf("storing a message twice = %v, want a conflict", err)
	}

	missing, err := mail.NewID(epoch, h.entropy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Get(missing); !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("Get on a missing message = %v, want not found", err)
	}
	if _, err := h.Get(mail.ID{}); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Get on a zero id = %v, want an internal fault", err)
	}
}

// TestGetRefusesAMisnamedFile catches a message file that was copied rather
// than sent, which would otherwise answer for an id it is not.
func TestGetRefusesAMisnamedFile(t *testing.T) {
	h := newHarness(t, "boss", "alice")
	first := h.message("boss", []string{"alice"}, "one", "a")
	second := h.message("boss", []string{"alice"}, "two", "b")

	src := filepath.Join(h.Root(), "messages", first.ID().String()[:2], first.ID().String()+".msg")
	dst := filepath.Join(h.Root(), "messages", second.ID().String()[:2], second.ID().String()+".msg")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := h.Get(second.ID()); !errors.Is(err, fault.ErrConflict) {
		t.Errorf("Get on a misnamed file = %v, want a conflict", err)
	}
}

func TestDeliverAssignsPUIDs(t *testing.T) {
	h := newHarness(t, "boss", "alice")
	alice := h.name("alice")

	var ids []mail.ID
	for i := range 5 {
		m := h.message("boss", []string{"alice"}, fmt.Sprintf("subject %d", i), "body")
		puid, err := h.Deliver(alice, m.ID())
		if err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		if puid != i {
			t.Errorf("message %d was given puid %d, want %d", i, puid, i)
		}
		ids = append(ids, m.ID())
	}

	// Delivery is idempotent: a retried send must not duplicate mail.
	again, err := h.Deliver(alice, ids[2])
	if err != nil {
		t.Fatalf("re-Deliver: %v", err)
	}
	if again != 2 {
		t.Errorf("re-delivering gave puid %d, want the original 2", again)
	}

	st, err := h.Replay(alice)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if st.Len() != 5 {
		t.Errorf("mailbox holds %d messages, want 5", st.Len())
	}
	if st.NextPUID() != 5 {
		t.Errorf("NextPUID = %d, want 5", st.NextPUID())
	}
}

func TestMarkTransitions(t *testing.T) {
	h := newHarness(t, "boss", "alice")
	alice := h.name("alice")
	m := h.message("boss", []string{"alice"}, "s", "b")
	if _, err := h.Deliver(alice, m.ID()); err != nil {
		t.Fatal(err)
	}

	entry := func() store.Entry {
		t.Helper()
		st, err := h.Replay(alice)
		if err != nil {
			t.Fatalf("Replay: %v", err)
		}
		e, ok := st.Lookup(m.ID())
		if !ok {
			t.Fatal("the message vanished from the mailbox")
		}
		return e
	}

	if !entry().Unread() {
		t.Error("a freshly delivered message should be unread")
	}

	if err := h.Mark(alice, m.ID(), store.OpRead); err != nil {
		t.Fatalf("Mark read: %v", err)
	}
	first := entry().ReadAt
	if first.IsZero() {
		t.Fatal("the message should be read now")
	}

	// Marking read again is ordinary, not an error, and must not move the time.
	if err := h.Mark(alice, m.ID(), store.OpRead); err != nil {
		t.Fatalf("Mark read twice: %v", err)
	}
	if !entry().ReadAt.Equal(first) {
		t.Error("a second read moved the timestamp")
	}

	// Pruning before archiving is refused.
	if err := h.Mark(alice, m.ID(), store.OpPrune); !errors.Is(err, fault.ErrConflict) {
		t.Errorf("pruning unarchived mail = %v, want a conflict", err)
	}

	if err := h.Mark(alice, m.ID(), store.OpArchive); err != nil {
		t.Fatalf("Mark archive: %v", err)
	}
	if !entry().Archived {
		t.Error("the message should be archived")
	}
	if err := h.Mark(alice, m.ID(), store.OpPrune); err != nil {
		t.Fatalf("Mark prune: %v", err)
	}

	st, err := h.Replay(alice)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Lookup(m.ID()); ok {
		t.Error("a pruned message should not be looked up")
	}
	// But the mailbox still remembers it, so its puid is never reused.
	if !st.Has(m.ID()) {
		t.Error("a pruned message should still be remembered")
	}
	if st.NextPUID() != 1 {
		t.Errorf("NextPUID = %d; a pruned puid must not be reused", st.NextPUID())
	}
}

func TestMarkRejectsBadInput(t *testing.T) {
	h := newHarness(t, "boss", "alice")
	alice := h.name("alice")
	m := h.message("boss", []string{"alice"}, "s", "b")

	if err := h.Mark(alice, m.ID(), store.OpRead); !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("marking undelivered mail = %v, want not found", err)
	}
	if err := h.Mark(alice, m.ID(), store.OpDeliver); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Mark with OpDeliver = %v, want an internal fault", err)
	}
	if err := h.Mark(alice, m.ID(), store.Op("shred")); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Mark with an unknown op = %v, want an internal fault", err)
	}
}

// TestReplayRecoversFromATruncatedTail is the crash-consistency property. Any
// prefix of a journal must replay, because that is exactly what a process
// killed mid-append leaves behind.
func TestReplayRecoversFromATruncatedTail(t *testing.T) {
	h := newHarness(t, "boss", "alice")
	alice := h.name("alice")

	for i := range 4 {
		m := h.message("boss", []string{"alice"}, fmt.Sprintf("s%d", i), "b")
		if _, err := h.Deliver(alice, m.ID()); err != nil {
			t.Fatal(err)
		}
		if i%2 == 0 {
			if err := h.Mark(alice, m.ID(), store.OpRead); err != nil {
				t.Fatal(err)
			}
		}
	}

	path := h.JournalPathFor("alice")
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Every prefix must replay without error.
	for cut := range len(full) + 1 {
		if err := os.WriteFile(path, full[:cut], 0o600); err != nil {
			t.Fatal(err)
		}
		st, err := h.Replay(alice)
		if err != nil {
			t.Fatalf("a %d byte prefix of the journal failed to replay: %v", cut, err)
		}
		// A truncated prefix can only ever lose entries, never invent them.
		if st.Len() > 4 {
			t.Fatalf("a %d byte prefix produced %d entries", cut, st.Len())
		}
	}

	// And the untruncated file must still be intact.
	if err := os.WriteFile(path, full, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := h.Replay(alice)
	if err != nil {
		t.Fatal(err)
	}
	if st.Len() != 4 || st.Skipped() != 0 {
		t.Errorf("the whole journal replayed to %d entries with %d skipped", st.Len(), st.Skipped())
	}
}

// TestReplayRefusesInteriorCorruption is the other half of the rule: only the
// tail may be damaged. A bad line in the middle is corruption, and skipping it
// would silently drop mail.
func TestReplayRefusesInteriorCorruption(t *testing.T) {
	h := newHarness(t, "boss", "alice")
	alice := h.name("alice")
	for i := range 3 {
		m := h.message("boss", []string{"alice"}, fmt.Sprintf("s%d", i), "b")
		if _, err := h.Deliver(alice, m.ID()); err != nil {
			t.Fatal(err)
		}
	}

	path := h.JournalPathFor("alice")
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(full), "\n"), "\n")

	for _, tc := range []struct {
		name    string
		replace string
	}{
		{"garbage", "not json at all"},
		{"empty", ""},
		{"unknown op", `{"op":"shred","mid":"` + strings.Repeat("0", 16) + `-00000000","at":"2026-07-24T12:00:00.000Z"}`},
		{"unknown field", `{"op":"read","mid":"x","at":"y","secret":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			damaged := append([]string{}, lines...)
			damaged[1] = tc.replace
			if err := os.WriteFile(path, []byte(strings.Join(damaged, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := h.Replay(alice); !errors.Is(err, fault.ErrParse) {
				t.Errorf("interior corruption replayed with %v, want a parse fault", err)
			}
		})
	}
}

func TestReplayRefusesInconsistentHistories(t *testing.T) {
	h := newHarness(t, "alice")
	alice := h.name("alice")
	path := h.JournalPathFor("alice")

	id := strings.Repeat("0", 16) + "-00000001"
	other := strings.Repeat("0", 16) + "-00000002"
	at := `"at":"2026-07-24T12:00:00.000Z"`

	for _, tc := range []struct {
		name  string
		lines []string
	}{
		{"read before delivery", []string{`{"op":"read","mid":"` + id + `",` + at + `}`}},
		{"archived before delivery", []string{`{"op":"archive","mid":"` + id + `",` + at + `}`}},
		{"delivered twice", []string{
			`{"op":"deliver","mid":"` + id + `","puid":0,` + at + `}`,
			`{"op":"deliver","mid":"` + id + `","puid":1,` + at + `}`,
		}},
		{"pruned without archiving", []string{
			`{"op":"deliver","mid":"` + id + `","puid":0,` + at + `}`,
			`{"op":"prune","mid":"` + id + `",` + at + `}`,
		}},
		{"duplicate puid", []string{
			`{"op":"deliver","mid":"` + id + `","puid":0,` + at + `}`,
			`{"op":"deliver","mid":"` + other + `","puid":0,` + at + `}`,
		}},
		{"puid on a read event", []string{
			`{"op":"deliver","mid":"` + id + `","puid":0,` + at + `}`,
			`{"op":"read","mid":"` + id + `","puid":3,` + at + `}`,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(strings.Join(tc.lines, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := h.Replay(alice); err == nil {
				t.Error("an inconsistent journal replayed without complaint")
			}
		})
	}
}

func TestReceipts(t *testing.T) {
	h := newHarness(t, "boss", "alice", "bob")
	m := h.message("boss", []string{"alice", "bob"}, "s", "b")

	got, err := h.Receipts(m.ID())
	if err != nil {
		t.Fatalf("Receipts: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a fresh message has %d receipts, want none", len(got))
	}

	at := h.clock.Now()
	if err := h.PutReceipt(m.ID(), h.name("bob"), at); err != nil {
		t.Fatalf("PutReceipt: %v", err)
	}
	if err := h.PutReceipt(m.ID(), h.name("alice"), at.Add(time.Hour)); err != nil {
		t.Fatalf("PutReceipt: %v", err)
	}

	got, err = h.Receipts(m.ID())
	if err != nil {
		t.Fatalf("Receipts: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d receipts, want 2", len(got))
	}
	// Sorted by name, so a rendered table is stable.
	if got[0].User.String() != "alice" || got[1].User.String() != "bob" {
		t.Errorf("receipts are not in name order: %v, %v", got[0].User, got[1].User)
	}

	// The first receipt wins: a second read does not move the timestamp.
	if err := h.PutReceipt(m.ID(), h.name("bob"), at.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err = h.Receipts(m.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !got[1].At.Equal(clock.Normalise(at)) {
		t.Errorf("bob's receipt moved to %s, want %s", got[1].At, at)
	}
}

func TestReceiptsRefuseDamagedFiles(t *testing.T) {
	h := newHarness(t, "boss", "alice")
	m := h.message("boss", []string{"alice"}, "s", "b")
	if err := h.PutReceipt(m.ID(), h.name("alice"), h.clock.Now()); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(h.Root(), "messages", m.ID().String()[:2], m.ID().String()+".rcpt", "alice.json")
	for _, content := range []string{
		"", "{}", "not json",
		`{"version":1,"user":"bob","at":"2026-07-24T12:00:00.000Z"}`,
		`{"version":99,"user":"alice","at":"2026-07-24T12:00:00.000Z"}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		// A receipt that cannot be read is reported, never skipped: `check`
		// exists to say who has seen a message, and an answer that quietly
		// omits someone is worse than an error.
		if _, err := h.Receipts(m.ID()); err == nil {
			t.Errorf("a receipt containing %q was accepted", content)
		}
	}
}

func TestConversations(t *testing.T) {
	h := newHarness(t, "boss", "alice")
	root := h.message("boss", []string{"alice"}, "work", "start")

	c, err := h.OpenConvo(root, "work")
	if err != nil {
		t.Fatalf("OpenConvo: %v", err)
	}
	if c.Title() != "work" || c.Len() != 1 {
		t.Errorf("a new conversation is %q with %d messages", c.Title(), c.Len())
	}
	if c.ID().String() != root.ID().String() {
		t.Errorf("conversation id %s, want the root's %s", c.ID(), root.ID())
	}
	if c.IndexOf(root.ID()) != 1 {
		t.Errorf("the root is at index %d, want 1", c.IndexOf(root.ID()))
	}

	// Rooting twice is idempotent, so two agents replying at once both succeed.
	again, err := h.OpenConvo(root, "different title")
	if err != nil {
		t.Fatalf("re-OpenConvo: %v", err)
	}
	if again.Title() != "work" {
		t.Errorf("re-rooting changed the title to %q", again.Title())
	}

	reply := h.message("alice", []string{"boss"}, "RE: work", "ok")
	index, err := h.AddToConvo(root.ID(), reply.ID())
	if err != nil {
		t.Fatalf("AddToConvo: %v", err)
	}
	if index != 2 {
		t.Errorf("the reply is at index %d, want 2", index)
	}

	// Adding the same message again reports its existing position.
	if index, err = h.AddToConvo(root.ID(), reply.ID()); err != nil || index != 2 {
		t.Errorf("re-adding gave %d, %v; want 2, nil", index, err)
	}

	loaded, err := h.Convo(root.ID())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Len() != 2 {
		t.Errorf("the conversation holds %d messages, want 2", loaded.Len())
	}

	missing, err := mail.NewID(epoch, h.entropy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Convo(missing); !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("Convo on a missing thread = %v, want not found", err)
	}
	if _, err := h.AddToConvo(missing, reply.ID()); !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("AddToConvo on a missing thread = %v, want not found", err)
	}
}

func TestConvoRecoversFromATruncatedTail(t *testing.T) {
	h := newHarness(t, "boss", "alice")
	root := h.message("boss", []string{"alice"}, "work", "start")
	if _, err := h.OpenConvo(root, "work"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		reply := h.message("alice", []string{"boss"}, "RE: work", "ok")
		if _, err := h.AddToConvo(root.ID(), reply.ID()); err != nil {
			t.Fatal(err)
		}
	}

	path := filepath.Join(h.Root(), "convos", root.ID().String()+".jsonl")
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	for cut := range len(full) + 1 {
		if err := os.WriteFile(path, full[:cut], 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := h.Convo(root.ID())
		if err != nil {
			// A prefix too short to hold the open event is legitimately not a
			// conversation yet; anything longer must load.
			if cut > 200 {
				t.Fatalf("a %d byte prefix failed to load: %v", cut, err)
			}
			continue
		}
		if c.Len() > 4 {
			t.Fatalf("a %d byte prefix produced %d messages", cut, c.Len())
		}
	}
}

func TestReplaceOnlyAddsAThread(t *testing.T) {
	h := newHarness(t, "boss", "alice")
	m := h.message("boss", []string{"alice"}, "work", "start")

	threaded, err := m.InConvo(m.ID(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Replace(threaded); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	back, err := h.Get(m.ID())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := back.Convo(); !ok {
		t.Error("the stored message did not gain its conversation")
	}

	// Replacing again is refused: the message is already threaded.
	if err := h.Replace(threaded); !errors.Is(err, fault.ErrConflict) {
		t.Errorf("re-threading = %v, want a conflict", err)
	}

	// A replacement that changes anything else is refused outright, so this
	// cannot become a general edit path.
	other := h.message("boss", []string{"alice"}, "other", "different")
	if err := h.Replace(other); !errors.Is(err, fault.ErrConflict) {
		t.Errorf("replacing with an unthreaded message = %v, want a conflict", err)
	}
}

func TestDelete(t *testing.T) {
	h := newHarness(t, "boss", "alice")
	m := h.message("boss", []string{"alice"}, "s", "b")
	if err := h.PutReceipt(m.ID(), h.name("alice"), h.clock.Now()); err != nil {
		t.Fatal(err)
	}

	if err := h.Delete(m.ID()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if ok, _ := h.HasMessage(m.ID()); ok {
		t.Error("the message should be gone")
	}
	got, err := h.Receipts(m.ID())
	if err != nil {
		t.Fatalf("Receipts after delete: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("%d receipts survived the delete", len(got))
	}

	// Deleting twice is not an error: prune journals before it deletes, so a
	// retry after a crash must succeed.
	if err := h.Delete(m.ID()); err != nil {
		t.Errorf("deleting twice = %v, want nil", err)
	}
}

func TestConvoParticipants(t *testing.T) {
	h := newHarness(t, "boss", "alice", "carol")
	root := h.message("boss", []string{"alice"}, "work", "start")

	c, err := h.OpenConvo(root, "work")
	if err != nil {
		t.Fatalf("OpenConvo: %v", err)
	}
	// A conversation opens with the root message's participants: sender first.
	if got := user.Names(c.Participants()); strings.Join(got, ",") != "boss,alice" {
		t.Errorf("Participants() = %v, want boss,alice", got)
	}
	if !c.HasMember(h.name("boss")) || !c.HasMember(h.name("alice")) {
		t.Error("the root's participants should be members")
	}
	if c.HasMember(h.name("carol")) {
		t.Error("carol is not in the conversation yet")
	}

	if err := h.AddParticipant(root.ID(), h.name("carol")); err != nil {
		t.Fatalf("AddParticipant: %v", err)
	}
	c, err = h.Convo(root.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !c.HasMember(h.name("carol")) {
		t.Error("carol should be a member now")
	}
	if got := user.Names(c.Participants()); strings.Join(got, ",") != "boss,alice,carol" {
		t.Errorf("Participants() = %v; joins should append in order", got)
	}

	// Idempotent: two agents adding the same person both succeed.
	if err := h.AddParticipant(root.ID(), h.name("carol")); err != nil {
		t.Errorf("re-adding a member = %v, want nil", err)
	}
	c, err = h.Convo(root.ID())
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Participants()) != 3 {
		t.Errorf("a repeated join changed the membership to %v", user.Names(c.Participants()))
	}

	if err := h.AddParticipant(mail.ID{}, h.name("carol")); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("AddParticipant with no conversation = %v, want an internal fault", err)
	}
	missing, err := mail.NewID(epoch, h.entropy)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.AddParticipant(missing, h.name("carol")); !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("AddParticipant on a missing conversation = %v, want not found", err)
	}
}

// TestConvoMembershipSurvivesATruncatedTail: a join is an append like any
// other, so an interrupted one may be lost but must never corrupt the rest.
func TestConvoMembershipSurvivesATruncatedTail(t *testing.T) {
	h := newHarness(t, "boss", "alice", "carol")
	root := h.message("boss", []string{"alice"}, "work", "start")
	if _, err := h.OpenConvo(root, "work"); err != nil {
		t.Fatal(err)
	}
	if err := h.AddParticipant(root.ID(), h.name("carol")); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(h.Root(), "convos", root.ID().String()+".jsonl")
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	for cut := range len(full) + 1 {
		if err := os.WriteFile(path, full[:cut], 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := h.Convo(root.ID())
		if err != nil {
			continue // too short to be a conversation yet
		}
		// However much was lost, the membership that survived must be a prefix
		// of the real one — never a member who was never added.
		for _, m := range c.Participants() {
			if m.String() != "boss" && m.String() != "alice" && m.String() != "carol" {
				t.Fatalf("a %d byte prefix invented the member %q", cut, m)
			}
		}
		if len(c.Participants()) == 0 {
			t.Fatalf("a %d byte prefix produced a conversation with no members", cut)
		}
	}
}

// TestConvoRefusesInconsistentMembership pins the fold's own checks.
func TestConvoRefusesInconsistentMembership(t *testing.T) {
	h := newHarness(t, "boss", "alice")
	root := h.message("boss", []string{"alice"}, "work", "start")
	if _, err := h.OpenConvo(root, "work"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.Root(), "convos", root.ID().String()+".jsonl")

	at := `"at":"2026-07-24T12:00:00.000Z"`
	mid := root.ID().String()
	for _, tc := range []struct {
		name  string
		lines []string
	}{
		{"no participants", []string{
			`{"op":"open","title":"work",` + at + `}`,
			`{"op":"add","mid":"` + mid + `","index":1,` + at + `}`,
		}},
		{"join before open", []string{
			`{"op":"join","user":"carol",` + at + `}`,
		}},
		{"joined twice", []string{
			`{"op":"open","title":"work","users":["boss","alice"],` + at + `}`,
			`{"op":"add","mid":"` + mid + `","index":1,` + at + `}`,
			`{"op":"join","user":"carol",` + at + `}`,
			`{"op":"join","user":"carol",` + at + `}`,
		}},
		{"already a member at open", []string{
			`{"op":"open","title":"work","users":["boss","alice"],` + at + `}`,
			`{"op":"add","mid":"` + mid + `","index":1,` + at + `}`,
			`{"op":"join","user":"boss",` + at + `}`,
		}},
		{"join names a bad user", []string{
			`{"op":"open","title":"work","users":["boss"],` + at + `}`,
			`{"op":"add","mid":"` + mid + `","index":1,` + at + `}`,
			`{"op":"join","user":"../etc",` + at + `}`,
		}},
		{"join also names a message", []string{
			`{"op":"open","title":"work","users":["boss"],` + at + `}`,
			`{"op":"add","mid":"` + mid + `","index":1,` + at + `}`,
			`{"op":"join","user":"alice","mid":"` + mid + `",` + at + `}`,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(strings.Join(tc.lines, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := h.Convo(root.ID()); err == nil {
				t.Error("an inconsistent conversation loaded without complaint")
			}
		})
	}
}
