// Package fixture holds the corpus every golden test draws against.
//
// It is one place so that a change to the rendered layout breaks exactly one
// constant, the way Anno's fixture holds the worked example from its own
// documentation. The mailbox below is deliberately awkward: a threaded message
// and a standalone one, a cc notice, a read message and unread ones, a long
// subject that must truncate, and a CJK subject that must not shear the table.
package fixture

import (
	"encoding/binary"
	"fmt"
	"time"

	"orc/common/user"
	"orc/mailman/internal/mail"
	"orc/mailman/internal/store"
	"orc/mailman/internal/view"
)

// Epoch is the instant the corpus is built around. It is fixed so every
// rendered timestamp is a constant.
var Epoch = time.Date(2026, 7, 24, 18, 31, 4, 512_000_000, time.UTC)

// Entropy is a deterministic byte source, so identifiers in golden output are
// stable across runs.
type Entropy struct{ n uint64 }

func (e *Entropy) Read(p []byte) (int, error) {
	for i := 0; i < len(p); i += 8 {
		e.n++
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], e.n)
		copy(p[i:], buf[:])
	}
	return len(p), nil
}

// Spec describes one message in the corpus.
type Spec struct {
	PUID     int
	Kind     mail.Kind
	From     string
	To       []string
	CC       []string
	Subject  string
	Body     string
	Offset   time.Duration
	Convo    bool
	Index    int
	Title    string
	Read     bool
	Archived bool
}

// Corpus is the mailbox the golden tests render. Owner is alice.
var Corpus = []Spec{
	{
		PUID: 0, From: "boss", To: []string{"alice", "carol"},
		Subject: "RE: work", Body: "Ship it by Friday.\n",
		Convo: true, Index: 1, Title: "work",
		Read: true,
	},
	{
		PUID: 1, From: "carol", To: []string{"alice"},
		Subject: "deploy notes", Body: "# Notes\n\n- staging is green\n",
		Offset: time.Hour,
	},
	{
		PUID: 2, Kind: mail.Notice, From: "boss", To: []string{"alice"},
		Subject: "cc: dave added to work", Body: "boss added dave to this conversation.\n",
		Convo: true, Index: 2, Title: "work",
		Offset: 2 * time.Hour,
	},
	{
		PUID: 3, From: "boss", To: []string{"alice"},
		Subject: "a subject long enough that it has to be truncated in a narrow column",
		Body:    "…\n", Offset: 3 * time.Hour,
	},
	{
		PUID: 4, From: "carol", To: []string{"alice"},
		Subject: "日本語の件について", Body: "本文\n", Offset: 4 * time.Hour,
	},
	{
		PUID: 5, From: "boss", To: []string{"alice"},
		Subject: "old business", Body: "archived\n",
		Offset: 5 * time.Hour,
		Read:   true, Archived: true,
	},
}

// Owner is whose mailbox the corpus belongs to.
const Owner = "alice"

// Rows builds the corpus as renderable rows.
//
// It constructs values directly rather than through a store, because the golden
// tests are about drawing and should not fail because something changed about
// how mail is filed.
func Rows() ([]view.Row, error) {
	entropy := &Entropy{}
	var rows []view.Row

	// The conversation's identifier is its root message's, so the root is built
	// first and its identifier reused.
	var convo mail.ID

	for _, spec := range Corpus {
		at := Epoch.Add(spec.Offset)

		id, err := mail.NewID(at, entropy)
		if err != nil {
			return nil, err
		}
		from, err := user.Parse(spec.From)
		if err != nil {
			return nil, err
		}
		to, err := user.ParseList(spec.To)
		if err != nil {
			return nil, err
		}
		cc, err := user.ParseList(spec.CC)
		if err != nil {
			return nil, err
		}

		thread := mail.ID{}
		if spec.Convo {
			if convo.Zero() {
				convo = id
			}
			thread = convo
		}

		msg, err := mail.New(id, spec.Kind, from, to, cc, spec.Subject, thread, spec.Index, at, []byte(spec.Body))
		if err != nil {
			return nil, fmt.Errorf("building %q: %w", spec.Subject, err)
		}

		entry := store.Entry{MID: id, PUID: spec.PUID, Delivered: at, Archived: spec.Archived}
		if spec.Read {
			entry.ReadAt = at.Add(time.Minute)
		}

		rows = append(rows, view.Row{
			Entry:   entry,
			Message: msg,
			Title:   spec.Title,
		})
	}
	return rows, nil
}

// Unarchived returns the rows an inbox listing would show.
func Unarchived(rows []view.Row) []view.Row {
	var out []view.Row
	for _, r := range rows {
		if !r.Archived() {
			out = append(out, r)
		}
	}
	return out
}
