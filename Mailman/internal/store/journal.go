package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/mailman/internal/mail"
)

// Op is a journal event's kind.
type Op string

const (
	// OpDeliver records mail arriving and assigns its puid.
	OpDeliver Op = "deliver"
	// OpRead records that the mailbox's owner has read a message.
	OpRead Op = "read"
	// OpArchive moves a message out of the inbox.
	OpArchive Op = "archive"
	// OpPrune records that an archived message was deleted.
	OpPrune Op = "prune"
)

func (o Op) valid() bool {
	switch o {
	case OpDeliver, OpRead, OpArchive, OpPrune:
		return true
	default:
		return false
	}
}

// event is one journal line.
type event struct {
	Op   Op     `json:"op"`
	MID  string `json:"mid"`
	PUID int    `json:"puid,omitempty"`
	At   string `json:"at"`
}

func (e event) validate(path string, line int) error {
	bad := func(format string, args ...any) error {
		return fault.Parse{Path: path, Line: line, Reason: fmt.Sprintf(format, args...)}
	}

	if !e.Op.valid() {
		return bad("unknown journal operation %q", e.Op)
	}
	if _, err := mail.ParseID(e.MID); err != nil {
		return bad("journal event names a bad message id: %s", e.MID)
	}
	if _, err := clock.Parse(e.At); err != nil {
		return bad("journal event has a bad timestamp: %s", e.At)
	}
	// A puid is assigned exactly once, at delivery. Carrying one on any other
	// event would mean two places could disagree about it.
	if e.Op == OpDeliver {
		if e.PUID < 0 {
			return bad("delivery event has puid %d", e.PUID)
		}
	} else if e.PUID != 0 {
		return bad("%s event carries a puid, which only delivery may do", e.Op)
	}
	return nil
}

// Entry is one message's state in one mailbox, after replay.
type Entry struct {
	MID       mail.ID
	PUID      int
	Delivered time.Time
	ReadAt    time.Time // zero when unread
	Archived  bool
	Pruned    bool
}

// Unread reports whether the owner has yet to read this message.
func (e Entry) Unread() bool { return e.ReadAt.IsZero() }

// State is a mailbox's journal, folded. It is immutable once Replay returns it.
type State struct {
	entries map[string]Entry
	order   []string // message ids in delivery order
	next    int      // the next puid to assign
	skipped int      // events dropped from a truncated tail
}

// Len returns how many messages the mailbox has ever received.
func (st State) Len() int { return len(st.order) }

// NextPUID returns the identifier the next delivery will be given.
func (st State) NextPUID() int { return st.next }

// Skipped reports how many trailing journal bytes were unreadable and dropped.
// It is non-zero only after a process was killed mid-append.
func (st State) Skipped() int { return st.skipped }

