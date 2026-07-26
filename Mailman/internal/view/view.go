// Package view projects the store into the frozen shapes commands render and
// query.
//
// It is the only place that knows a message and a mailbox have to be read
// together. A stored message says who it went to; a mailbox says what happened
// to it after it arrived — which puid it was given, whether this reader has
// read it, whether they archived it. Neither alone can answer "show me my
// unread mail", and building the join once here keeps every command from
// building it slightly differently.
package view

import (
	"fmt"
	"slices"
	"time"

	"orc/common/fault"
	"orc/common/user"
	"orc/mailman/internal/mail"
	"orc/mailman/internal/query"
	"orc/mailman/internal/store"
)

// Row is one message as one reader sees it.
type Row struct {
	Entry   store.Entry
	Message mail.Message
	Title   string // the conversation's title, empty when standalone
	Subject query.Subject

	// Filed reports whether the message is in this reader's own mailbox.
	//
	// It is false only for thread history: someone added to a conversation may
	// read messages sent before they joined, and those have no entry in their
	// journal and so no puid of their own. Everything a mailbox lists is filed.
	Filed bool
}

// PUID returns the reader's identifier for the message.
func (r Row) PUID() int { return r.Entry.PUID }

// Mine reports whether this is the reader's own outgoing mail.
//
// A sender's copy exists so `check` can answer "who has read what I sent" —
// without it, a sent message is in nobody's mailbox but its recipients', and
// the sender cannot name it in a query at all. It is excluded from the inbox,
// because mail you wrote yourself is not mail you have to read. A message
// addressed to yourself is not "mine" in this sense: you did have to read it,
// and agents use self-addressed mail as a scratch note.
func (r Row) Mine(owner user.Name) bool {
	return r.Message.From().String() == owner.String() &&
		!user.Contains(r.Message.Recipients(), owner)
}

// Unread reports whether the reader has yet to read it.
func (r Row) Unread() bool { return r.Entry.Unread() }

// Archived reports whether the reader has archived it.
func (r Row) Archived() bool { return r.Entry.Archived }

// Sent returns when the message was sent.
func (r Row) Sent() time.Time { return r.Message.Sent() }

// Mailbox is one reader's whole view of the store, frozen at load time.
type Mailbox struct {
	owner   user.Name
	rows    []Row
	damaged []Damage
	skipped int
}

// Damage records a message the mailbox knows about but could not read.
//
// It is kept rather than silently dropped, and it is kept rather than made
// fatal. One unreadable file must not hide the rest of someone's mail, but an
// inbox that quietly shows nine of ten messages is worse than one that shows
// nine and says so.
type Damage struct {
	MID mail.ID
	Err error
}

// Owner returns whose mailbox this is.
func (m Mailbox) Owner() user.Name { return m.owner }

// Rows returns every live message, newest last.
func (m Mailbox) Rows() []Row { return slices.Clone(m.rows) }

// Damaged returns the messages that could not be read.
func (m Mailbox) Damaged() []Damage { return slices.Clone(m.damaged) }

// Skipped reports how many trailing journal bytes were dropped as an
// interrupted append.
func (m Mailbox) Skipped() int { return m.skipped }

// Counts returns how many messages are unread and how many are live in total,
// excluding archived mail and the reader's own sent copies.
func (m Mailbox) Counts() (unread, total int) {
	for _, r := range m.In(All) {
		total++
		if r.Unread() {
			unread++
		}
	}
	return unread, total
}

// Load builds a reader's mailbox.
func Load(s *store.Store, owner user.Name) (Mailbox, error) {
	if s == nil {
		return Mailbox{}, fault.Internal{Where: "view.Load", Detail: "no store given"}
	}
	if owner.Zero() {
		return Mailbox{}, fault.Internal{Where: "view.Load", Detail: "no owner given"}
	}

	st, err := s.Replay(owner)
	if err != nil {
		return Mailbox{}, err
	}

	box := Mailbox{owner: owner, skipped: st.Skipped()}
	titles := map[string]string{}

	for _, entry := range st.Entries() {
		msg, err := s.Get(entry.MID)
		if err != nil {
			box.damaged = append(box.damaged, Damage{MID: entry.MID, Err: err})
			continue
		}

		title := ""
		if convo, threaded := msg.Convo(); threaded {
			title, err = lookupTitle(s, titles, convo)
			if err != nil {
				box.damaged = append(box.damaged, Damage{MID: entry.MID, Err: err})
				continue
			}
		}

		row, err := makeRow(entry, msg, title)
		if err != nil {
			box.damaged = append(box.damaged, Damage{MID: entry.MID, Err: err})
			continue
		}
		box.rows = append(box.rows, row)
	}

	// Ordered by send time, with the identifier as a tiebreak so two messages
	// sent in the same millisecond still have a stable order.
	slices.SortFunc(box.rows, func(a, b Row) int {
		if c := a.Sent().Compare(b.Sent()); c != 0 {
			return c
		}
		return a.Message.ID().Compare(b.Message.ID())
	})

	if err := box.validate(); err != nil {
		return Mailbox{}, err
	}
	return box, nil
}

