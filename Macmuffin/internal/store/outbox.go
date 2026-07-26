package store

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
)

// Bounds on the outbox.
const (
	// MaxAttempts is how many times a notice is retried before it stops being
	// retried and starts being reported. A retry loop that never gives up is a
	// retry loop that eventually hides the actual problem.
	MaxAttempts = 10

	// MaxOutbox bounds the queue. Past this, something is wrong with delivery
	// rather than with any one notice.
	MaxOutbox = 4096

	// MaxNoticeSize bounds one queued notice.
	MaxNoticeSize = 64 << 10
)

// notice is the on-disk shape of a queued notification.
type notice struct {
	Version  int      `json:"version"`
	ID       string   `json:"id"`
	To       []string `json:"to"`
	Subject  string   `json:"subject"`
	Body     string   `json:"body"`
	Attempts int      `json:"attempts"`
	Queued   string   `json:"queued"`
	LastErr  string   `json:"last_error,omitempty"`
}

// Notice is a notification waiting to be sent.
type Notice struct {
	ID       string
	To       []user.Name
	Subject  string
	Body     string
	Attempts int
	Queued   time.Time
	LastErr  string
}

// Exhausted reports whether the notice has stopped being retried.
func (n Notice) Exhausted() bool { return n.Attempts >= MaxAttempts }

func (s *Store) noticePath(id string) string {
	return filepath.Join(s.root, outboxDir, id+".json")
}

// Queue records a notification that ought to be sent.
//
// It is written *before* delivery is attempted, and that order is the whole
// design: a Mailman that is missing, misconfigured, or momentarily broken then
// delays a notification rather than losing one. The membership change it
// announces has already happened and is not conditional on the mail arriving.
func (s *Store) Queue(to []user.Name, subject, body string) (Notice, error) {
	if len(to) == 0 {
		return Notice{}, fault.Internal{Where: "store.Queue", Detail: "a notice needs a recipient"}
	}
	if strings.TrimSpace(subject) == "" {
		return Notice{}, fault.Internal{Where: "store.Queue", Detail: "a notice needs a subject"}
	}

	pending, err := s.Pending()
	if err != nil {
		return Notice{}, err
	}
	if len(pending) >= MaxOutbox {
		return Notice{}, fault.Conflict{Path: filepath.Join(s.root, outboxDir), Reason: fmt.Sprintf(
			"the outbox holds %d undelivered notices; delivery is broken, not this one", len(pending))}
	}

	id, err := newNoticeID()
	if err != nil {
		return Notice{}, err
	}
	got := Notice{ID: id, To: slices.Clone(to), Subject: subject, Body: body, Queued: s.clock.Now()}
	if err := s.writeNotice(got); err != nil {
		return Notice{}, err
	}
	return got, nil
}

// newNoticeID mints an identifier unique across processes without coordination.
func newNoticeID() (string, error) {
	var raw [12]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
		return "", fault.IO{Op: "read entropy for", Path: "notice id", Err: err}
	}
	return hex.EncodeToString(raw[:]), nil
}

func (s *Store) writeNotice(n Notice) error {
	data, err := json.MarshalIndent(notice{
		Version:  Version,
		ID:       n.ID,
		To:       user.Names(n.To),
		Subject:  n.Subject,
		Body:     n.Body,
		Attempts: n.Attempts,
		Queued:   clock.Format(n.Queued),
		LastErr:  n.LastErr,
	}, "", "  ")
	if err != nil {
		return fault.Internal{Where: "store.writeNotice", Detail: err.Error()}
	}
	if len(data) > MaxNoticeSize {
		return fault.Usage{Reason: fmt.Sprintf("notice is %d bytes, limit is %d", len(data), MaxNoticeSize)}
	}
	return s.writeFile(s.noticePath(n.ID), append(data, '\n'))
}

// Pending lists undelivered notices, oldest first.
//
// A notice that cannot be decoded is skipped rather than failing the listing:
// the outbox is drained opportunistically by every command, and one damaged
// entry must not stop the rest from being delivered. `verify` is what reports
// it.
func (s *Store) Pending() ([]Notice, error) {
	dir := filepath.Join(s.root, outboxDir)

	entries, err := s.ops.readDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fault.IO{Op: "list", Path: dir, Err: err}
	}

	var out []Notice
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		got, err := s.readNotice(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out = append(out, got)
	}
	slices.SortFunc(out, func(a, b Notice) int {
		if c := a.Queued.Compare(b.Queued); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

// Damaged lists outbox entries that could not be decoded, so `verify` can name
// them rather than leaving them to accumulate unseen.
func (s *Store) Damaged() ([]string, error) {
	dir := filepath.Join(s.root, outboxDir)

	entries, err := s.ops.readDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fault.IO{Op: "list", Path: dir, Err: err}
	}

	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if _, err := s.readNotice(strings.TrimSuffix(e.Name(), ".json")); err != nil {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

func (s *Store) readNotice(id string) (Notice, error) {
	path := s.noticePath(id)

	data, err := s.ops.readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Notice{}, fault.NotFound{Target: id}
		}
		return Notice{}, fault.IO{Op: "read", Path: path, Err: err}
	}
	if len(data) > MaxNoticeSize {
		return Notice{}, fault.Parse{Path: path, Reason: fmt.Sprintf(
			"notice is %d bytes, limit is %d", len(data), MaxNoticeSize)}
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var n notice
	if err := dec.Decode(&n); err != nil {
		return Notice{}, fault.Parse{Path: path, Reason: "notice: " + err.Error()}
	}
	if dec.More() {
		return Notice{}, fault.Parse{Path: path, Reason: "notice has trailing content"}
	}
	if n.Version != Version {
		return Notice{}, fault.Parse{Path: path, Reason: fmt.Sprintf(
			"notice is version %d, this macmuffin writes version %d", n.Version, Version)}
	}
	if n.ID != id {
		return Notice{}, fault.Conflict{Path: path, Reason: fmt.Sprintf(
			"notice is filed as %s but calls itself %s", id, n.ID)}
	}

	to, err := user.ParseList(n.To)
	if err != nil {
		return Notice{}, fault.Parse{Path: path, Reason: "notice recipients: " + err.Error()}
	}
	if len(to) == 0 {
		return Notice{}, fault.Parse{Path: path, Reason: "notice has no recipients"}
	}
	queued, err := clock.Parse(n.Queued)
	if err != nil {
		return Notice{}, fault.Parse{Path: path, Reason: "notice queued: " + err.Error()}
	}
	if n.Attempts < 0 {
		return Notice{}, fault.Parse{Path: path, Reason: "notice has a negative attempt count"}
	}

	return Notice{
		ID: n.ID, To: to, Subject: n.Subject, Body: n.Body,
		Attempts: n.Attempts, Queued: queued, LastErr: n.LastErr,
	}, nil
}

// Delivered removes a notice that was sent.
func (s *Store) Delivered(id string) error {
	if strings.TrimSpace(id) == "" {
		return fault.Internal{Where: "store.Delivered", Detail: "no notice named"}
	}
	if err := s.ops.remove(s.noticePath(id)); err != nil && !os.IsNotExist(err) {
		return fault.IO{Op: "remove the notice", Path: s.noticePath(id), Err: err}
	}
	return nil
}

// Undelivered records a failed attempt, so the count can reach the point where
// retrying stops and reporting starts.
func (s *Store) Undelivered(n Notice, cause error) error {
	n.Attempts++
	if cause != nil {
		n.LastErr = cause.Error()
	}
	return s.writeNotice(n)
}
