// Package read decodes the other tools' stores, without their code.
//
// This is what makes the omniscient views omniscient. Mailman shows one
// mailbox because that is what a mailbox is for; orcprobe shows all of them at
// once, straight off the copied store, with no identity and no permission
// check — the thing no real agent could do. Macmuffin shows the pool; orcprobe
// shows the pool plus what `pool` hides.
//
// Everything here is read-only and forgiving in one direction only. A file it
// cannot decode becomes a Damage and the rest still renders, because a view
// that shows nothing because one message is broken is a view that fails exactly
// when it is needed. What it never does is guess: a damaged file is reported,
// never interpreted.
//
// The formats belong to other tools. That coupling is real and deliberate —
// there is no other way to read a store from outside — so each decoder names
// the file it mirrors, and a format that moves is a compile-or-test failure
// here rather than a silently empty table.
package read

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"orc/orcprobe/internal/clock"
	"orc/orcprobe/internal/fault"
)

// Mailman's layout, from Mailman/internal/store/store.go.
const (
	mailUsersDir    = "users"
	mailMessagesDir = "messages"
	mailUserFile    = "user.json"
	mailJournalFile = "journal.jsonl"
	mailMessageExt  = ".msg"
	mailReceiptExt  = ".rcpt"

	// mailFormat is the magic line every stored message starts with.
	mailFormat = "mailman/1"
)

// Damage is one thing that could not be read, and why. Views show these rather
// than hiding them: a mailbox that quietly shows nine of ten messages is worse
// than one that shows nine and says so.
type Damage struct {
	Path string
	Why  string
}

// Message is one stored message, decoded.
type Message struct {
	MID     string
	Kind    string
	From    string
	To      []string
	CC      []string
	Subject string
	Convo   string
	Index   int
	Sent    time.Time
	Size    int
	// Body is the markdown, present only when Messages was asked for it.
	Body []byte
	// Readers are the recipients who have read it, from the receipt files.
	Readers []string
	// Per-recipient state, folded from each mailbox's journal. A message is
	// unread for some people and read for others, so these are maps rather than
	// flags — the whole reason orcprobe can show what one mailbox cannot.
	PUID     map[string]int
	Unread   map[string]bool
	Archived map[string]bool

	Path string
}

// Recipients returns everyone the message went to, to and cc together.
func (m Message) Recipients() []string {
	out := make([]string, 0, len(m.To)+len(m.CC))
	out = append(out, m.To...)
	out = append(out, m.CC...)
	return out
}

// UnreadBy reports whether anybody still has it unread.
func (m Message) UnreadBy() bool {
	for _, unread := range m.Unread {
		if unread {
			return true
		}
	}
	return false
}

// Mailbox is one user's state, folded from their journal.
type Mailbox struct {
	Name     string
	Unread   int
	Total    int
	Archived int
	// Created is when the mailbox record says it was made.
	Created time.Time
}

// MailMoment is one journal event, with the mailbox it happened in. The fold
// throws away order to compute state; the timeline needs it back, so it is kept
// here rather than recomputed by reading every journal twice.
type MailMoment struct {
	User string
	Op   string
	MID  string
	At   time.Time
}

// Mail is a whole decoded mail store.
type Mail struct {
	Present   bool
	Mailboxes []Mailbox
	Messages  []Message
	Events    []MailMoment
	Damage    []Damage
}

// Find returns one message by id.
func (m Mail) Find(mid string) (Message, bool) {
	for _, msg := range m.Messages {
		if msg.MID == mid {
			return msg, true
		}
	}
	return Message{}, false
}

