package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/mailman/internal/mail"
)

// shard is the two-character directory a message lives under. A flat directory
// of tens of thousands of files is slow to list, and `inbox` lists on every
// invocation.
func shard(id mail.ID) string {
	s := id.String()
	if len(s) < 2 {
		return "00"
	}
	return s[:2]
}

func (s *Store) messagePath(id mail.ID) string {
	return filepath.Join(s.root, messagesDir, shard(id), id.String()+messageExt)
}

func (s *Store) receiptDir(id mail.ID) string {
	return filepath.Join(s.root, messagesDir, shard(id), id.String()+receiptExt)
}

func (s *Store) receiptPath(id mail.ID, name user.Name) string {
	return filepath.Join(s.receiptDir(id), name.String()+".json")
}

// Put stores a message. It is write-once: a message is never rewritten, and an
// attempt to store one twice is a conflict rather than an overwrite.
func (s *Store) Put(m mail.Message) error {
	data, err := mail.Encode(m)
	if err != nil {
		return err
	}
	if len(data) > MaxMessageSize {
		return fault.Usage{Reason: fmt.Sprintf("message is %d bytes, limit is %d", len(data), MaxMessageSize)}
	}
	return s.withLock(func() error { return s.writeNew(s.messagePath(m.ID()), data) })
}

// Replace overwrites a stored message.
//
// It exists for exactly one operation: `reply` roots a conversation on a
// message that was standalone when it was sent, so the root's own record has to
// learn the conversation it now belongs to. The guard is that the replacement
// must be the same message — same id, sender, recipients, subject, and body —
// differing only in its conversation binding. Anything else is refused, so this
// cannot become a general edit path by accident.
func (s *Store) Replace(m mail.Message) error {
	return s.withLock(func() error {
		old, err := s.Get(m.ID())
		if err != nil {
			return err
		}
		if err := sameExceptThread(old, m); err != nil {
			return err
		}
		data, err := mail.Encode(m)
		if err != nil {
			return err
		}
		return s.writeFile(s.messagePath(m.ID()), data)
	})
}

// sameExceptThread reports whether two messages differ only in their
// conversation binding.
func sameExceptThread(old, next mail.Message) error {
	deny := func(what string) error {
		return fault.Conflict{
			Path:   old.ID().String(),
			Reason: "a stored message may only gain a conversation; this would change its " + what,
		}
	}

	switch {
	case old.ID().String() != next.ID().String():
		return deny("identifier")
	case old.From().String() != next.From().String():
		return deny("sender")
	case old.Subject() != next.Subject():
		return deny("subject")
	case old.Kind() != next.Kind():
		return deny("kind")
	case !old.Sent().Equal(next.Sent()):
		return deny("send time")
	case !bytes.Equal(old.Body(), next.Body()):
		return deny("body")
	case strings.Join(user.Names(old.To()), ",") != strings.Join(user.Names(next.To()), ","):
		return deny("recipients")
	case strings.Join(user.Names(old.CC()), ",") != strings.Join(user.Names(next.CC()), ","):
		return deny("copied recipients")
	}

	if _, threaded := old.Convo(); threaded {
		return fault.Conflict{Path: old.ID().String(), Reason: "message already belongs to a conversation"}
	}
	if _, threaded := next.Convo(); !threaded {
		return fault.Conflict{Path: old.ID().String(), Reason: "replacement adds no conversation, so the write would do nothing"}
	}
	return nil
}

// Get loads a message.
func (s *Store) Get(id mail.ID) (mail.Message, error) {
	if id.Zero() {
		return mail.Message{}, fault.Internal{Where: "store.Get", Detail: "no message id given"}
	}
	path := s.messagePath(id)

	data, err := s.readFile(path)
	if err != nil {
		return mail.Message{}, err
	}
	if len(data) > MaxMessageSize {
		return mail.Message{}, fault.Parse{Path: path, Reason: fmt.Sprintf(
			"message file is %d bytes, limit is %d", len(data), MaxMessageSize)}
	}

	m, err := mail.Decode(path, data)
	if err != nil {
		return mail.Message{}, err
	}
	// The file name states an identity and so does the content. A disagreement
	// means the store was hand-edited or a file was copied, and either way the
	// content must not be trusted to be about the message that was asked for.
	if m.ID().String() != id.String() {
		return mail.Message{}, fault.Conflict{Path: path, Reason: fmt.Sprintf(
			"file is named for %s but contains %s", id, m.ID())}
	}
	return m, nil
}

