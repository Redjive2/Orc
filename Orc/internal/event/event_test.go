package event_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/orc/internal/event"
)

// The feed is written by a hook inside somebody's live session, on every tool
// call, and read by `orc attach`. Two things follow, and they are what this file
// is about:
//
//   - the reader must survive a damaged file, because the writer can be killed
//     mid-append at any moment;
//   - an interrupted final line is *not* corruption, and everything else is —
//     conflating them either hides real damage or reports it constantly.

// The tree's timestamp format, milliseconds and all. clock.Format produces it,
// and an event carrying anything else does not decode.
const at = "2026-07-25T12:00:00.000Z"

func sample() event.Event {
	return event.Event{
		At: at, Session: "11111111-2222-3333-4444-555555555555",
		Name: "PostToolUse", Tool: "Edit",
		Path: "Anno/internal/marker/marker.go", Verdict: event.VerdictAllow,
	}
}

func TestAnEventSurvivesTheRoundTrip(t *testing.T) {
	raw, err := sample().Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := event.DecodeEvent(raw)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if got.Name != "PostToolUse" || got.Tool != "Edit" || got.Verdict != event.VerdictAllow {
		t.Errorf("event = %+v", got)
	}
	if got.At != at {
		t.Errorf("time = %q, want %q", got.At, at)
	}
	// A path is recorded and content never is, so an event stays small enough
	// that a line is an atomic append.
	if strings.Contains(string(raw), "\n") {
		t.Errorf("an encoded event contains a newline, so it is not one line: %q", raw)
	}
}

// TestAnInterruptedFinalLineIsNotCorruption is the distinction the whole reader
// rests on. A hook killed mid-append leaves a partial last line; a partial line
// anywhere else means the file was damaged by something else.
func TestAnInterruptedFinalLineIsNotCorruption(t *testing.T) {
	good, err := sample().Encode()
	if err != nil {
		t.Fatal(err)
	}
	data := append(append([]byte{}, good...), '\n')
	data = append(data, []byte(`{"at":"2026-07-25T12:00`)...) // cut off mid-write

	events, skipped, err := event.DecodeEvents(data)
	if err != nil {
		t.Fatalf("an interrupted append should be tolerated: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("events = %+v, want the one complete line", events)
	}
	if skipped == 0 {
		t.Error("the dropped bytes should be reported, not silently lost")
	}
}

// Damage anywhere but the end is corruption, and saying so is the point: a feed
// that quietly skipped bad lines would show a view with holes nobody could see.
func TestDamageBeforeTheEndIsAnError(t *testing.T) {
	good, err := sample().Encode()
	if err != nil {
		t.Fatal(err)
	}
	var data []byte
	data = append(data, []byte("{not json at all}\n")...)
	data = append(data, good...)
	data = append(data, '\n')

	if _, _, err := event.DecodeEvents(data); !errors.Is(err, fault.ErrParse) {
		t.Errorf("error = %v, want a parse fault", err)
	}
}

func TestAnEmptyLineInTheMiddleIsAnError(t *testing.T) {
	good, err := sample().Encode()
	if err != nil {
		t.Fatal(err)
	}
	data := append([]byte("\n"), good...)
	data = append(data, '\n')

	if _, _, err := event.DecodeEvents(data); !errors.Is(err, fault.ErrParse) {
		t.Errorf("error = %v, want a parse fault", err)
	}
}

// An empty feed is a session that has done nothing yet, which is every session
// for its first moment.
func TestAnEmptyFeedIsNotAnError(t *testing.T) {
	events, skipped, err := event.DecodeEvents(nil)
	if err != nil || len(events) != 0 || skipped != 0 {
		t.Errorf("DecodeEvents(nil) = %v, %d, %v", events, skipped, err)
	}
}

// TestAnOverLongEventIsRefusedRatherThanTruncated: a half-written line would be
// indistinguishable from an interrupted append, so the writer refuses instead.
func TestAnOverLongEventIsRefusedRatherThanTruncated(t *testing.T) {
	ev := sample()
	ev.Path = strings.Repeat("x", event.MaxEventLine)

	if _, err := ev.Encode(); err == nil {
		t.Error("an event past the line limit should be refused")
	}
}

func TestAppendAndReadAFeed(t *testing.T) {
	path := filepath.Join(t.TempDir(), event.EventFile)

	for _, tool := range []string{"Read", "Edit", "Bash"} {
		ev := sample()
		ev.Tool = tool
		if err := event.Append(path, ev); err != nil {
			t.Fatalf("Append %s: %v", tool, err)
		}
	}

	events, skipped, err := event.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped %d bytes of a feed nothing interrupted", skipped)
	}
	if len(events) != 3 {
		t.Fatalf("events = %+v", events)
	}
	// In the order they happened, because the view draws a session's history.
	for i, want := range []string{"Read", "Edit", "Bash"} {
		if events[i].Tool != want {
			t.Errorf("event %d is %s, want %s", i, events[i].Tool, want)
		}
	}
}

// A feed that is not there is a session that has not written one, which is not a
// failure — the view shows an empty history rather than an error.
func TestReadingAFeedThatIsNotThere(t *testing.T) {
	events, _, err := event.Read(filepath.Join(t.TempDir(), event.EventFile))
	if err != nil && !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("Read: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("events = %+v", events)
	}
}

// TestAppendIsAtomicPerLine: the hook takes no lock, so two firings in the same
// session must not interleave into a line neither wrote.
func TestAppendIsAtomicPerLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), event.EventFile)

	done := make(chan error, 8)
	for i := range 8 {
		go func() {
			ev := sample()
			ev.Tool = strings.Repeat("t", i+1)
			done <- event.Append(path, ev)
		}()
	}
	for range 8 {
		if err := <-done; err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	events, skipped, err := event.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if skipped != 0 || len(events) != 8 {
		t.Errorf("%d events, %d bytes skipped; want 8 whole lines", len(events), skipped)
	}
}

// The feed is appended to directly rather than through the store, because the
// hook opens the store read-only. So it makes the directory it needs: a hook that
// failed because a session directory was not there yet would be the logging
// breaking the tool call it was logging.
func TestAppendMakesTheDirectoryItNeeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session", event.EventFile)
	if err := event.Append(path, sample()); err != nil {
		t.Fatalf("Append: %v", err)
	}
	events, _, err := event.Read(path)
	if err != nil || len(events) != 1 {
		t.Errorf("Read = %v, %v", events, err)
	}
}

// A hand-written feed is a valid input: it is what lets the view be developed
// against a fixture rather than against a live session.
func TestAHandWrittenFeedReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), event.EventFile)
	body := `{"at":"2026-07-25T12:00:00.000Z","session":"s","event":"SessionStart"}
{"at":"2026-07-25T12:00:01.000Z","session":"s","event":"PreToolUse","tool":"Write","path":"a.go","verdict":"block"}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	events, _, err := event.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v", events)
	}
	if events[1].Verdict != event.VerdictBlock {
		t.Errorf("verdict = %q, want block", events[1].Verdict)
	}
}