// lookupTitle reads a conversation's title, memoised so a threaded inbox does
// not re-read one file per message.
func lookupTitle(s *store.Store, cache map[string]string, convo mail.ID) (string, error) {
	if title, ok := cache[convo.String()]; ok {
		return title, nil
	}
	c, err := s.Convo(convo)
	if err != nil {
		return "", err
	}
	cache[convo.String()] = c.Title()
	return c.Title(), nil
}

// makeRow joins one entry with its message and validates the result.
func makeRow(entry store.Entry, msg mail.Message, title string) (Row, error) {
	convo := ""
	if id, threaded := msg.Convo(); threaded {
		convo = id.String()
	}

	subject := query.Subject{
		PUID:     entry.PUID,
		MID:      msg.ID().String(),
		Kind:     msg.Kind().String(),
		From:     msg.From().String(),
		To:       user.Names(msg.To()),
		CC:       user.Names(msg.CC()),
		Subject:  msg.Subject(),
		Body:     msg.BodyString(),
		Convo:    convo,
		Title:    title,
		Index:    msg.Index(),
		Unread:   entry.Unread(),
		Archived: entry.Archived,
		Sent:     msg.Sent(),
	}
	// Validated here rather than at match time, so a half-built row is a
	// reported defect instead of a message that quietly matches nothing — which
	// for `archive` would mean mail silently left behind.
	if err := subject.Validate(); err != nil {
		return Row{}, err
	}

	return Row{Entry: entry, Message: msg, Title: title, Subject: subject, Filed: true}, nil
}

func (m Mailbox) validate() error {
	const where = "view.Mailbox"
	seen := make(map[int]string, len(m.rows))
	for _, r := range m.rows {
		if err := fault.Check(r.Entry.MID.String() == r.Message.ID().String(), where,
			"row joins entry %s to message %s", r.Entry.MID, r.Message.ID()); err != nil {
			return err
		}
		if other, dup := seen[r.PUID()]; dup {
			return fault.Internal{Where: where, Detail: fmt.Sprintf(
				"puid %d appears on both %s and %s", r.PUID(), other, r.Message.ID())}
		}
		seen[r.PUID()] = r.Message.ID().String()
	}
	return nil
}

// Scope narrows which messages a listing considers.
type Scope int

const (
	// Inbox is unread, unarchived mail: what `mailman inbox` shows.
	Inbox Scope = iota
	// All is every unarchived message, read or not.
	All
	// Archive is archived mail only.
	Archive
	// Sent is the reader's own outgoing mail.
	Sent
	// Everything ignores archive state and direction entirely, which is what a
	// query-driven command needs so `check`, `archive`, and `read` can reach
	// mail wherever it is.
	Everything
)

// In returns the rows within a scope, in send order.
func (m Mailbox) In(scope Scope) []Row {
	out := make([]Row, 0, len(m.rows))
	for _, r := range m.rows {
		mine := r.Mine(m.owner)
		keep := false
		switch scope {
		case Inbox:
			keep = !mine && !r.Archived() && r.Unread()
		case All:
			keep = !mine && !r.Archived()
		case Archive:
			keep = !mine && r.Archived()
		case Sent:
			keep = mine
		case Everything:
			keep = true
		}
		if keep {
			out = append(out, r)
		}
	}
	return out
}