// Delete removes a message and its receipts.
//
// Only `prune` reaches this, and only after the prune has been journaled, so a
// crash midway leaves a message that nothing references rather than a reference
// to a message that is gone.
func (s *Store) Delete(id mail.ID) error {
	if id.Zero() {
		return fault.Internal{Where: "store.Delete", Detail: "no message id given"}
	}
	return s.withLock(func() error {
		if err := s.ops.removeAll(s.receiptDir(id)); err != nil && !os.IsNotExist(err) {
			return fault.IO{Op: "remove the receipts for", Path: id.String(), Err: err}
		}
		if err := s.ops.remove(s.messagePath(id)); err != nil && !os.IsNotExist(err) {
			return fault.IO{Op: "remove", Path: s.messagePath(id), Err: err}
		}
		return nil
	})
}

// Receipt records that one user read one message, and when.
type Receipt struct {
	User user.Name
	At   time.Time
}

// storedReceipt is the on-disk shape.
type storedReceipt struct {
	Version int    `json:"version"`
	User    string `json:"user"`
	At      string `json:"at"`
}

// PutReceipt records a read.
//
// Receipts live beside the message rather than in the reader's journal because
// `check` has to read *other* users' state, and this makes that a directory
// listing instead of a scan of every mailbox in the store. Each user writes
// only their own file, so two recipients marking the same message read never
// contend — which is why this one does not take the lock.
//
// The first receipt wins: a second read does not move the timestamp, because
// the moment someone first saw a message is the fact worth keeping.
func (s *Store) PutReceipt(id mail.ID, name user.Name, at time.Time) error {
	if id.Zero() || name.Zero() {
		return fault.Internal{Where: "store.PutReceipt", Detail: "message and user are both required"}
	}

	path := s.receiptPath(id, name)
	if _, err := s.ops.stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fault.IO{Op: "check for", Path: path, Err: err}
	}

	data, err := json.MarshalIndent(storedReceipt{
		Version: Version,
		User:    name.String(),
		At:      clock.Format(at),
	}, "", "  ")
	if err != nil {
		return fault.Internal{Where: "store.PutReceipt", Detail: err.Error()}
	}
	return s.writeFile(path, append(data, '\n'))
}

// Receipts lists who has read a message, in name order.
//
// A receipt file that cannot be read is reported rather than skipped: `check`
// exists to answer "who has seen this", and an answer that quietly omits
// someone is worse than an error.
func (s *Store) Receipts(id mail.ID) ([]Receipt, error) {
	if id.Zero() {
		return nil, fault.Internal{Where: "store.Receipts", Detail: "no message id given"}
	}
	dir := s.receiptDir(id)

	entries, err := s.ops.readDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fault.IO{Op: "list", Path: dir, Err: err}
	}

	out := make([]Receipt, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := s.readFile(path)
		if err != nil {
			return nil, err
		}

		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		var sr storedReceipt
		if err := dec.Decode(&sr); err != nil {
			return nil, fault.Parse{Path: path, Reason: "read receipt: " + err.Error()}
		}
		if dec.More() {
			return nil, fault.Parse{Path: path, Reason: "read receipt has trailing content"}
		}
		if sr.Version != Version {
			return nil, fault.Parse{Path: path, Reason: fmt.Sprintf(
				"read receipt is version %d, this mailman writes version %d", sr.Version, Version)}
		}

		name, err := user.Parse(sr.User)
		if err != nil {
			return nil, fault.Parse{Path: path, Reason: "read receipt names a bad user: " + err.Error()}
		}
		// The file is named for a user and the content names one too; a
		// disagreement means a receipt was copied between mailboxes.
		if want := strings.TrimSuffix(e.Name(), ".json"); name.String() != want {
			return nil, fault.Conflict{Path: path, Reason: fmt.Sprintf(
				"receipt file is named for %s but records %s", want, name)}
		}
		at, err := clock.Parse(sr.At)
		if err != nil {
			return nil, fault.Parse{Path: path, Reason: "read receipt: " + err.Error()}
		}

		out = append(out, Receipt{User: name, At: at})
	}

	slices.SortFunc(out, func(a, b Receipt) int { return a.User.Compare(b.User) })
	return out, nil
}

// HasMessage reports whether a message file exists.
func (s *Store) HasMessage(id mail.ID) (bool, error) {
	if id.Zero() {
		return false, fault.Internal{Where: "store.HasMessage", Detail: "no message id given"}
	}
	_, err := s.ops.stat(s.messagePath(id))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fault.IO{Op: "check for", Path: s.messagePath(id), Err: err}
}
