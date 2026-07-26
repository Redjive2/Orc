package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"orc/cq/internal/atomic"
	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
)

// The journal is append-only, and the reason is its failure mode: a process
// killed mid-append can only damage the last line. Replay drops an unparseable
// **final** line with a note and continues, because that is an interrupted
// write; an unparseable line anywhere else is corruption, and is a hard error.
// Silently skipping it would silently drop the record of something that
// happened to the user's mail.
//
// This is Mailman's and Macmuffin's discipline, unchanged, for the same reason.

// op names a journal event.
type op string

const (
	// opApplying is written *before* an action is attempted.
	opApplying op = "applying"
	// opApplied is written after, with the outcome.
	opApplied op = "applied"
	// opReported is written once the server has taken the results.
	opReported op = "reported"
)

// event is one line of the journal.
type event struct {
	Op      op                  `json:"op"`
	ID      protocol.ActionID   `json:"id,omitempty"`
	IDs     []protocol.ActionID `json:"ids,omitempty"`
	OK      bool                `json:"ok,omitempty"`
	Error   string              `json:"error,omitempty"`
	At      time.Time           `json:"at"`
	Machine protocol.MachineID  `json:"machine,omitempty"`
}

// Validate checks an event is one replay can act on.
func (e event) Validate() error {
	if e.At.IsZero() {
		return fault.Field("event", "at", "event carries no time")
	}
	switch e.Op {
	case opApplying:
		return e.ID.Validate()
	case opApplied:
		if err := e.ID.Validate(); err != nil {
			return err
		}
		if !e.OK && e.Error == "" {
			return fault.Field("event", "error", "a failed action carries no reason")
		}
		if e.OK && e.Error != "" {
			return fault.Field("event", "error", "a successful action carries an error")
		}
		return nil
	case opReported:
		if len(e.IDs) == 0 {
			return fault.Field("event", "ids", "a report names no actions")
		}
		for _, id := range e.IDs {
			if err := id.Validate(); err != nil {
				return err
			}
		}
		return nil
	default:
		return fault.Field("event", "op", "unknown event %q", string(e.Op))
	}
}

// outcome is what replay knows about one action.
type outcome struct {
	Result   protocol.Result
	Reported bool
	// InDoubt marks an action that was started and whose end was never
	// recorded. It may or may not have happened.
	InDoubt bool
}

// state is the journal replayed: every action cq has touched, and what it knows.
type state struct {
	outcomes map[protocol.ActionID]outcome
	// Truncated notes that the final line was an interrupted append.
	Truncated bool
}

// Applied reports whether an action has already been dealt with, so a
// re-delivered one is a no-op rather than a second send.
func (s state) Applied(id protocol.ActionID) (outcome, bool) {
	o, ok := s.outcomes[id]
	return o, ok
}

// Unreported lists the results the server has not yet taken, oldest first.
func (s state) Unreported() []protocol.Result {
	var out []protocol.Result
	for _, o := range s.outcomes {
		if !o.Reported {
			out = append(out, o.Result)
		}
	}
	slices.SortFunc(out, func(a, b protocol.Result) int {
		if c := a.At.Compare(b.At); c != 0 {
			return c
		}
		return strings.Compare(string(a.ActionID), string(b.ActionID))
	})
	return out
}

// journal is the agent's local record, under the state directory.
type journal struct {
	path string
}

func openJournal(root string) (*journal, error) {
	if root == "" {
		return nil, fault.Usage{Reason: "empty state path"}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fault.IO{Op: "resolve", Subject: root, Err: err}
	}
	if err := atomic.MkdirAll(abs, 0o700); err != nil {
		return nil, err
	}
	return &journal{path: filepath.Join(abs, "applied.jsonl")}, nil
}

// append writes one event and flushes it, so the record of an action reaching
// the world is itself on disk before the next one is attempted.
func (j *journal) append(e event) error {
	if err := e.Validate(); err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return fault.IO{Op: "encode", Subject: j.path, Err: err}
	}

	f, err := os.OpenFile(j.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fault.IO{Op: "open", Subject: j.path, Err: err}
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Write(append(line, '\n')); err != nil {
		return fault.IO{Op: "append to", Subject: j.path, Err: err}
	}
	if err := f.Sync(); err != nil {
		return fault.IO{Op: "flush", Subject: j.path, Err: err}
	}
	return nil
}

