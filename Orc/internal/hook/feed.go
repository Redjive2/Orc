package hook

import (
	"strings"

	"orc/common/clock"
	"orc/common/user"
	"orc/orc/internal/event"
	"orc/orc/internal/store"
)

// The event feed: one line per hook firing, which is what `orc attach` draws from.
//
// A pty carrying a TUI is a stream of screen redraws rather than a stream of facts, so
// the clean view is built from this instead. The schema is fixed in internal/event and
// is not this package's to change.
//
// Two rules govern the writer, and both follow from where it runs — inside somebody's
// live session, on every tool call:
//
//   - **Never fatal.** Every error is dropped. A feed that could not be written is a
//     view with a gap in it; a tool call that failed because its logging failed would
//     be the hook doing precisely what a bystander must not.
//   - **O(1) on the hot path.** A tool call appends and reads nothing. Only
//     `UserPromptSubmit` — once per turn — reads the feed back, to number the turn.
//     The view assigns turns to the tool calls between them, because it is scanning
//     the feed anyway.

// feed records what happened. A zero feed is a working no-op, which is what makes the
// nil-store case need no special handling at the call sites.
type feed struct {
	path    string
	session string
	now     clock.Clock
	payload payload
	first   bool
}

// newFeed prepares the writer.
//
// The store is where the path comes from, so a nil store means no feed — the layout is
// the store's to know, and a hook that composed the path itself would be a second
// definition of it.
func newFeed(s *store.Store, opts Options, who user.Name, p payload) feed {
	if s == nil {
		return feed{}
	}
	session := strings.TrimSpace(p.SessionID)
	if session == "" {
		if got, ok := opts.env(EnvSession); ok {
			session = strings.TrimSpace(got)
		}
	}
	return feed{
		path:    s.EventsPath(who),
		session: session,
		now:     opts.clock(),
		payload: p,
	}
}

// record appends one event.
func (f feed) record(verdict event.Verdict, reason string) {
	if f.path == "" {
		return
	}

	ev := event.Event{
		At:      clock.Format(f.now.Now()),
		Session: f.session,
		Name:    f.payload.HookEventName,
		Tool:    f.payload.ToolName,
		Verdict: verdict,
		Reason:  firstLine(reason),
	}

	// The path, when the call has one. It is the path and never the content: a feed
	// carrying the text of every edit would be a second copy of the repository.
	if targets, _ := f.payload.targets(); len(targets) > 0 {
		ev.Path = targets[0]
	}

	// The transcript, on the events that carry it. Claude tells the hook where its own
	// JSONL is, so the view never has to know how that path is derived — which is the
	// difference between reading a documented value and guessing at a private one.
	if f.payload.HookEventName == "SessionStart" || f.payload.HookEventName == "UserPromptSubmit" {
		ev.Transcript = f.payload.TranscriptPath
	}

	// The turn number, once per turn. This is the only place the feed is read back,
	// and it is deliberately not on the tool-call path.
	if f.payload.HookEventName == "UserPromptSubmit" {
		ev.Turn = f.nextTurn()
	}

	_ = event.Append(f.path, ev)
}

// nextTurn counts the turns already in the feed.
//
// A feed that cannot be read yields turn 1, which is wrong in the harmless direction:
// a view showing a turn number twice is a cosmetic problem, and a view that refused to
// draw because it could not number a turn is not.
func (f feed) nextTurn() int {
	events, _, err := event.Read(f.path)
	if err != nil {
		return 1
	}
	turns := 0
	for _, e := range events {
		if e.Name == "UserPromptSubmit" && e.Session == f.session {
			turns++
		}
	}
	return turns + 1
}

// firstLine trims a refusal to its headline for the feed.
//
// The whole refusal went to the agent on stderr, where it belongs. The feed is a
// timeline somebody is scanning, and a six-line explanation in a table cell is a
// timeline nobody can read.
func firstLine(s string) string {
	if s == "" {
		return ""
	}
	line := strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	return strings.TrimPrefix(line, "orc: ")
}
