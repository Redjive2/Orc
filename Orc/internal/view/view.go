// Package view builds the clean `attach` screen from Orc's own records.
//
// A pty carrying a TUI is a stream of screen redraws, not a stream of facts, so this
// is not built by parsing one. It is a fold over `session/events.jsonl` — one line
// per hook firing, written inside the session's own process — with Claude's
// transcript as the source of prose where it can be read.
//
// Everything here is pure. The model is a function of the bytes of a feed, which is
// what lets the whole screen be built and tested against a hand-written fixture,
// with no session running, no pty, and no terminal.
//
// Two properties from Plan.md §6.2 are load-bearing and are why the package is shaped
// this way:
//
//   - **It is legible when the session is not.** A wedged TUI or a mid-compact
//     session still has a feed, and the feed still says what happened last.
//   - **It never touches the child.** Nothing in here writes anything, dials
//     anything, or can block on the session it describes.
package view

import (
	"os"
	"strings"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/event"
)

// MaxRows bounds what a fold keeps.
//
// A session that runs for a day produces thousands of events and a pane shows a
// dozen. Keeping them all would mean a screen redraw walking an ever-growing slice
// while somebody watches four agents at once; the oldest are dropped, and Dropped
// says so rather than the screen quietly implying the session did less than it did.
const MaxRows = 512

// Kind is what a row is, which is how it is drawn and which glyph it wears.
//
// It is derived from Claude's event names rather than replacing them: the feed keeps
// `hook_event_name` verbatim, and this is the view's own reading of it, so an event
// Claude adds tomorrow becomes an Unknown row rather than a parse failure.
type Kind int

const (
	// Unknown is an event this build has no opinion about. It is still shown —
	// a session doing something unrecognised is exactly what an operator wants
	// to see.
	Unknown Kind = iota
	// Prompt is a turn boundary: the operator, or a poke, said something.
	Prompt
	// Action is a tool call.
	Action
	// Waiting is the session asking for input, or having stopped.
	Waiting
	// Lifecycle is a session starting or ending.
	Lifecycle
)

// Row is one line of the feed as the pane shows it.
type Row struct {
	// At is when it happened. The zero time means the event carried none, which
	// DecodeEvent will not allow, so it never happens in practice.
	At time.Time
	// Turn is the turn the row belongs to.
	Turn int
	// Kind is how to draw it.
	Kind Kind
	// Tool is the tool name, empty for lifecycle rows.
	Tool string
	// Detail is the path, the message, or whatever the row is about.
	Detail string
	// Verdict is set on decisions only.
	Verdict event.Verdict
	// Reason is why a decision went the way it did. It is shown on its own line
	// under a block, because "denied" without the reason sends the reader to the
	// permissions table to find out what they already needed to know.
	Reason string
}

// Blocked reports whether the row is a refusal.
func (r Row) Blocked() bool { return r.Verdict == event.VerdictBlock }

// Session is the whole model the pane draws.
type Session struct {
	// Identity is whose session this is.
	Identity user.Name
	// ID is the Claude session id, so a view held across a refresh can tell that
	// the conversation underneath it was replaced.
	ID string
	// Turn is the current turn number.
	Turn int
	// Rows are the events, oldest first.
	Rows []Row
	// Waiting reports whether the session is waiting for input. It is the single
	// most useful fact on the screen for somebody watching four agents, so it is
	// derived rather than read: the last event decides it.
	Waiting bool
	// Transcript is where Claude's own JSONL lives, if the feed said.
	Transcript string
	// Dropped counts rows discarded for being older than MaxRows.
	Dropped int
	// Skipped is bytes left by an interrupted append at the end of the feed.
	// A hook writes on every tool call, so a torn final line is ordinary and is
	// reported rather than treated as damage.
	Skipped int
}

// Live reports whether there is anything to show.
func (s Session) Live() bool { return len(s.Rows) > 0 }

// Last returns the most recent row, and whether there was one.
func (s Session) Last() (Row, bool) {
	if len(s.Rows) == 0 {
		return Row{}, false
	}
	return s.Rows[len(s.Rows)-1], true
}