// Select returns every row a query matches, within a scope, in send order.
//
// It never stops at the first hit. `archive`, `read`, and `prune` all act on
// the whole match set, and a selector that quietly returned one result would
// leave the rest of the caller's mail untouched without saying so.
func (m Mailbox) Select(q query.Query, scope Scope, now query.Now) ([]Row, error) {
	if q.Zero() {
		return nil, fault.Internal{Where: "view.Select", Detail: "the query was never parsed"}
	}

	var out []Row
	for _, r := range m.In(scope) {
		ok, err := q.Match(r.Subject, now)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// Latest returns the most recently sent row, which is what `open` resolves a
// query to. It also reports how many candidates there were, so the command can
// say that it narrowed rather than narrowing silently.
func Latest(rows []Row) (Row, int, error) {
	if len(rows) == 0 {
		return Row{}, 0, fault.Internal{Where: "view.Latest", Detail: "no rows to choose from"}
	}
	best := rows[0]
	for _, r := range rows[1:] {
		if c := r.Sent().Compare(best.Sent()); c > 0 || (c == 0 && r.Message.ID().Compare(best.Message.ID()) > 0) {
			best = r
		}
	}
	return best, len(rows), nil
}

// ByPUID finds a row by the reader's identifier for it.
func (m Mailbox) ByPUID(puid int) (Row, bool) {
	for _, r := range m.rows {
		if r.PUID() == puid {
			return r, true
		}
	}
	return Row{}, false
}

// ByID finds a row by message identifier.
func (m Mailbox) ByID(id mail.ID) (Row, bool) {
	for _, r := range m.rows {
		if r.Message.ID().String() == id.String() {
			return r, true
		}
	}
	return Row{}, false
}

// Thread returns the rows of one conversation that are in this mailbox, in
// index order. LoadThread is what a reader should normally see; this is the
// subset they were personally sent.
func (m Mailbox) Thread(convo mail.ID) []Row {
	var out []Row
	for _, r := range m.rows {
		if id, threaded := r.Message.Convo(); threaded && id.String() == convo.String() {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b Row) int { return a.Message.Index() - b.Message.Index() })
	return out
}

// LoadThread returns every message in a conversation, for a reader who is in it.
//
// Membership is the access check, and it is the whole conversation that
// membership grants: someone added by `cc` can read what was said before they
// joined. Anything less makes "added to the conversation" mean nothing — they
// would hold a notice about a thread they cannot see.
//
// A non-member gets ErrNotFound rather than a refusal, so the command cannot be
// used to discover which conversations exist.
//
// Messages the reader was not personally sent come back with Filed false: they
// have no entry in the reader's journal and therefore no puid, and the renderer
// shows that rather than inventing one.
func LoadThread(s *store.Store, convo mail.ID, reader user.Name) (store.Convo, []Row, error) {
	if s == nil {
		return store.Convo{}, nil, fault.Internal{Where: "view.LoadThread", Detail: "no store given"}
	}
	if reader.Zero() {
		return store.Convo{}, nil, fault.Internal{Where: "view.LoadThread", Detail: "no reader given"}
	}

	c, err := s.Convo(convo)
	if err != nil {
		return store.Convo{}, nil, err
	}
	if !c.HasMember(reader) {
		return store.Convo{}, nil, fault.NotFound{Target: convo.String()}
	}

	// The reader's own state, so their messages keep their puids and read marks.
	box, err := Load(s, reader)
	if err != nil {
		return store.Convo{}, nil, err
	}

	var out []Row
	for _, mid := range c.Messages() {
		if row, ok := box.ByID(mid); ok {
			out = append(out, row)
			continue
		}

		msg, err := s.Get(mid)
		if err != nil {
			// One unreadable message must not hide the rest of the thread. The
			// gap is visible in the index numbering, which is why the renderer
			// prints those.
			continue
		}
		row, err := makeHistoryRow(msg, c.Title())
		if err != nil {
			continue
		}
		out = append(out, row)
	}

	slices.SortFunc(out, func(a, b Row) int { return a.Message.Index() - b.Message.Index() })
	return c, out, nil
}

// makeHistoryRow builds a row for a message the reader has access to through a
// conversation but never personally received.
func makeHistoryRow(msg mail.Message, title string) (Row, error) {
	// The entry is synthetic: delivery is the only thing that assigns a puid,
	// and this message was never delivered to this reader. It is marked read so
	// nothing counts it as unread mail, and Filed false is what tells the
	// renderer not to print an identifier it does not have.
	entry := store.Entry{MID: msg.ID(), PUID: 0, Delivered: msg.Sent(), ReadAt: msg.Sent()}

	row, err := makeRow(entry, msg, title)
	if err != nil {
		return Row{}, err
	}
	row.Filed = false
	return row, nil
}

// Status is one recipient's read state for one message, for `check`.
type Status struct {
	User   user.Name
	ReadAt time.Time
}

// Read reports whether this recipient has read the message.
func (s Status) Read() bool { return !s.ReadAt.IsZero() }

// Check reports who has and has not read a message.
//
// Every recipient appears, read or not — the question `check` answers is "who
// has *not* seen this", and a list of only the readers cannot answer it.
func Check(s *store.Store, msg mail.Message) ([]Status, error) {
	if s == nil {
		return nil, fault.Internal{Where: "view.Check", Detail: "no store given"}
	}

	receipts, err := s.Receipts(msg.ID())
	if err != nil {
		return nil, err
	}
	when := make(map[string]time.Time, len(receipts))
	for _, r := range receipts {
		when[r.User.String()] = r.At
	}

	recipients := msg.Recipients()
	out := make([]Status, 0, len(recipients))
	for _, name := range recipients {
		out = append(out, Status{User: name, ReadAt: when[name.String()]})
	}

	// A receipt from someone who is not a recipient means a file was copied
	// between mailboxes. It is reported rather than ignored.
	for _, r := range receipts {
		if !user.Contains(recipients, r.User) {
			return nil, fault.Conflict{Path: msg.ID().String(), Reason: fmt.Sprintf(
				"there is a read receipt from %s, who is not a recipient", r.User)}
		}
	}
	return out, nil
}