// Entries returns every entry, in delivery order, excluding pruned ones.
func (st State) Entries() []Entry {
	out := make([]Entry, 0, len(st.order))
	for _, mid := range st.order {
		e := st.entries[mid]
		if e.Pruned {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Lookup finds one message's state.
func (st State) Lookup(id mail.ID) (Entry, bool) {
	e, ok := st.entries[id.String()]
	if !ok || e.Pruned {
		return Entry{}, false
	}
	return e, true
}

// Has reports whether the mailbox has ever been delivered this message,
// including after pruning. It is what stops a message being delivered twice.
func (st State) Has(id mail.ID) bool {
	_, ok := st.entries[id.String()]
	return ok
}

// Replay folds a user's journal into a State.
//
// The recovery rule is the reason for the whole append-only design. A process
// killed mid-append can only damage the *last* line, so an unparseable final
// line is dropped with a count. An unparseable line anywhere else is corruption
// rather than interruption, and is a hard error — silently skipping it would
// silently drop mail, which is the one thing this tool must not do.
func (s *Store) Replay(name user.Name) (State, error) {
	if name.Zero() {
		return State{}, fault.Internal{Where: "store.Replay", Detail: "no user given"}
	}
	path := s.journalPath(name)

	data, err := s.ops.readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{entries: map[string]Entry{}}, nil
		}
		return State{}, fault.IO{Op: "read", Path: path, Err: err}
	}
	return fold(path, data)
}

// fold replays journal bytes into a State.
//
// It is separate from Replay so that the recovery rules can be tested and
// fuzzed as the pure function they are: everything above this line is I/O, and
// everything below it is a fold over bytes. A fuzz target that had to build a
// store per iteration explored a few hundred inputs a second; this one explores
// hundreds of thousands.
func fold(path string, data []byte) (State, error) {
	if len(data) > MaxJournalSize {
		return State{}, fault.Parse{Path: path, Reason: fmt.Sprintf(
			"journal is %d bytes, limit is %d", len(data), MaxJournalSize)}
	}

	st := State{entries: make(map[string]Entry, 64)}

	// A trailing newline means the file ends on a complete line. Without one,
	// the final fragment is what an interrupted append left behind.
	complete := len(data) == 0 || data[len(data)-1] == '\n'
	lines := bytes.Split(data, []byte("\n"))
	if complete && len(lines) > 0 {
		lines = lines[:len(lines)-1] // drop the empty piece after the final newline
	}

	for i, raw := range lines {
		lineNo := i + 1
		last := i == len(lines)-1

		if len(raw) == 0 {
			if last && !complete {
				continue
			}
			return State{}, fault.Parse{Path: path, Line: lineNo, Reason: "empty journal line"}
		}
		if len(raw) > MaxJournalLine {
			return State{}, fault.Parse{Path: path, Line: lineNo, Reason: fmt.Sprintf(
				"journal line is %d bytes, limit is %d", len(raw), MaxJournalLine)}
		}

		ev, err := decodeEvent(path, lineNo, raw)
		if err != nil {
			if last && !complete {
				// The tail of an interrupted append. Dropped, but counted, so
				// `verify` and the commands can say it happened.
				st.skipped = len(raw)
				break
			}
			return State{}, err
		}
		if err := st.apply(path, lineNo, ev); err != nil {
			return State{}, err
		}
	}

	if err := st.validate(path); err != nil {
		return State{}, err
	}
	return st, nil
}

func decodeEvent(path string, line int, raw []byte) (event, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	var ev event
	if err := dec.Decode(&ev); err != nil {
		return event{}, fault.Parse{Path: path, Line: line, Reason: "journal event: " + err.Error()}
	}
	if dec.More() {
		return event{}, fault.Parse{Path: path, Line: line, Reason: "journal line has trailing content"}
	}
	if err := ev.validate(path, line); err != nil {
		return event{}, err
	}
	return ev, nil
}

// apply folds one event into the state.
func (st *State) apply(path string, line int, ev event) error {
	bad := func(format string, args ...any) error {
		return fault.Parse{Path: path, Line: line, Reason: fmt.Sprintf(format, args...)}
	}

	id, err := mail.ParseID(ev.MID)
	if err != nil {
		return bad("%s", err)
	}
	at, err := clock.Parse(ev.At)
	if err != nil {
		return bad("%s", err)
	}

	entry, seen := st.entries[ev.MID]

	switch ev.Op {
	case OpDeliver:
		if seen {
			return bad("message %s was delivered twice", ev.MID)
		}
		entry = Entry{MID: id, PUID: ev.PUID, Delivered: at}
		st.order = append(st.order, ev.MID)
		if ev.PUID >= st.next {
			st.next = ev.PUID + 1
		}

	case OpRead:
		if !seen {
			return bad("message %s was marked read before it was delivered", ev.MID)
		}
		// Re-reading is not an error: two commands may race, and the first
		// timestamp is the one that is true.
		if entry.ReadAt.IsZero() {
			entry.ReadAt = at
		}

	case OpArchive:
		if !seen {
			return bad("message %s was archived before it was delivered", ev.MID)
		}
		entry.Archived = true

	case OpPrune:
		if !seen {
			return bad("message %s was pruned before it was delivered", ev.MID)
		}
		if !entry.Archived {
			return bad("message %s was pruned without being archived", ev.MID)
		}
		entry.Pruned = true

	default:
		return bad("unknown journal operation %q", ev.Op)
	}

	st.entries[ev.MID] = entry
	return nil
}

// validate re-derives the invariants replay is expected to establish, so a
// defect in apply surfaces here rather than as a wrong inbox much later.
func (st State) validate(path string) error {
	const where = "store.State"
	if err := fault.Check(len(st.entries) == len(st.order), where,
		"%s: %d entries but %d in order", path, len(st.entries), len(st.order)); err != nil {
		return err
	}

	seen := make(map[int]string, len(st.entries))
	for _, mid := range st.order {
		e, ok := st.entries[mid]
		if err := fault.Check(ok, where, "%s: %s is ordered but has no entry", path, mid); err != nil {
			return err
		}
		if err := fault.Check(e.PUID < st.next, where,
			"%s: %s has puid %d but the next is %d", path, mid, e.PUID, st.next); err != nil {
			return err
		}
		// A puid must name exactly one message, forever. Two messages sharing one
		// is how `open id=4` opens the wrong mail.
		if other, dup := seen[e.PUID]; dup {
			return fault.Parse{Path: path, Reason: fmt.Sprintf(
				"puid %d belongs to both %s and %s; the journal is inconsistent", e.PUID, other, mid)}
		}
		seen[e.PUID] = mid
	}
	return nil
}

// Deliver records mail arriving in a mailbox and returns its new puid.
//
// The puid is chosen while the lock is held, from the journal being appended
// to, so two processes delivering at once cannot pick the same one. This is the
// only operation in Mailman that needs genuine mutual exclusion.
func (s *Store) Deliver(name user.Name, id mail.ID) (int, error) {
	if name.Zero() || id.Zero() {
		return 0, fault.Internal{Where: "store.Deliver", Detail: "user and message are both required"}
	}

	puid := -1
	err := s.withLock(func() error {
		st, err := s.Replay(name)
		if err != nil {
			return err
		}
		if st.Has(id) {
			// Already delivered. Reporting the existing puid rather than failing
			// makes delivery idempotent, which is what lets a partly-completed
			// send be retried without duplicating mail.
			entry := st.entries[id.String()]
			puid = entry.PUID
			return nil
		}

		puid = st.next
		return s.append(name, event{Op: OpDeliver, MID: id.String(), PUID: puid, At: clock.Format(s.clock.Now())})
	})
	if err != nil {
		return 0, err
	}
	if err := fault.Check(puid >= 0, "store.Deliver", "delivery produced puid %d", puid); err != nil {
		return 0, err
	}
	return puid, nil
}

// Mark records a state change for one message in one mailbox.
func (s *Store) Mark(name user.Name, id mail.ID, op Op) error {
	if name.Zero() || id.Zero() {
		return fault.Internal{Where: "store.Mark", Detail: "user and message are both required"}
	}
	if op == OpDeliver {
		return fault.Internal{Where: "store.Mark", Detail: "use Deliver to record a delivery"}
	}
	if !op.valid() {
		return fault.Internal{Where: "store.Mark", Detail: "unknown operation " + string(op)}
	}

	return s.withLock(func() error {
		st, err := s.Replay(name)
		if err != nil {
			return err
		}
		entry, ok := st.entries[id.String()]
		if !ok {
			return fault.NotFound{Target: id.String()}
		}

		// Re-applying a state the mailbox is already in is a no-op rather than an
		// error: two agents marking the same mail read is ordinary, not a fault.
		switch op {
		case OpRead:
			if !entry.ReadAt.IsZero() {
				return nil
			}
		case OpArchive:
			if entry.Archived {
				return nil
			}
		case OpPrune:
			if entry.Pruned {
				return nil
			}
			if !entry.Archived {
				return fault.Conflict{Path: id.String(), Reason: "message must be archived before it can be pruned"}
			}
		}

		return s.append(name, event{Op: op, MID: id.String(), At: clock.Format(s.clock.Now())})
	})
}

// append writes one event. It must be called with the lock held.
func (s *Store) append(name user.Name, ev event) error {
	if err := ev.validate("<new event>", 0); err != nil {
		return err
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return fault.Internal{Where: "store.append", Detail: err.Error()}
	}
	if bytes.ContainsAny(line, "\n\r") {
		return fault.Internal{Where: "store.append", Detail: "encoded event contains a newline"}
	}
	// Decoded back before it is written: an event that cannot be read is an
	// event that has been lost, and it is cheaper to find that out here.
	if _, err := decodeEvent("<new event>", 1, line); err != nil {
		return fault.Internal{Where: "store.append", Detail: "event does not decode back: " + err.Error()}
	}
	return s.appendLine(s.journalPath(name), line)
}

// PUIDs returns the mailbox's live entries indexed by puid, for `open id=…`.
func (st State) PUIDs() map[int]Entry {
	out := make(map[int]Entry, len(st.order))
	for _, e := range st.Entries() {
		out[e.PUID] = e
	}
	return out
}

// Order returns the message ids in delivery order.
func (st State) Order() []string { return slices.Clone(st.order) }

// PrunedIDs returns the messages this mailbox deliberately deleted.
//
// They are absent from Entries, but a conversation may still name them, and a
// consistency check has to be able to tell a deliberate deletion from a lost
// file.
func (st State) PrunedIDs() []mail.ID {
	var out []mail.ID
	for _, mid := range st.order {
		if e := st.entries[mid]; e.Pruned {
			out = append(out, e.MID)
		}
	}
	return out
}
