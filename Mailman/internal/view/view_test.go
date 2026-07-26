package view_test

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/mailman/internal/mail"
	"orc/mailman/internal/query"
	"orc/mailman/internal/store"
	"orc/mailman/internal/view"
)

var epoch = time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

type entropy struct{ n uint64 }

func (e *entropy) Read(p []byte) (int, error) {
	for i := 0; i < len(p); i += 8 {
		e.n++
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], e.n)
		copy(p[i:], buf[:])
	}
	return len(p), nil
}

type rig struct {
	t   *testing.T
	s   *store.Store
	now *clock.Fake
	e   *entropy
}

func newRig(t *testing.T, users ...string) *rig {
	t.Helper()
	now := clock.NewFake(epoch, time.Minute)
	s, err := store.Open(t.TempDir(), now)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	r := &rig{t: t, s: s, now: now, e: &entropy{}}
	for _, name := range users {
		key, err := user.NewKey(r.e)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CreateUser(r.name(name), key); err != nil {
			t.Fatalf("CreateUser(%s): %v", name, err)
		}
	}
	return r
}

func (r *rig) name(s string) user.Name {
	r.t.Helper()
	n, err := user.Parse(s)
	if err != nil {
		r.t.Fatalf("Parse(%q): %v", s, err)
	}
	return n
}

// send stores a message and delivers it to every recipient.
func (r *rig) send(from string, to []string, subject, body string) mail.Message {
	r.t.Helper()
	at := r.now.Now()

	id, err := mail.NewID(at, r.e)
	if err != nil {
		r.t.Fatal(err)
	}
	recipients, err := user.ParseList(to)
	if err != nil {
		r.t.Fatal(err)
	}
	m, err := mail.New(id, mail.Ordinary, r.name(from), recipients, nil, subject, mail.ID{}, 0, at, []byte(body))
	if err != nil {
		r.t.Fatal(err)
	}
	if err := r.s.Put(m); err != nil {
		r.t.Fatal(err)
	}
	for _, name := range recipients {
		if _, err := r.s.Deliver(name, id); err != nil {
			r.t.Fatal(err)
		}
	}
	return m
}

func (r *rig) load(owner string) view.Mailbox {
	r.t.Helper()
	box, err := view.Load(r.s, r.name(owner))
	if err != nil {
		r.t.Fatalf("Load(%s): %v", owner, err)
	}
	return box
}

func TestLoadJoinsMessagesToMailboxState(t *testing.T) {
	r := newRig(t, "boss", "alice")
	first := r.send("boss", []string{"alice"}, "one", "a")
	r.send("boss", []string{"alice"}, "two", "b")

	if err := r.s.Mark(r.name("alice"), first.ID(), store.OpRead); err != nil {
		t.Fatal(err)
	}

	box := r.load("alice")
	rows := box.Rows()
	if len(rows) != 2 {
		t.Fatalf("mailbox holds %d rows, want 2", len(rows))
	}

	// Send order, not journal order.
	if rows[0].Message.Subject() != "one" || rows[1].Message.Subject() != "two" {
		t.Errorf("rows are out of order: %q, %q", rows[0].Message.Subject(), rows[1].Message.Subject())
	}
	if rows[0].Unread() {
		t.Error("the first message should be read")
	}
	if !rows[1].Unread() {
		t.Error("the second message should be unread")
	}

	unread, total := box.Counts()
	if unread != 1 || total != 2 {
		t.Errorf("Counts() = %d unread, %d total; want 1, 2", unread, total)
	}
	if box.Owner().String() != "alice" {
		t.Errorf("Owner() = %q", box.Owner())
	}
}