// Mailman decodes a copied mail store.
//
// bodies asks for message bodies to be read as well as headers. Views that only
// list messages leave them out: a store with ten thousand messages should not
// be pulled into memory to draw a table of subjects.
func Mailman(root string, bodies bool) (Mail, error) {
	var out Mail

	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, fault.IO{Op: "look at", Path: root, Err: err}
	}
	if !info.IsDir() {
		return out, fault.Conflict{Path: root, Reason: "is not a mail store"}
	}
	out.Present = true

	names, err := mailboxNames(root)
	if err != nil {
		return out, err
	}

	// The per-user journals are folded first, so every message can be annotated
	// with who has it unread and under what puid.
	state := map[string]map[string]entryState{}
	for _, name := range names {
		box, folded, moments, damage, err := foldMailbox(root, name)
		if err != nil {
			return out, err
		}
		out.Damage = append(out.Damage, damage...)
		out.Mailboxes = append(out.Mailboxes, box)
		out.Events = append(out.Events, moments...)
		state[name] = folded
	}

	messages, damage, err := readMessages(root, bodies)
	if err != nil {
		return out, err
	}
	out.Damage = append(out.Damage, damage...)

	for i := range messages {
		msg := &messages[i]
		msg.PUID = map[string]int{}
		msg.Unread = map[string]bool{}
		msg.Archived = map[string]bool{}
		for user, folded := range state {
			entry, ok := folded[msg.MID]
			if !ok {
				continue
			}
			msg.PUID[user] = entry.puid
			msg.Unread[user] = !entry.read
			msg.Archived[user] = entry.archived
		}
	}

	out.Messages = messages
	sort.Slice(out.Messages, func(i, j int) bool { return out.Messages[i].Sent.Before(out.Messages[j].Sent) })
	sort.Slice(out.Mailboxes, func(i, j int) bool { return out.Mailboxes[i].Name < out.Mailboxes[j].Name })
	return out, nil
}

func mailboxNames(root string) ([]string, error) {
	dir := filepath.Join(root, mailUsersDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fault.IO{Op: "list", Path: dir, Err: err}
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), mailUserFile)); err != nil {
			continue
		}
		names = append(names, e.Name())
	}
	return names, nil
}

// entryState is one message's state in one mailbox.
type entryState struct {
	puid     int
	read     bool
	archived bool
	pruned   bool
}

// mailEvent is one line of a Mailman user journal, from
// Mailman/internal/store/journal.go.
type mailEvent struct {
	Op   string `json:"op"`
	MID  string `json:"mid"`
	PUID int    `json:"puid,omitempty"`
	At   string `json:"at"`
}

// foldMailbox replays one user's journal into per-message state, keeping the
// events themselves for the timeline.
func foldMailbox(root, name string) (Mailbox, map[string]entryState, []MailMoment, []Damage, error) {
	box := Mailbox{Name: name}
	folded := map[string]entryState{}
	var moments []MailMoment

	if rec, err := os.ReadFile(filepath.Join(root, mailUsersDir, name, mailUserFile)); err == nil {
		var stored struct {
			Created string `json:"created"`
		}
		if json.Unmarshal(rec, &stored) == nil {
			if at, err := clock.Parse(stored.Created); err == nil {
				box.Created = at
			}
		}
	}

	path := filepath.Join(root, mailUsersDir, name, mailJournalFile)
	lines, complete, err := readLines(path)
	if err != nil {
		return box, nil, nil, nil, err
	}

	var damage []Damage
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev mailEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			if i == len(lines)-1 && !complete {
				break // an interrupted append, which the copy caught mid-write
			}
			damage = append(damage, Damage{Path: path, Why: "line " + strconv.Itoa(i+1) + " does not parse"})
			continue
		}

		entry := folded[ev.MID]
		switch ev.Op {
		case "deliver":
			entry.puid = ev.PUID
		case "read":
			entry.read = true
		case "archive":
			entry.archived = true
		case "prune":
			entry.pruned = true
		default:
			damage = append(damage, Damage{Path: path, Why: "unknown journal op " + strconv.Quote(ev.Op)})
			continue
		}
		folded[ev.MID] = entry

		if at, err := clock.Parse(ev.At); err == nil {
			moments = append(moments, MailMoment{User: name, Op: ev.Op, MID: ev.MID, At: at})
		}
	}

	for _, entry := range folded {
		if entry.pruned {
			continue
		}
		box.Total++
		if entry.archived {
			box.Archived++
		}
		if !entry.read {
			box.Unread++
		}
	}
	return box, folded, moments, damage, nil
}

