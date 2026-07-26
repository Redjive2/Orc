package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/mailman/internal/mail"
)

// MaxConvoLength bounds a conversation. A thread this long is a mailing list,
// and rendering it would be useless anyway.
const MaxConvoLength = 1 << 16

// convoOp is a conversation event's kind.
type convoOp string

const (
	// convoOpen names a new conversation, recording its title and the people
	// who were in it at the start.
	convoOpen convoOp = "open"
	// convoAdd places one message at one index.
	convoAdd convoOp = "add"
	// convoJoin adds a participant, which is what cc does.
	convoJoin convoOp = "join"
)

// MaxParticipants bounds a conversation's membership.
const MaxParticipants = 256

// convoEvent is one line of a conversation file.
type convoEvent struct {
	Op    convoOp  `json:"op"`
	Title string   `json:"title,omitempty"`
	Users []string `json:"users,omitempty"`
	User  string   `json:"user,omitempty"`
	MID   string   `json:"mid,omitempty"`
	Index int      `json:"index,omitempty"`
	At    string   `json:"at"`
}

// Convo is a conversation, folded from its file. It is immutable once returned.
//
// Membership is stored rather than derived from the messages, because the two
// are not the same thing: `cc` adds someone to a conversation before they have
// received anything in it, and a reply has to reach everyone in the thread
// rather than only the people the message being replied to happened to name.
// Deriving membership per message is how a cc'd participant silently falls out
// of the next reply.
type Convo struct {
	id      mail.ID
	title   string
	opened  time.Time
	order   []mail.ID
	members []user.Name
	skipped int
}

// ID returns the conversation's identifier, which is its root message's.
func (c Convo) ID() mail.ID { return c.id }

// Title returns the subject the conversation was rooted with.
func (c Convo) Title() string { return c.title }

// Opened returns when the conversation was created.
func (c Convo) Opened() time.Time { return c.opened }

// Messages returns the conversation's messages in index order.
func (c Convo) Messages() []mail.ID { return slices.Clone(c.order) }

// Len returns how many messages the conversation holds.
func (c Convo) Len() int { return len(c.order) }

// Skipped reports how many trailing bytes were unreadable and dropped.
func (c Convo) Skipped() int { return c.skipped }

// Participants returns everyone in the conversation, in join order.
func (c Convo) Participants() []user.Name { return slices.Clone(c.members) }

// HasMember reports whether a user is in the conversation.
//
// It is the access check for the whole thread: a member may read every message
// in it, including those sent before they joined, because that is what being
// added to a conversation has to mean for the addition to be worth anything.
func (c Convo) HasMember(name user.Name) bool { return user.Contains(c.members, name) }

// Zero reports whether the conversation was never loaded.
func (c Convo) Zero() bool { return c.id.Zero() }

// IndexOf returns a message's 1-based position, or 0 if it is not in the
// conversation.
func (c Convo) IndexOf(id mail.ID) int {
	for i, got := range c.order {
		if got.String() == id.String() {
			return i + 1
		}
	}
	return 0
}

func (s *Store) convoPath(id mail.ID) string {
	return filepath.Join(s.root, convosDir, id.String()+convoExt)
}

// OpenConvo creates a conversation rooted on a message.
//
// A conversation's identifier is its root message's, so no separate identifier
// has to be minted or kept unique. Rooting is idempotent: two agents replying
// to the same standalone message at the same moment both succeed, and the
// second finds the conversation the first made.
func (s *Store) OpenConvo(root mail.Message, title string) (Convo, error) {
	if err := mail.CheckSubject(title); err != nil {
		return Convo{}, err
	}

	var out Convo
	err := s.withLock(func() error {
		existing, err := s.Convo(root.ID())
		if err == nil {
			out = existing
			return nil
		}
		if !isNotFound(err) {
			return err
		}

		now := clock.Format(s.clock.Now())
		if err := s.appendConvo(root.ID(), convoEvent{
			Op:    convoOpen,
			Title: title,
			Users: user.Names(root.Participants()),
			At:    now,
		}); err != nil {
			return err
		}
		if err := s.appendConvo(root.ID(), convoEvent{Op: convoAdd, MID: root.ID().String(), Index: 1, At: now}); err != nil {
			return err
		}
		out, err = s.Convo(root.ID())
		return err
	})
	if err != nil {
		return Convo{}, err
	}
	return out, nil
}

