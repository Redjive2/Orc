package view

import (
	"cmp"
	"slices"
	"time"

	"orc/common/fault"
	"orc/common/user"
	"orc/mailman/internal/mail"
	"orc/mailman/internal/store"
)

// Holder is one mailbox a message sits in, and what that reader has done with
// it. The same message appears in as many holders as it was addressed to.
type Holder struct {
	User     user.Name
	PUID     int
	Read     bool
	Archived bool
	// Mine reports the sender's own copy, which exists so `check` can answer
	// who has read what they sent. It is not a delivery.
	Mine bool
}

// Seen is one recipient's read receipt.
type Seen struct {
	User user.Name
	At   time.Time
}

// Whole is one message as the store holds it: the message itself, every mailbox
// it is in, and every receipt against it.
type Whole struct {
	Message  mail.Message
	Title    string
	Holders  []Holder
	Receipts []Seen
}

// WholeStore builds the admin view: every message, once, with the
// mailboxes that hold it and the receipts against it.
//
// It is assembled from each account's own mailbox rather than by walking the
// message directory, for two reasons. The per-user journals are the record of
// who actually holds what — a file under `messages/` says nothing about whose
// mailbox it is in — and replaying them reuses the same validated read path
// every other command uses, rather than a second one that could disagree with
// it.
//
// A mailbox that cannot be read is reported as damage rather than failing the
// whole view: an admin panel that shows nine accounts and names the tenth as
// unreadable is more useful than one that shows nothing.
func WholeStore(s *store.Store) ([]Whole, []Damage, error) {
	if s == nil {
		return nil, nil, fault.Internal{Where: "view.WholeStore", Detail: "no store given"}
	}

	names, err := s.Users()
	if err != nil {
		return nil, nil, err
	}

	var damaged []Damage
	byID := map[string]*Whole{}
	var order []string

	for _, name := range names {
		box, err := Load(s, name)
		if err != nil {
			damaged = append(damaged, Damage{Err: err})
			continue
		}
		damaged = append(damaged, box.Damaged()...)

		for _, row := range box.Rows() {
			// Thread history a reader can see but does not hold is not a
			// mailbox entry, and counting it as one would report mail as
			// delivered to people it was never sent to.
			if !row.Filed {
				continue
			}
			id := row.Message.ID().String()
			whole, ok := byID[id]
			if !ok {
				order = append(order, id)
				byID[id] = &Whole{Message: row.Message, Title: row.Title}
				whole = byID[id]
			}
			if whole.Title == "" {
				whole.Title = row.Title
			}
			whole.Holders = append(whole.Holders, Holder{
				User: name, PUID: row.PUID(),
				Read: !row.Unread(), Archived: row.Archived(), Mine: row.Mine(name),
			})
		}
	}

	out := make([]Whole, 0, len(order))
	for _, id := range order {
		whole := byID[id]

		receipts, err := s.Receipts(whole.Message.ID())
		if err != nil {
			// The message is still worth showing; only its receipts are lost.
			damaged = append(damaged, Damage{Err: err})
		}
		for _, r := range receipts {
			whole.Receipts = append(whole.Receipts, Seen{User: r.User, At: r.At})
		}

		slices.SortFunc(whole.Holders, func(a, b Holder) int {
			return cmp.Compare(a.User.String(), b.User.String())
		})
		slices.SortFunc(whole.Receipts, func(a, b Seen) int {
			return cmp.Compare(a.User.String(), b.User.String())
		})
		out = append(out, *whole)
	}

	// Newest first, the way every other listing in Mailman reads.
	slices.SortStableFunc(out, func(a, b Whole) int {
		return b.Message.Sent().Compare(a.Message.Sent())
	})
	return out, damaged, nil
}