// readMessages walks the message tree and decodes every stored message.
func readMessages(root string, bodies bool) ([]Message, []Damage, error) {
	dir := filepath.Join(root, mailMessagesDir)
	var (
		out    []Message
		damage []Damage
	)

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fault.IO{Op: "walk", Path: path, Err: err}
		}
		if d.IsDir() || !strings.HasSuffix(path, mailMessageExt) {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			damage = append(damage, Damage{Path: path, Why: err.Error()})
			return nil
		}
		msg, err := decodeMessage(path, data, bodies)
		if err != nil {
			damage = append(damage, Damage{Path: path, Why: err.Error()})
			return nil
		}
		msg.Readers = readReceipts(path)
		out = append(out, msg)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return out, damage, nil
}

// readReceipts lists who has read a message. One file per reader is what makes
// two recipients never contend, and it is also what makes this a directory
// listing rather than a parse.
func readReceipts(messagePath string) []string {
	dir := strings.TrimSuffix(messagePath, mailMessageExt) + mailReceiptExt
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))
	}
	sort.Strings(out)
	return out
}

// decodeMessage reads one stored message.
//
// The format is Mailman's: a magic line, "key: value" headers, a blank line,
// then exactly `bytes:` bytes of body. The count is what makes the body
// unambiguous — a body containing the magic line, or a thousand blank lines,
// still decodes back to itself — so this reader consumes the count and never
// scans for a terminator.
func decodeMessage(path string, data []byte, bodies bool) (Message, error) {
	reader := bufio.NewReader(bytes.NewReader(data))

	magic, err := reader.ReadString('\n')
	if err != nil || strings.TrimRight(magic, "\n") != mailFormat {
		return Message{}, fault.Parse{Path: path, Line: 1, Reason: "not a " + mailFormat + " message"}
	}

	msg := Message{Path: path}
	consumed := len(magic)
	line := 1
	for {
		text, err := reader.ReadString('\n')
		if err != nil {
			return Message{}, fault.Parse{Path: path, Line: line + 1, Reason: "headers end without a blank line"}
		}
		consumed += len(text)
		line++
		trimmed := strings.TrimRight(text, "\n")
		if trimmed == "" {
			break
		}

		key, value, ok := strings.Cut(trimmed, ": ")
		if !ok {
			return Message{}, fault.Parse{Path: path, Line: line, Reason: "header line has no value"}
		}
		switch key {
		case "id":
			msg.MID = value
		case "kind":
			msg.Kind = value
		case "from":
			msg.From = value
		case "to":
			msg.To = splitList(value)
		case "cc":
			msg.CC = splitList(value)
		case "subject":
			msg.Subject = unescape(value)
		case "convo":
			msg.Convo = value
		case "index":
			n, err := strconv.Atoi(value)
			if err != nil {
				return Message{}, fault.Parse{Path: path, Line: line, Reason: "index is not a number"}
			}
			msg.Index = n
		case "sent":
			at, err := clock.Parse(value)
			if err != nil {
				return Message{}, fault.Parse{Path: path, Line: line, Reason: "sent: " + err.Error()}
			}
			msg.Sent = at
		case "bytes":
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 {
				return Message{}, fault.Parse{Path: path, Line: line, Reason: "bytes is not a count"}
			}
			msg.Size = n
		}
	}

	if msg.MID == "" || msg.From == "" {
		return Message{}, fault.Parse{Path: path, Reason: "message has no id or no sender"}
	}
	if consumed+msg.Size > len(data) {
		return Message{}, fault.Parse{Path: path, Reason: "body is shorter than its byte count says"}
	}
	if bodies {
		msg.Body = data[consumed : consumed+msg.Size]
	}
	return msg, nil
}

// unescape reverses the header escaping Mailman applies, so a subject with a
// newline in it reads back as one.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case '\\':
				b.WriteByte('\\')
			default:
				b.WriteByte('\\')
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// readLines reads a file into lines, reporting whether the last one was
// complete. Every journal reader in this tree needs that distinction: a
// truncated final line is an interrupted append, and anything else is damage.
func readLines(path string) ([]string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, true, nil
		}
		return nil, false, fault.IO{Op: "read", Path: path, Err: err}
	}

	var lines []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, false, fault.IO{Op: "read", Path: path, Err: err}
	}
	return lines, bytes.HasSuffix(data, []byte("\n")), nil
}
