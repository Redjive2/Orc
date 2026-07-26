package event

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"orc/common/clock"
	"orc/common/fault"
)

// Package event is the structured feed `orc attach` renders.
//
// A pty carrying a TUI is a stream of screen redraws, not a stream of facts, so the
// clean view in Plan.md §6.2 is not built by parsing it. It is built from this: one
// line per hook firing, written by `orc-hook` inside the session's own process.
//
// The schema is fixed **here**, in a leaf package that imports nothing of Orc's, so
// the writer (internal/hook, through internal/store) and the reader (internal/view)
// can be built at the same time without either waiting for the other — and without
// the import cycle that putting it in internal/session created. A hand-written events.jsonl is a valid
// input, which is what lets the view be developed against a fixture.
//
// The journal discipline is the tree's usual one, and it matters more here than
// elsewhere because a hook writes on every tool call in a live session: a truncated
// final line is an interrupted append and is dropped, and anything else that will
// not parse is corruption.

// EventFile is the feed's name inside a session directory.
const EventFile = "events.jsonl"

// MaxEventLine bounds one event. A tool input can be large — a whole file in a
// Write — so the *path* is recorded and the content never is.
const MaxEventLine = 8 << 10

// Verdict is what a PreToolUse hook decided.
type Verdict string

// The verdicts. Empty means the event was not a decision.
const (
	VerdictAllow Verdict = "allow"
	VerdictBlock Verdict = "block"
)

// Event is one line of the feed.
//
// The field set is deliberately small: what happened, to what, and what Orc decided.
// Anything a reader wants beyond this is in Claude's own transcript, which Transcript
// points at.
type Event struct {
	// At is when it happened, in the tree's format.
	At string `json:"at"`
	// Session is the Claude session id, so a feed read after a refresh can tell the
	// old conversation from the new one.
	Session string `json:"session"`
	// Name is Claude's own hook_event_name, verbatim: PreToolUse, PostToolUse,
	// UserPromptSubmit, Notification, Stop, SubagentStop, SessionStart, SessionEnd.
	// It is not translated, because a name Orc invented would be one more thing to
	// map when Claude adds an event.
	Name string `json:"event"`
	// Tool is the tool being used, empty for lifecycle events.
	Tool string `json:"tool,omitempty"`
	// Path is the file a tool is acting on, empty when it is not acting on one. It
	// is the *path* and never the content: a feed that carried the text of every
	// edit would be a second copy of the repository.
	Path string `json:"path,omitempty"`
	// Turn counts UserPromptSubmit events, so the view can show which turn a tool
	// call belongs to.
	Turn int `json:"turn,omitempty"`
	// Verdict and Reason are set on PreToolUse only.
	Verdict Verdict `json:"verdict,omitempty"`
	Reason  string  `json:"reason,omitempty"`
	// Transcript is where Claude's own JSONL for this session lives. It appears on
	// the **first** event of a session and is how the view finds the transcript
	// without knowing anything about how the path is derived — the hook is told it,
	// so Orc never has to guess.
	Transcript string `json:"transcript,omitempty"`
}

// Encode renders an event for the feed, checking it decodes back before it is
// written: an event that cannot be read is an event that has been lost.
func (e Event) Encode() ([]byte, error) {
	if e.At == "" {
		return nil, fault.Internal{Where: "event.Event.Encode", Detail: "event has no timestamp"}
	}
	if e.Name == "" {
		return nil, fault.Internal{Where: "event.Event.Encode", Detail: "event has no name"}
	}

	line, err := json.Marshal(e)
	if err != nil {
		return nil, fault.Internal{Where: "event.Event.Encode", Detail: err.Error()}
	}
	if bytes.ContainsAny(line, "\n\r") {
		return nil, fault.Internal{Where: "event.Event.Encode", Detail: "encoded event contains a newline"}
	}
	if len(line)+1 > MaxEventLine {
		return nil, fault.Internal{Where: "event.Event.Encode", Detail: fmt.Sprintf(
			"event is %d bytes, over the %d limit", len(line), MaxEventLine)}
	}
	if _, err := DecodeEvent(line); err != nil {
		return nil, fault.Internal{Where: "event.Event.Encode",
			Detail: "event does not decode back: " + err.Error()}
	}
	return line, nil
}