func TestScopes(t *testing.T) {
	r := newRig(t, "boss", "alice")
	alice := r.name("alice")

	read := r.send("boss", []string{"alice"}, "read", "a")
	r.send("boss", []string{"alice"}, "unread", "b")
	archived := r.send("boss", []string{"alice"}, "archived", "c")
	mine := r.send("alice", []string{"boss"}, "sent by me", "d")

	// The sender keeps a copy of their own outgoing mail, as the CLI does.
	if _, err := r.s.Deliver(alice, mine.ID()); err != nil {
		t.Fatal(err)
	}
	if err := r.s.Mark(alice, mine.ID(), store.OpRead); err != nil {
		t.Fatal(err)
	}
	if err := r.s.Mark(alice, read.ID(), store.OpRead); err != nil {
		t.Fatal(err)
	}
	if err := r.s.Mark(alice, archived.ID(), store.OpArchive); err != nil {
		t.Fatal(err)
	}

	box := r.load("alice")
	for _, tc := range []struct {
		scope view.Scope
		want  []string
	}{
		{view.Inbox, []string{"unread"}},
		{view.All, []string{"read", "unread"}},
		{view.Archive, []string{"archived"}},
		{view.Sent, []string{"sent by me"}},
		{view.Everything, []string{"read", "unread", "archived", "sent by me"}},
	} {
		var got []string
		for _, row := range box.In(tc.scope) {
			got = append(got, row.Message.Subject())
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("scope %d = %v, want %v", tc.scope, got, tc.want)
		}
	}
}

// TestSelfAddressedMailIsNotMine: mail you sent to yourself is mail you have to
// read, so it belongs in the inbox.
func TestSelfAddressedMailIsNotMine(t *testing.T) {
	r := newRig(t, "alice")
	r.send("alice", []string{"alice"}, "note to self", "remember")

	box := r.load("alice")
	if got := len(box.In(view.Inbox)); got != 1 {
		t.Errorf("the inbox holds %d messages, want 1", got)
	}
	if got := len(box.In(view.Sent)); got != 0 {
		t.Errorf("a self-addressed note should not count as sent, got %d", got)
	}
}

func TestSelectReturnsEveryMatch(t *testing.T) {
	r := newRig(t, "boss", "carol", "alice")
	r.send("boss", []string{"alice"}, "one", "a")
	r.send("boss", []string{"alice"}, "two", "b")
	r.send("carol", []string{"alice"}, "three", "c")

	box := r.load("alice")
	q, err := query.Parse(`from="boss"`)
	if err != nil {
		t.Fatal(err)
	}

	rows, err := box.Select(q, view.Everything, query.At(r.now.Now()))
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	// Never stops at the first hit: archive, read, and prune all act on the
	// whole set, and returning one would silently leave the rest behind.
	if len(rows) != 2 {
		t.Errorf("Select returned %d rows, want 2", len(rows))
	}

	if _, err := box.Select(query.Query{}, view.Everything, query.At(r.now.Now())); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Select with an unparsed query = %v, want an internal fault", err)
	}
}

func TestLatestPicksTheMostRecent(t *testing.T) {
	r := newRig(t, "boss", "alice")
	r.send("boss", []string{"alice"}, "old", "a")
	r.send("boss", []string{"alice"}, "new", "b")

	box := r.load("alice")
	rows := box.In(view.All)

	got, count, err := view.Latest(rows)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if got.Message.Subject() != "new" {
		t.Errorf("Latest chose %q, want the most recent", got.Message.Subject())
	}
	if count != 2 {
		t.Errorf("Latest reported %d candidates, want 2", count)
	}

	// It reports rather than guessing when there is nothing to choose from.
	if _, _, err := view.Latest(nil); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Latest(nil) = %v, want an internal fault", err)
	}
}

func TestLookupHelpers(t *testing.T) {
	r := newRig(t, "boss", "alice")
	m := r.send("boss", []string{"alice"}, "one", "a")

	box := r.load("alice")
	if got, ok := box.ByPUID(0); !ok || got.Message.ID().String() != m.ID().String() {
		t.Errorf("ByPUID(0) = %v, %v", got.Message.ID(), ok)
	}
	if _, ok := box.ByPUID(99); ok {
		t.Error("ByPUID found a message that is not there")
	}
	if got, ok := box.ByID(m.ID()); !ok || got.PUID() != 0 {
		t.Errorf("ByID = %v, %v", got.PUID(), ok)
	}
}

