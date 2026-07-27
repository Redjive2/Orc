package view

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Detecting a session stopped by a usage limit, and working out when it may move
// again.
//
// This is the failure the fleet had no name for. An agent that hits its limit does
// not fail, does not stop, and does not say anything Orc can see: Claude prints one
// line into its own transcript and sits there. The child process is alive, the
// session is live, `orc status` shows a filled circle, and the agent will never do
// anything again.
//
// It is invisible to `orc wake` for a reason worth stating, because it is the whole
// bug. The limit lands wherever the turn happened to be, which is almost always
// straight after a tool call — so the feed's last event is a PostToolUse, and a feed
// ending mid-tool is exactly what "working" looks like. The one backstop built to
// notice a fleet that has quietly stopped skips these, every pass, forever. Seven
// agents in one fleet stopped at 03:10 and were still stopped twelve hours later,
// nine of them after the limit had already reset.
//
// So detection cannot come from the feed. It comes from the transcript, which is
// the only place the fact exists — the same file `ReadProse` reads, for the same
// reason: it is Claude's own record, and this is a thing only Claude knows.

// Limit is a session stopped by a usage limit.
type Limit struct {
	// Text is what Claude said, kept whole. It is shown rather than paraphrased:
	// the message names a wall-clock time in somebody's own zone, and a paraphrase
	// that got the zone wrong would send an operator back at the wrong hour.
	Text string
	// At is when it said it.
	At time.Time
	// Reset is when the limit lifts, or the zero time when the message did not say
	// in a shape this could read.
	//
	// Unparsed is a real state and not a failure: the limit is still detected, and
	// everything downstream falls back to trying again on a cadence rather than at
	// an instant. Guessing a time would be worse than not having one — an agent
	// woken too early is one that burns its first turn on another refusal.
	Reset time.Time
}

// Known reports whether the reset time could be read.
func (l Limit) Known() bool { return !l.Reset.IsZero() }

// Over reports whether the limit has lifted by now.
//
// An unknown reset is never over. The caller decides what to do about that — see
// the cadence fallback in `orc wake` — and this saying "yes, probably" would be
// this making that decision badly.
func (l Limit) Over(now time.Time) bool {
	return l.Known() && !now.Before(l.Reset)
}

// limitPhrase is what the message says. Matched on the *shape* rather than the
// exact sentence, because the wording is Claude's and not ours: "session limit",
// "usage limit", and whatever it is called next all say the same thing about the
// same session, and a matcher pinned to one of them would silently stop working
// the day it changed.
var limitPhrase = regexp.MustCompile(`(?i)\b(hit|reached|exceeded)\b.{0,40}\blimit\b`)

// resetPhrase reads the time the message names: "resets 1:10am (America/Chicago)",
// "resets 10pm", "resets 13:10". The zone is optional and so are the minutes.
var resetPhrase = regexp.MustCompile(`(?i)\bresets?\s+(\d{1,2})(?::(\d{2}))?\s*(am|pm)?\s*(?:\(([^)]+)\))?`)

// limitEntry is the part of a transcript line this reads.
type limitEntry struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	// APIError is Claude's own flag. It is the half of the test that cannot be
	// faked by an agent: an assistant *quoting* a limit message — which happens,
	// because agents discuss their own limits — is not an assistant that hit one.
	APIError bool `json:"isApiErrorMessage"`
	Message  struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// ReadLimit reports whether a session is stopped at a usage limit right now.