// Fold builds the model from a decoded feed.
//
// Order is the file's order. The feed is append-only and single-writer — one hook
// process per tool call, inside one session — so the file *is* the sequence, and
// sorting by timestamp would only introduce a way for two events in the same
// millisecond to swap.
func Fold(name user.Name, events []event.Event, skipped int) (Session, error) {
	if name.Zero() {
		return Session{}, fault.Internal{Where: "view.Fold", Detail: "no identity given"}
	}

	got := Session{Identity: name, Skipped: skipped}
	for _, e := range events {
		at, err := clock.Parse(e.At)
		if err != nil {
			// DecodeEvent already checked this, so reaching here means a caller
			// built events by hand. Refusing is better than drawing a row at the
			// zero time, which would sort and read as 1 January year one.
			return Session{}, fault.Parse{Path: event.EventFile, Reason: "event timestamp: " + err.Error()}
		}

		if e.Session != "" {
			got.ID = e.Session
		}
		if e.Transcript != "" {
			got.Transcript = e.Transcript
		}
		if e.Name == "UserPromptSubmit" {
			got.Turn++
		}
		if e.Turn > got.Turn {
			// The writer counts turns too, and it is the one inside the session.
			// Where they disagree the feed wins.
			got.Turn = e.Turn
		}

		row, keep := rowOf(e, at, got.Turn)
		if !keep {
			continue
		}
		got.Rows = append(got.Rows, row)
		got.Waiting = row.Kind == Waiting
	}

	if over := len(got.Rows) - MaxRows; over > 0 {
		got.Rows = got.Rows[over:]
		got.Dropped = over
	}
	return got, nil
}

// rowOf turns one event into a row, or drops it.
//
// PostToolUse is dropped: it says a tool finished, which the next row implies, and
// showing both would double the length of every screen to add nothing. The decision
// is on PreToolUse, and the decision is what this view is for.
func rowOf(e event.Event, at time.Time, turn int) (Row, bool) {
	row := Row{At: at, Turn: turn, Tool: e.Tool, Verdict: e.Verdict, Reason: e.Reason}

	switch e.Name {
	case "PostToolUse":
		return Row{}, false

	case "PreToolUse":
		row.Kind = Action
		row.Detail = e.Path

	case "UserPromptSubmit":
		row.Kind = Prompt
		row.Detail = e.Reason // the writer puts the prompt's first line here

	case "Notification", "Stop", "SubagentStop":
		row.Kind = Waiting
		row.Detail = waitingText(e)

	case "SessionStart", "SessionEnd":
		row.Kind = Lifecycle
		row.Detail = lifecycleText(e)

	default:
		row.Kind = Unknown
		row.Detail = e.Path
		if row.Detail == "" {
			row.Detail = e.Reason
		}
	}
	return row, true
}

func waitingText(e event.Event) string {
	if strings.TrimSpace(e.Reason) != "" {
		return e.Reason
	}
	if e.Name == "SubagentStop" {
		return "a subagent finished"
	}
	return "waiting for input"
}

func lifecycleText(e event.Event) string {
	if strings.TrimSpace(e.Reason) != "" {
		return e.Reason
	}
	if e.Name == "SessionEnd" {
		return "session ended"
	}
	return "session started"
}

// Load reads a feed from disk and folds it.
//
// A feed that is not there is not an error: a session that has just started, or one
// whose hook has not fired yet, has no file, and the pane's job then is to say "no
// events yet" rather than to fail. Anything else that goes wrong with the read *is*
// reported — an unreadable file is a different problem from an absent one, and
// conflating them would hide a permissions mistake.
func Load(path string, name user.Name) (Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Session{Identity: name}, nil
		}
		return Session{}, fault.IO{Op: "read", Path: path, Err: err}
	}

	events, skipped, err := event.DecodeEvents(data)
	if err != nil {
		return Session{}, err
	}
	return Fold(name, events, skipped)
}