// DecodeEvent reads one line of the feed.
//
// Unknown fields are refused rather than ignored, as everywhere else in this tree: a
// field this build does not understand means a newer Orc wrote the feed, and a view
// that quietly dropped it would show a session doing less than it was.
func DecodeEvent(raw []byte) (Event, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	var e Event
	if err := dec.Decode(&e); err != nil {
		return Event{}, fault.Parse{Path: EventFile, Reason: "session event: " + err.Error()}
	}
	if dec.More() {
		return Event{}, fault.Parse{Path: EventFile, Reason: "session event has trailing content"}
	}
	if _, err := clock.Parse(e.At); err != nil {
		return Event{}, fault.Parse{Path: EventFile, Reason: "session event timestamp: " + err.Error()}
	}
	if strings.TrimSpace(e.Name) == "" {
		return Event{}, fault.Parse{Path: EventFile, Reason: "session event has no name"}
	}
	return e, nil
}

// DecodeEvents reads a whole feed, dropping an interrupted final line.
//
// It reports how many bytes were dropped, which is the same contract every journal
// reader in this tree has: `orc verify` cares, and a view does not.
func DecodeEvents(data []byte) (events []Event, skipped int, err error) {
	complete := len(data) == 0 || data[len(data)-1] == '\n'
	lines := bytes.Split(data, []byte("\n"))
	if complete && len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}

	for i, raw := range lines {
		last := i == len(lines)-1
		if len(raw) == 0 {
			if last && !complete {
				continue
			}
			return nil, 0, fault.Parse{Path: EventFile, Line: i + 1, Reason: "empty event line"}
		}
		e, err := DecodeEvent(raw)
		if err != nil {
			if last && !complete {
				return events, len(raw), nil
			}
			return nil, 0, err
		}
		events = append(events, e)
	}
	return events, 0, nil
}

// Append adds one event to a feed.
//
// It takes a path rather than a store, and that is deliberate. `orc-hook` opens the
// store **read-only** — it is a bystander in somebody's session and must not be able
// to touch fleet state — and a read-only store that could still write one kind of
// file would be a guarantee with an exception in it. So the feed is appended to
// directly, here, by the one process that writes it.
//
// Three properties, each of which the hook depends on:
//
//   - **No lock.** A hook fires on every tool call, and a lock in that path is a lock
//     in the path of every edit. O_APPEND on a line under the pipe buffer is atomic
//     enough for a feed nobody makes decisions from.
//   - **Bounded.** An over-long event is refused by Encode rather than truncated: a
//     half-written line would be indistinguishable from an interrupted append.
//   - **Never fatal.** The caller drops the error. A feed that could not be written is
//     a view with a gap in it, and a tool call failing because its *logging* failed
//     would be the hook doing exactly what a bystander must not.
func Append(path string, ev Event) error {
	line, err := ev.Encode()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fault.IO{Op: "create the directory for", Path: path, Err: err}
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fault.IO{Op: "open for appending", Path: path, Err: err}
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Write(append(line, '\n')); err != nil {
		return fault.IO{Op: "append to", Path: path, Err: err}
	}
	return nil
}

// Read reads a whole feed from a file, dropping an interrupted final line.
//
// A feed that is not there yet is not an error: a session that has not fired a hook
// has no feed, which is an empty history rather than a fault.
func Read(path string) (events []Event, skipped int, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, fault.IO{Op: "read", Path: path, Err: err}
	}
	if len(data) > MaxFeed {
		// A feed this size is a session that has been running for a very long time,
		// or something appending in a loop. The tail is what a view wants anyway.
		data = data[len(data)-MaxFeed:]
		if cut := bytes.IndexByte(data, '\n'); cut >= 0 {
			data = data[cut+1:]
		}
	}
	return DecodeEvents(data)
}

// MaxFeed bounds how much of a feed is read at once. Past this, the tail is taken:
// the newest events are the ones a view is drawing, and a session that has produced
// more than this has produced more than anybody will scroll through.
const MaxFeed = 8 << 20