//
// "Right now" is the whole difficulty. A transcript holds every limit the session
// has ever hit, and all but the last were survived — so what counts is whether the
// limit is the *last thing that happened*. Anything the assistant or the writer said
// afterwards means the session moved on, and a limit that has been moved on from is
// history.
//
// The bool is whether a limit was found, not whether the transcript could be read.
// An unreadable transcript is not a limited session, and treating it as one would
// stop a working agent on the strength of a missing file.
func ReadLimit(path string) (Limit, bool) {
	if strings.TrimSpace(path) == "" {
		return Limit{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return Limit{}, false
	}
	defer func() { _ = f.Close() }()

	lines, err := lastLines(f, transcriptTailLines)
	if err != nil {
		return Limit{}, false
	}

	// Backwards, stopping at the first line that says anything about who spoke
	// last. A `system` line after the error is Claude's own bookkeeping and says
	// nothing about whether the conversation moved, so it is stepped over.
	for i := len(lines) - 1; i >= 0; i-- {
		var e limitEntry
		if err := json.Unmarshal(lines[i], &e); err != nil {
			continue
		}
		switch e.Type {
		case "assistant":
			text := textOf(e.Message.Content)
			if !e.APIError || !limitPhrase.MatchString(text) {
				return Limit{}, false
			}
			return Limit{Text: clean(text), At: stamp(e.Timestamp),
				Reset: resetAt(text, stamp(e.Timestamp))}, true
		case "user":
			// The writer said something after it, so whatever came before is over.
			return Limit{}, false
		}
	}
	return Limit{}, false
}

// transcriptTailLines is how far back to look.
//
// Far enough to step over the bookkeeping that follows a turn, and nowhere near far
// enough to be a search: this is asking what just happened, and a limit ten thousand
// lines ago is a limit that was survived.
const transcriptTailLines = 32

// resetAt turns "resets 1:10am (America/Chicago)" into an instant.
//
// The message names a wall clock and a zone and leaves the date to be worked out,
// which is fine for a person reading it minutes later and not fine for a machine
// deciding when to try again. The rule: the named time on whichever day makes it
// *after* the message. A limit hit at 22:10 that resets at 1:10am resets tomorrow;
// one hit at 00:30 that resets at 1:10am resets in forty minutes.
//
// A zone that will not load falls back to the machine's own. That is a guess, and it
// is the right one: an operator's agents and their clock are usually in the same
// place, and the alternative — refusing to read the time at all — turns a limit that
// lifts at a known hour into one that has to be found by poking.
func resetAt(text string, at time.Time) time.Time {
	m := resetPhrase.FindStringSubmatch(text)
	if m == nil || at.IsZero() {
		return time.Time{}
	}

	hour, err := strconv.Atoi(m[1])
	if err != nil || hour > 23 {
		return time.Time{}
	}
	minute := 0
	if m[2] != "" {
		if minute, err = strconv.Atoi(m[2]); err != nil || minute > 59 {
			return time.Time{}
		}
	}
	switch strings.ToLower(m[3]) {
	case "am":
		if hour == 12 {
			hour = 0 // 12am is midnight, which is the one hour the arithmetic gets wrong
		}
	case "pm":
		if hour < 12 {
			hour += 12
		}
	default:
		if hour > 23 {
			return time.Time{}
		}
	}

	zone := at.Location()
	if name := strings.TrimSpace(m[4]); name != "" {
		if loaded, err := time.LoadLocation(name); err == nil {
			zone = loaded
		}
	}

	local := at.In(zone)
	reset := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, zone)
	if !reset.After(local) {
		reset = reset.AddDate(0, 0, 1)
	}
	return reset
}

// stamp reads a transcript's timestamp, or the zero time.
func stamp(raw string) time.Time {
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}
	}
	return at
}

// lastLines returns up to n lines from the end of a file.
//
// It reads a bounded window from the end rather than the whole file. A transcript is
// an agent's entire conversation — tens of megabytes on a long-running session — and
// this question is about its last few lines. Reading it whole to answer that would
// make the check cost more than the thing it protects.
func lastLines(f *os.File, n int) ([][]byte, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	const window = 128 << 10
	size := info.Size()
	from := size - window
	if from < 0 {
		from = 0
	}
	buf := make([]byte, size-from)
	if _, err := f.ReadAt(buf, from); err != nil {
		return nil, err
	}

	lines := splitLines(buf)
	// The first line of a window that did not start at the file's start is a
	// fragment. Dropping it is what keeps this from parsing half a line and
	// deciding something about it.
	if from > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

func splitLines(buf []byte) [][]byte {
	var out [][]byte
	for _, line := range strings.Split(string(buf), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			out = append(out, []byte(trimmed))
		}
	}
	return out
}

// Says renders the limit for a person: what it is and when it lifts.
func (l Limit) Says(now time.Time) string {
	if !l.Known() {
		return "at its usage limit; it did not say when that lifts"
	}
	if l.Over(now) {
		return fmt.Sprintf("its usage limit lifted %s ago", roughly(now.Sub(l.Reset)))
	}
	return fmt.Sprintf("at its usage limit for another %s", roughly(l.Reset.Sub(now)))
}

// roughly is a duration a person reads rather than parses.
func roughly(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