func TestThreadShowsOnlyWhatTheReaderReceived(t *testing.T) {
	r := newRig(t, "boss", "alice", "carol")

	root := r.send("boss", []string{"alice"}, "work", "one")
	convo, err := r.s.OpenConvo(root, "work")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := root.InConvo(convo.ID(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.s.Replace(bound); err != nil {
		t.Fatal(err)
	}

	// A later message in the same thread, sent to carol but not alice.
	at := r.now.Now()
	id, err := mail.NewID(at, r.e)
	if err != nil {
		t.Fatal(err)
	}
	later, err := mail.New(id, mail.Ordinary, r.name("boss"), []user.Name{r.name("carol")}, nil,
		"work", convo.ID(), 2, at, []byte("two"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.s.Put(later); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.AddToConvo(convo.ID(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.Deliver(r.name("carol"), id); err != nil {
		t.Fatal(err)
	}

	// Alice sees only her own message. Showing her one she was never sent
	// would be a disclosure, not a convenience.
	alice := r.load("alice").Thread(convo.ID())
	if len(alice) != 1 {
		t.Fatalf("alice sees %d messages in the thread, want 1", len(alice))
	}
	if carol := r.load("carol").Thread(convo.ID()); len(carol) != 1 {
		t.Fatalf("carol sees %d messages in the thread, want 1", len(carol))
	}
}

func TestCheckListsEveryRecipient(t *testing.T) {
	r := newRig(t, "boss", "alice", "carol")
	m := r.send("boss", []string{"alice", "carol"}, "one", "a")

	statuses, err := view.Check(r.s, m)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("Check returned %d statuses, want 2", len(statuses))
	}
	// Everyone appears, read or not: the question is who has *not* seen it.
	for _, s := range statuses {
		if s.Read() {
			t.Errorf("%s should not have read it yet", s.User)
		}
	}

	if err := r.s.PutReceipt(m.ID(), r.name("alice"), r.now.Now()); err != nil {
		t.Fatal(err)
	}
	statuses, err = view.Check(r.s, m)
	if err != nil {
		t.Fatal(err)
	}
	if !statuses[0].Read() || statuses[1].Read() {
		t.Errorf("expected alice read and carol unread, got %v", statuses)
	}
}

// TestCheckRefusesAStrayReceipt catches a receipt file copied between
// mailboxes, which would otherwise report a reader who was never sent anything.
func TestCheckRefusesAStrayReceipt(t *testing.T) {
	r := newRig(t, "boss", "alice", "carol")
	m := r.send("boss", []string{"alice"}, "one", "a")

	if err := r.s.PutReceipt(m.ID(), r.name("carol"), r.now.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := view.Check(r.s, m); !errors.Is(err, fault.ErrConflict) {
		t.Errorf("Check with a stray receipt = %v, want a conflict", err)
	}
}

// TestDamagedMessagesAreReportedNotHidden: one unreadable file must not hide
// the rest of a mailbox, and must not be silently omitted either.
func TestDamagedMessagesAreReportedNotHidden(t *testing.T) {
	r := newRig(t, "boss", "alice")
	broken := r.send("boss", []string{"alice"}, "broken", "a")
	r.send("boss", []string{"alice"}, "fine", "b")

	path := filepath.Join(r.s.Root(), "messages", broken.ID().String()[:2], broken.ID().String()+".msg")
	if err := os.WriteFile(path, []byte("not a message"), 0o600); err != nil {
		t.Fatal(err)
	}

	box := r.load("alice")
	if got := len(box.Rows()); got != 1 {
		t.Errorf("mailbox shows %d rows, want the one readable message", got)
	}
	damaged := box.Damaged()
	if len(damaged) != 1 {
		t.Fatalf("%d damaged messages reported, want 1", len(damaged))
	}
	if damaged[0].MID.String() != broken.ID().String() {
		t.Errorf("the wrong message was reported damaged: %s", damaged[0].MID)
	}
	if damaged[0].Err == nil {
		t.Error("the damage report carries no cause")
	}
}

func TestLoadRejectsBadArguments(t *testing.T) {
	r := newRig(t, "alice")
	if _, err := view.Load(nil, r.name("alice")); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Load without a store = %v, want an internal fault", err)
	}
	if _, err := view.Load(r.s, user.Name{}); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Load without an owner = %v, want an internal fault", err)
	}
	if _, err := view.Check(nil, mail.Message{}); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Check without a store = %v, want an internal fault", err)
	}
}

func TestEmptyMailboxLoads(t *testing.T) {
	r := newRig(t, "alice")
	box := r.load("alice")
	if len(box.Rows()) != 0 {
		t.Errorf("a fresh mailbox holds %d rows", len(box.Rows()))
	}
	unread, total := box.Counts()
	if unread != 0 || total != 0 {
		t.Errorf("Counts() = %d, %d; want 0, 0", unread, total)
	}
}

// TestSubjectsAreValidAtConstruction is what stops a half-built row quietly
// matching nothing — which for `archive` would mean mail silently left behind.
func TestSubjectsAreValidAtConstruction(t *testing.T) {
	r := newRig(t, "boss", "alice")
	r.send("boss", []string{"alice"}, "one", "a")

	for _, row := range r.load("alice").Rows() {
		if err := row.Subject.Validate(); err != nil {
			t.Errorf("row %d has an invalid subject: %v", row.PUID(), err)
		}
		if row.Subject.MID != row.Message.ID().String() {
			t.Errorf("the subject names %s but the row holds %s", row.Subject.MID, row.Message.ID())
		}
	}
}

// TestLoadThreadGivesMembersTheWholeConversation is the view-level half of what
// `cc` promises: membership grants the thread, including what came before.
func TestLoadThreadGivesMembersTheWholeConversation(t *testing.T) {
	r := newRig(t, "boss", "alice", "carol", "dave")

	root := r.send("boss", []string{"alice"}, "work", "one")
	convo, err := r.s.OpenConvo(root, "work")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := root.InConvo(convo.ID(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.s.Replace(bound); err != nil {
		t.Fatal(err)
	}

	// Carol joins without having received anything.
	if err := r.s.AddParticipant(convo.ID(), r.name("carol")); err != nil {
		t.Fatal(err)
	}

	c, rows, err := view.LoadThread(r.s, convo.ID(), r.name("carol"))
	if err != nil {
		t.Fatalf("LoadThread for a member: %v", err)
	}
	if c.Title() != "work" {
		t.Errorf("Title() = %q", c.Title())
	}
	if len(rows) != 1 {
		t.Fatalf("carol sees %d messages, want the one already in the thread", len(rows))
	}
	// It is history, not her mail: no puid, and not counted as unread.
	if rows[0].Filed {
		t.Error("a message carol never received should not be marked filed")
	}
	if rows[0].Unread() {
		t.Error("history should not count as unread mail")
	}

	// A member who *did* receive it keeps their own puid and read state.
	_, mine, err := view.LoadThread(r.s, convo.ID(), r.name("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 1 || !mine[0].Filed {
		t.Fatalf("alice's own message should be filed: %+v", mine)
	}

	// A non-member gets not-found, so this cannot enumerate conversations.
	if _, _, err := view.LoadThread(r.s, convo.ID(), r.name("dave")); !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("LoadThread for a non-member = %v, want not found", err)
	}
}

func TestLoadThreadRejectsBadArguments(t *testing.T) {
	r := newRig(t, "alice")
	id, err := mail.NewID(epoch, r.e)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := view.LoadThread(nil, id, r.name("alice")); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("LoadThread without a store = %v, want an internal fault", err)
	}
	if _, _, err := view.LoadThread(r.s, id, user.Name{}); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("LoadThread without a reader = %v, want an internal fault", err)
	}
	if _, _, err := view.LoadThread(r.s, id, r.name("alice")); !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("LoadThread on a missing conversation = %v, want not found", err)
	}
}
