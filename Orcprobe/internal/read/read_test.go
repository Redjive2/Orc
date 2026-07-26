package read

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// message writes one stored message in Mailman's format: magic line, headers,
// blank line, then exactly `bytes` bytes of body.
func message(t *testing.T, root, mid, from, to, subject, body string) {
	t.Helper()
	header := strings.Join([]string{
		mailFormat,
		"id: " + mid,
		"kind: mail",
		"from: " + from,
		"to: " + to,
		"subject: " + subject,
		"sent: 2026-07-01T09:00:00.000Z",
		"bytes: " + itoa(len(body)),
		"",
	}, "\n")
	write(t, filepath.Join(root, mailMessagesDir, mid[:2], mid+mailMessageExt), header+"\n"+body)
}

func mailbox(t *testing.T, root, name string, events ...string) {
	t.Helper()
	write(t, filepath.Join(root, mailUsersDir, name, mailUserFile),
		`{"version":1,"name":"`+name+`","created":"2026-01-01T00:00:00.000Z"}`)
	body := ""
	for _, e := range events {
		body += e + "\n"
	}
	write(t, filepath.Join(root, mailUsersDir, name, mailJournalFile), body)
}

func TestMailmanDecodesAcrossMailboxes(t *testing.T) {
	root := t.TempDir()
	message(t, root, "ab111111-0001", "boss", "alice, bob", "the plan", "read the plan\n")
	mailbox(t, root, "alice", `{"op":"deliver","mid":"ab111111-0001","puid":0,"at":"2026-07-01T09:00:01.000Z"}`)
	mailbox(t, root, "bob",
		`{"op":"deliver","mid":"ab111111-0001","puid":7,"at":"2026-07-01T09:00:01.000Z"}`,
		`{"op":"read","mid":"ab111111-0001","at":"2026-07-01T10:00:00.000Z"}`)

	mail, err := Mailman(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(mail.Messages) != 1 {
		t.Fatalf("decoded %d messages, want 1", len(mail.Messages))
	}

	msg := mail.Messages[0]
	if msg.From != "boss" || len(msg.To) != 2 {
		t.Fatalf("decoded %+v", msg)
	}
	if msg.Subject != "the plan" {
		t.Fatalf("subject is %q", msg.Subject)
	}
	if string(msg.Body) != "read the plan\n" {
		t.Fatalf("body is %q", string(msg.Body))
	}

	// The cross-user facts are the whole point: one message, unread by one
	// recipient and read by the other, under two different puids.
	if !msg.Unread["alice"] || msg.Unread["bob"] {
		t.Fatalf("unread state is %v, want alice unread and bob read", msg.Unread)
	}
	if msg.PUID["alice"] != 0 || msg.PUID["bob"] != 7 {
		t.Fatalf("puids are %v; they are per-mailbox and must not be shared", msg.PUID)
	}
	if !msg.UnreadBy() {
		t.Fatal("a message one recipient has not read reports as read by everybody")
	}

	for _, box := range mail.Mailboxes {
		switch box.Name {
		case "alice":
			if box.Unread != 1 {
				t.Fatalf("alice has %d unread, want 1", box.Unread)
			}
		case "bob":
			if box.Unread != 0 {
				t.Fatalf("bob has %d unread, want 0", box.Unread)
			}
		}
	}
}

// TestBodyIsReadByCountNotByScanning is the property Mailman's format exists to
// give: a body containing the format's own magic line still decodes back to
// itself, because the reader consumes a byte count and never searches.
func TestBodyIsReadByCountNotByScanning(t *testing.T) {
	root := t.TempDir()
	hostile := mailFormat + "\nid: forged\n\nnot a real message\n"
	message(t, root, "cd222222-0002", "boss", "alice", "nested", hostile)

	mail, err := Mailman(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(mail.Messages) != 1 {
		t.Fatalf("decoded %d messages; a body was read as a second one", len(mail.Messages))
	}
	if string(mail.Messages[0].Body) != hostile {
		t.Fatalf("body came back as %q", string(mail.Messages[0].Body))
	}
	if mail.Messages[0].MID == "forged" {
		t.Fatal("a header inside a body was read as the message's own")
	}
}

func TestDamagedMessagesAreReportedNotHidden(t *testing.T) {
	root := t.TempDir()
	message(t, root, "ab111111-0001", "boss", "alice", "fine", "ok\n")
	write(t, filepath.Join(root, mailMessagesDir, "ff", "ff999999-0009"+mailMessageExt), "this is not a message")
	mailbox(t, root, "alice")

	mail, err := Mailman(root, false)
	if err != nil {
		t.Fatalf("one damaged message failed the whole read: %v", err)
	}
	if len(mail.Messages) != 1 {
		t.Fatalf("decoded %d messages, want the one good one", len(mail.Messages))
	}
	if len(mail.Damage) != 1 {
		t.Fatalf("reported %d damaged files, want 1 — a silent skip is a view that lies", len(mail.Damage))
	}
}

func TestReceiptsSayWhoRead(t *testing.T) {
	root := t.TempDir()
	message(t, root, "ab111111-0001", "boss", "alice, bob", "the plan", "x\n")
	write(t, filepath.Join(root, mailMessagesDir, "ab", "ab111111-0001"+mailReceiptExt, "bob.rcpt"), "")
	mailbox(t, root, "alice")

	mail, err := Mailman(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := mail.Messages[0].Readers; len(got) != 1 || got[0] != "bob" {
		t.Fatalf("readers are %v, want [bob]", got)
	}
}

func task(t *testing.T, root, name string, events ...string) {
	t.Helper()
	write(t, filepath.Join(root, muffTasksDir, name, muffRecordFile),
		`{"version":1,"name":"`+name+`","author":"alice","priority":3,"difficulty":4,"created":"2026-01-01T00:00:00.000Z"}`)
	body := ""
	for _, e := range events {
		body += e + "\n"
	}
	write(t, filepath.Join(root, muffTasksDir, name, muffJournalFile), body)
}

func TestMacmuffinFoldsATask(t *testing.T) {
	root := t.TempDir()
	task(t, root, "refactor",
		`{"op":"scope","by":"alice","paths":["internal/"],"at":"2026-07-01T09:00:00.000Z"}`,
		`{"op":"sub.add","by":"alice","sub":"parser","at":"2026-07-01T09:01:00.000Z"}`,
		`{"op":"sub.add","by":"alice","sub":"lexer","at":"2026-07-01T09:02:00.000Z"}`,
		`{"op":"push","by":"alice","at":"2026-07-01T09:03:00.000Z"}`,
		`{"op":"claim","by":"bob","at":"2026-07-01T09:04:00.000Z"}`,
		`{"op":"invite","by":"bob","agent":"carol","at":"2026-07-01T09:05:00.000Z"}`,
		`{"op":"status","by":"bob","status":3,"at":"2026-07-01T09:06:00.000Z"}`,
		`{"op":"sub.done","by":"bob","sub":"lexer","at":"2026-07-01T09:07:00.000Z"}`,
	)
	write(t, filepath.Join(root, muffTombstoneFile), `{"name":"abandoned"}`+"\n")

	tasks, err := Macmuffin(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks.Tasks) != 1 {
		t.Fatalf("decoded %d tasks, want 1", len(tasks.Tasks))
	}

	got := tasks.Tasks[0]
	if got.Owner != "bob" {
		t.Fatalf("owner is %q, want bob", got.Owner)
	}
	if len(got.Collaborators) != 1 || got.Collaborators[0] != "carol" {
		t.Fatalf("collaborators are %v, want [carol]", got.Collaborators)
	}
	if got.Status != 3 || !got.Pushed || got.Complete {
		t.Fatalf("folded state is %+v", got)
	}
	if done, total := got.Done(); done != 1 || total != 2 {
		t.Fatalf("subtasks are %d/%d, want 1/2", done, total)
	}
	if got.Priority != 3 || got.Difficulty != 4 || got.Author != "alice" {
		t.Fatalf("the task record was not read: %+v", got)
	}
	// The pool hides deleted names; this view does not.
	if len(tasks.Tombstones) != 1 || tasks.Tombstones[0] != "abandoned" {
		t.Fatalf("tombstones are %v", tasks.Tombstones)
	}
}

func TestMacmuffinSurvivesATornJournal(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, muffTasksDir, "refactor", muffJournalFile),
		`{"op":"claim","by":"bob","at":"2026-07-01T09:00:00.000Z"}`+"\n"+`{"op":"stat`)

	tasks, err := Macmuffin(root)
	if err != nil {
		t.Fatalf("a torn final line failed the read: %v", err)
	}
	if tasks.Tasks[0].Owner != "bob" {
		t.Fatal("the events before the torn line were lost")
	}
	if len(tasks.Damage) != 0 {
		t.Fatalf("an interrupted append was reported as damage: %v", tasks.Damage)
	}
}

func TestCQCountsAppliedAndSpotsACursor(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, cqAppliedFile), "{\"id\":\"a\"}\n{\"id\":\"b\"}\n")

	sync, err := CQ(root)
	if err != nil {
		t.Fatal(err)
	}
	if sync.Applied != 2 {
		t.Fatalf("counted %d applied actions, want 2", sync.Applied)
	}
	if sync.Cursor {
		t.Fatal("a probe with no cursor file reported one")
	}

	write(t, filepath.Join(root, cqCursorFile), `{"watermark":42}`)
	sync, err = CQ(root)
	if err != nil {
		t.Fatal(err)
	}
	if !sync.Cursor {
		t.Fatal("a surviving sync cursor was not noticed; that is the fourth stop in front of the network")
	}
}