// AddToConvo appends a message to a conversation and returns its index.
//
// The index is chosen while the lock is held, from the file being appended to,
// so two replies at the same instant get distinct positions rather than both
// claiming the same one.
func (s *Store) AddToConvo(convo mail.ID, id mail.ID) (int, error) {
	if convo.Zero() || id.Zero() {
		return 0, fault.Internal{Where: "store.AddToConvo", Detail: "conversation and message are both required"}
	}

	index := 0
	err := s.withLock(func() error {
		c, err := s.Convo(convo)
		if err != nil {
			return err
		}
		// Already a member: report the position it already has, so a retried
		// send does not add the same message twice.
		if at := c.IndexOf(id); at > 0 {
			index = at
			return nil
		}
		if c.Len() >= MaxConvoLength {
			return fault.Conflict{Path: convo.String(), Reason: fmt.Sprintf(
				"conversation already holds %d messages, the limit", MaxConvoLength)}
		}

		index = c.Len() + 1
		return s.appendConvo(convo, convoEvent{
			Op: convoAdd, MID: id.String(), Index: index, At: clock.Format(s.clock.Now()),
		})
	})
	if err != nil {
		return 0, err
	}
	if err := fault.Check(index >= 1, "store.AddToConvo", "produced index %d", index); err != nil {
		return 0, err
	}
	return index, nil
}

// AddParticipant adds a user to a conversation.
//
// It is idempotent: adding someone who is already in the thread succeeds and
// changes nothing, so two agents running `cc` at the same moment both report
// success rather than one of them reporting a conflict about a state that is
// exactly what it asked for.
func (s *Store) AddParticipant(convo mail.ID, name user.Name) error {
	if convo.Zero() || name.Zero() {
		return fault.Internal{Where: "store.AddParticipant", Detail: "conversation and user are both required"}
	}

	return s.withLock(func() error {
		c, err := s.Convo(convo)
		if err != nil {
			return err
		}
		if c.HasMember(name) {
			return nil
		}
		if len(c.members) >= MaxParticipants {
			return fault.Conflict{Path: convo.String(), Reason: fmt.Sprintf(
				"conversation already has %d participants, the limit", MaxParticipants)}
		}
		return s.appendConvo(convo, convoEvent{
			Op:   convoJoin,
			User: name.String(),
			At:   clock.Format(s.clock.Now()),
		})
	})
}

// appendConvo writes one conversation event. It must be called with the lock
// held.
func (s *Store) appendConvo(convo mail.ID, ev convoEvent) error {
	line, err := json.Marshal(ev)
	if err != nil {
		return fault.Internal{Where: "store.appendConvo", Detail: err.Error()}
	}
	if bytes.ContainsAny(line, "\n\r") {
		return fault.Internal{Where: "store.appendConvo", Detail: "encoded event contains a newline"}
	}
	return s.appendLine(s.convoPath(convo), line)
}

// Convo loads a conversation.
//
// Recovery follows the journal's rule: only the final line of an interrupted
// append can be damaged, so an unreadable last fragment is dropped and counted,
// and anything else is a hard error.
func (s *Store) Convo(id mail.ID) (Convo, error) {
	if id.Zero() {
		return Convo{}, fault.Internal{Where: "store.Convo", Detail: "no conversation id given"}
	}
	path := s.convoPath(id)

	data, err := s.ops.readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Convo{}, fault.NotFound{Target: id.String()}
		}
		return Convo{}, fault.IO{Op: "read", Path: path, Err: err}
	}
	if len(data) > MaxJournalSize {
		return Convo{}, fault.Parse{Path: path, Reason: fmt.Sprintf(
			"conversation file is %d bytes, limit is %d", len(data), MaxJournalSize)}
	}

	c := Convo{id: id}

	complete := len(data) == 0 || data[len(data)-1] == '\n'
	lines := bytes.Split(data, []byte("\n"))
	if complete && len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}

	for i, raw := range lines {
		lineNo := i + 1
		last := i == len(lines)-1

		if len(raw) == 0 {
			if last && !complete {
				continue
			}
			return Convo{}, fault.Parse{Path: path, Line: lineNo, Reason: "empty conversation line"}
		}
		if len(raw) > MaxJournalLine {
			return Convo{}, fault.Parse{Path: path, Line: lineNo, Reason: fmt.Sprintf(
				"conversation line is %d bytes, limit is %d", len(raw), MaxJournalLine)}
		}

		ev, err := decodeConvoEvent(path, lineNo, raw)
		if err != nil {
			if last && !complete {
				c.skipped = len(raw)
				break
			}
			return Convo{}, err
		}
		if err := c.apply(path, lineNo, ev); err != nil {
			return Convo{}, err
		}
	}

	if err := c.validate(path); err != nil {
		return Convo{}, err
	}
	return c, nil
}

func decodeConvoEvent(path string, line int, raw []byte) (convoEvent, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	var ev convoEvent
	if err := dec.Decode(&ev); err != nil {
		return convoEvent{}, fault.Parse{Path: path, Line: line, Reason: "conversation event: " + err.Error()}
	}
	if dec.More() {
		return convoEvent{}, fault.Parse{Path: path, Line: line, Reason: "conversation line has trailing content"}
	}
	return ev, nil
}