// replay folds the journal into what cq knows.
func (j *journal) replay() (state, error) {
	s := state{outcomes: map[protocol.ActionID]outcome{}}

	data, err := os.ReadFile(j.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return state{}, fault.IO{Op: "read", Subject: j.path, Err: err}
	}

	lines := splitLines(data)
	for i, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e event
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&e); err != nil || e.Validate() != nil {
			// Only the final line may be damaged, and only by an interrupted
			// append. Anything earlier is corruption.
			if i == len(lines)-1 {
				s.Truncated = true
				continue
			}
			reason := "line is not a usable journal event"
			if err != nil {
				reason = err.Error()
			}
			return state{}, fault.Parse{Where: j.path, Line: i + 1, Reason: reason}
		}

		switch e.Op {
		case opApplying:
			if _, done := s.outcomes[e.ID]; !done {
				s.outcomes[e.ID] = outcome{
					InDoubt: true,
					Result: protocol.Result{
						ActionID: e.ID, OK: false, At: e.At, InDoubt: true,
						Error: "interrupted before its outcome was known; it may or may not have been applied",
					},
				}
			}
		case opApplied:
			s.outcomes[e.ID] = outcome{
				Result: protocol.Result{ActionID: e.ID, OK: e.OK, Error: e.Error, At: e.At},
			}
		case opReported:
			for _, id := range e.IDs {
				if o, ok := s.outcomes[id]; ok {
					o.Reported = true
					s.outcomes[id] = o
				}
			}
		}
	}
	return s, nil
}

// splitLines splits on newlines without dropping a final unterminated line,
// which is exactly the shape an interrupted append leaves.
func splitLines(data []byte) [][]byte {
	if len(data) == 0 {
		return nil
	}
	out := bytes.Split(data, []byte("\n"))
	if len(out) > 0 && len(out[len(out)-1]) == 0 {
		out = out[:len(out)-1]
	}
	return out
}

// cursor records the last successful sync, for `cq status` and for a human
// wondering whether the mirror is running at all.
type cursor struct {
	LastSync  time.Time          `json:"last_sync"`
	LastError string             `json:"last_error,omitempty"`
	Machine   protocol.MachineID `json:"machine"`
	Server    string             `json:"server"`
}

func (j *journal) cursorPath() string { return filepath.Join(filepath.Dir(j.path), "cursor.json") }

func (j *journal) readCursor() (cursor, error) {
	var c cursor
	err := atomic.ReadJSON(j.cursorPath(), &c)
	if err != nil && fault.Classify(err) == fault.CodeNotFound {
		return cursor{}, nil
	}
	return c, err
}

func (j *journal) writeCursor(c cursor) error {
	return atomic.WriteJSON(j.cursorPath(), c, 0o600)
}

// prune drops reported results older than the horizon, so the journal does not
// grow forever. Anything unreported or in doubt is kept whatever its age: it is
// still a record of something the server has not been told about.
func (j *journal) prune(before time.Time) error {
	s, err := j.replay()
	if err != nil {
		return err
	}

	var keep []event
	for id, o := range s.outcomes {
		if o.Reported && o.Result.At.Before(before) {
			continue
		}
		keep = append(keep, event{
			Op: opApplied, ID: id, OK: o.Result.OK, Error: o.Result.Error, At: o.Result.At,
		})
	}
	slices.SortFunc(keep, func(a, b event) int {
		if c := a.At.Compare(b.At); c != 0 {
			return c
		}
		return strings.Compare(string(a.ID), string(b.ID))
	})

	var reported []protocol.ActionID
	for _, e := range keep {
		if o := s.outcomes[e.ID]; o.Reported {
			reported = append(reported, e.ID)
		}
	}

	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	for _, e := range keep {
		line, err := json.Marshal(e)
		if err != nil {
			return fault.IO{Op: "encode", Subject: j.path, Err: err}
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			return fault.IO{Op: "encode", Subject: j.path, Err: err}
		}
	}
	if len(reported) > 0 {
		line, err := json.Marshal(event{Op: opReported, IDs: reported, At: before})
		if err != nil {
			return fault.IO{Op: "encode", Subject: j.path, Err: err}
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			return fault.IO{Op: "encode", Subject: j.path, Err: err}
		}
	}
	if err := w.Flush(); err != nil {
		return fault.IO{Op: "encode", Subject: j.path, Err: err}
	}

	// Rewriting is the one operation that is not an append, so it goes through
	// the atomic commit: a reader sees the old journal or the new one.
	return atomic.WriteFile(j.path, buf.Bytes(), 0o600)
}