func TestTimelineMergesTools(t *testing.T) {
	root := t.TempDir()
	message(t, root, "ab111111-0001", "boss", "alice", "the plan", "x\n")
	mailbox(t, root, "alice",
		`{"op":"deliver","mid":"ab111111-0001","puid":0,"at":"2026-07-01T09:00:01.000Z"}`,
		`{"op":"read","mid":"ab111111-0001","at":"2026-07-01T11:00:00.000Z"}`)

	tasksRoot := t.TempDir()
	task(t, tasksRoot, "refactor",
		`{"op":"claim","by":"bob","at":"2026-07-01T10:00:00.000Z"}`)

	mail, err := Mailman(root, false)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := Macmuffin(tasksRoot)
	if err != nil {
		t.Fatal(err)
	}

	moments := Timeline(mail, tasks)
	if len(moments) != 3 {
		t.Fatalf("merged %d moments, want the send, the claim, and the read: %+v", len(moments), moments)
	}
	// Time order across tools is the whole point of the view.
	for i := 1; i < len(moments); i++ {
		if moments[i].At.Before(moments[i-1].At) {
			t.Fatalf("moments are out of order: %+v", moments)
		}
	}
	if moments[0].Tool != ToolMailman || moments[1].Tool != ToolMacmuffin || moments[2].Tool != ToolMailman {
		t.Fatalf("tools are %s, %s, %s", moments[0].Tool, moments[1].Tool, moments[2].Tool)
	}
	// A delivery is the same instant as its send from the other side, so it is
	// not shown twice.
	for _, m := range moments {
		if m.What == "deliver" {
			t.Fatal("a delivery was shown alongside its send, doubling every line")
		}
	}
}