func (c *Convo) apply(path string, line int, ev convoEvent) error {
	bad := func(format string, args ...any) error {
		return fault.Parse{Path: path, Line: line, Reason: fmt.Sprintf(format, args...)}
	}

	at, err := clock.Parse(ev.At)
	if err != nil {
		return bad("%s", err)
	}

	switch ev.Op {
	case convoOpen:
		if !c.opened.IsZero() {
			return bad("conversation is opened twice")
		}
		if err := mail.CheckSubject(ev.Title); err != nil {
			return bad("conversation title: %s", err)
		}
		if ev.MID != "" || ev.Index != 0 || ev.User != "" {
			return bad("an open event must not name a message or a single user")
		}
		if len(ev.Users) == 0 {
			return bad("a conversation must open with at least one participant")
		}
		if len(ev.Users) > MaxParticipants {
			return bad("a conversation opened with %d participants, over the %d limit", len(ev.Users), MaxParticipants)
		}
		members, err := user.ParseList(ev.Users)
		if err != nil {
			return bad("conversation participants: %s", err)
		}
		if len(members) != len(ev.Users) {
			return bad("conversation participants repeat")
		}
		c.title, c.opened, c.members = ev.Title, at, members

	case convoJoin:
		if c.opened.IsZero() {
			return bad("a participant joins before the conversation is opened")
		}
		if ev.Title != "" || ev.MID != "" || ev.Index != 0 || len(ev.Users) > 0 {
			return bad("a join event may only name one user")
		}
		joiner, err := user.Parse(ev.User)
		if err != nil {
			return bad("conversation join: %s", err)
		}
		if user.Contains(c.members, joiner) {
			return bad("%s joins the conversation twice", joiner)
		}
		if len(c.members) >= MaxParticipants {
			return bad("conversation has more than %d participants", MaxParticipants)
		}
		c.members = append(c.members, joiner)

	case convoAdd:
		if c.opened.IsZero() {
			return bad("a message is added before the conversation is opened")
		}
		if ev.Title != "" || ev.User != "" || len(ev.Users) > 0 {
			return bad("an add event may only name a message")
		}
		id, err := mail.ParseID(ev.MID)
		if err != nil {
			return bad("conversation names a bad message id: %s", ev.MID)
		}
		// The index is stated and also derivable. Checking them against each
		// other is what catches a file that was appended to concurrently by a
		// build without locking.
		if want := len(c.order) + 1; ev.Index != want {
			return bad("message %s claims index %d but is in position %d", ev.MID, ev.Index, want)
		}
		if c.IndexOf(id) > 0 {
			return bad("message %s appears in the conversation twice", ev.MID)
		}
		c.order = append(c.order, id)

	default:
		return bad("unknown conversation operation %q", ev.Op)
	}
	return nil
}

func (c Convo) validate(path string) error {
	const where = "store.Convo"
	if err := fault.Check(!c.opened.IsZero(), where, "%s: conversation was never opened", path); err != nil {
		return err
	}
	if err := fault.Check(c.title != "", where, "%s: conversation has no title", path); err != nil {
		return err
	}
	if err := fault.Check(len(c.order) > 0, where, "%s: conversation holds no messages", path); err != nil {
		return err
	}
	if err := fault.Check(len(c.members) > 0, where, "%s: conversation has no participants", path); err != nil {
		return err
	}
	// The root of a conversation is the message it is named for, and it must be
	// first. Anything else means the identifier no longer describes the thread.
	return fault.Check(c.order[0].String() == c.id.String(), where,
		"%s: conversation is named for %s but starts with %s", path, c.id, c.order[0])
}

// Convos lists every conversation identifier in the store, in order.
func (s *Store) Convos() ([]mail.ID, error) {
	dir := filepath.Join(s.root, convosDir)
	entries, err := s.ops.readDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fault.IO{Op: "list", Path: dir, Err: err}
	}

	var out []mail.ID
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name, ok := trimSuffix(e.Name(), convoExt)
		if !ok {
			continue
		}
		id, err := mail.ParseID(name)
		if err != nil {
			continue
		}
		out = append(out, id)
	}
	return out, nil
}

func trimSuffix(s, suffix string) (string, bool) {
	if len(s) <= len(suffix) || s[len(s)-len(suffix):] != suffix {
		return "", false
	}
	return s[:len(s)-len(suffix)], true
}

// isNotFound reports whether an error is a missing-thing fault.
func isNotFound(err error) bool {
	var nf fault.NotFound
	return asNotFound(err, &nf)
}

func asNotFound(err error, out *fault.NotFound) bool {
	if e, ok := err.(fault.NotFound); ok {
		*out = e
		return true
	}
	return false
}
