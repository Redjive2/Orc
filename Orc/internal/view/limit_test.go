package view_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orc/orc/internal/view"
)

// Detecting the silence nothing else could see.
//
// The shape under test is real. Seven agents in one fleet stopped at 03:10 on the
// same message, and were still stopped twelve hours later — nine of those after the
// limit had already lifted — because the only record of what happened is one line in
// Claude's own transcript, and nothing read it.

// The line as Claude actually writes it, taken from a stopped session.
const hitIt = `{"type":"assistant","timestamp":"2026-07-27T03:10:37.875Z","isApiErrorMessage":true,` +
	`"message":{"role":"assistant","content":[{"type":"text",` +
	`"text":"You've hit your session limit · resets 1:10am (America/Chicago)"}]}}`

// What Claude writes after it. It says nothing about whether the conversation
// moved on, so it must not be read as the conversation moving on.
const bookkeeping = `{"type":"system","timestamp":"2026-07-27T03:10:37.881Z","subtype":"turn_end"}`

const working = `{"type":"assistant","timestamp":"2026-07-27T03:11:00Z",` +
	`"message":{"role":"assistant","content":[{"type":"text","text":"back to it"}]}}`

const spoken = `{"type":"user","timestamp":"2026-07-27T03:12:00Z",` +
	`"message":{"role":"user","content":"carry on"}}`

// transcript writes lines to a file and returns its path.
func transcript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	body := ""
	for _, line := range lines {
		body += line + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestALimitedSessionIsFound(t *testing.T) {
	got, hit := view.ReadLimit(transcript(t, spoken, hitIt, bookkeeping))
	if !hit {
		t.Fatal("a session sitting at its usage limit was read as working; this is the whole bug")
	}
	if got.Text != "You've hit your session limit · resets 1:10am (America/Chicago)" {
		t.Errorf("the message was not kept whole: %q", got.Text)
	}
	if !got.At.Equal(time.Date(2026, 7, 27, 3, 10, 37, 875000000, time.UTC)) {
		t.Errorf("hit at %s", got.At)
	}

	// 22:10 in Chicago, resetting at 1:10am, is 1:10am *tomorrow* — 06:10Z.
	want := time.Date(2026, 7, 27, 6, 10, 0, 0, time.UTC)
	if !got.Reset.Equal(want) {
		t.Errorf("resets at %s, want %s", got.Reset.UTC(), want)
	}
}

// A transcript holds every limit the session has ever hit, and all but the last were
// survived. What counts is whether it is the *last* thing that happened.
func TestALimitThatWasMovedOnFromIsNotALimit(t *testing.T) {
	for _, tc := range []struct {
		what  string
		lines []string
	}{
		{"the assistant carried on afterwards", []string{hitIt, working}},
		{"somebody spoke to it afterwards", []string{hitIt, spoken}},
		{"it was woken and worked for hours", []string{hitIt, spoken, working, working}},
	} {
		if _, hit := view.ReadLimit(transcript(t, tc.lines...)); hit {
			t.Errorf("%s, and it is still reported as limited — an agent that recovered "+
				"would be treated as stopped for the rest of its life", tc.what)
		}
	}
}

// The flag is half the test, and it is the half an agent cannot fake. Agents talk
// about their own limits; one quoting the message is not one that hit it.
func TestAnAgentTalkingAboutLimitsIsNotAtOne(t *testing.T) {
	quoting := `{"type":"assistant","timestamp":"2026-07-27T03:10:37Z",` +
		`"message":{"role":"assistant","content":[{"type":"text",` +
		`"text":"if you hit your session limit, orc wake now resumes you"}]}}`
	if _, hit := view.ReadLimit(transcript(t, quoting)); hit {
		t.Error("an agent describing a limit was read as being at one")
	}
}

func TestATranscriptThatIsNotThereIsNotALimit(t *testing.T) {
	if _, hit := view.ReadLimit(""); hit {
		t.Error("a session with no transcript was called limited")
	}
	if _, hit := view.ReadLimit(filepath.Join(t.TempDir(), "gone.jsonl")); hit {
		t.Error("a missing transcript was called limited — a stopped agent on the strength " +
			"of an absent file is worse than the bug it is meant to catch")
	}
}

// --- when it lifts --------------------------------------------------------

// The message names a wall clock and leaves the date to be worked out. Getting this
// wrong in one direction wakes an agent into a second refusal; in the other it
// leaves it stopped for a day.
func TestWhenTheLimitLifts(t *testing.T) {
	for _, tc := range []struct {
		what string
		at   string
		says string
		want string
	}{
		{"later today", "2026-07-27T00:30:00Z", "resets 1:10am (UTC)", "2026-07-27T01:10:00Z"},
		{"tomorrow", "2026-07-27T03:10:00Z", "resets 1:10am (UTC)", "2026-07-28T01:10:00Z"},
		{"an afternoon reset", "2026-07-27T03:10:00Z", "resets 2pm (UTC)", "2026-07-27T14:00:00Z"},
		{"midnight is 12am, not noon", "2026-07-27T22:00:00Z", "resets 12am (UTC)", "2026-07-28T00:00:00Z"},
		{"noon is 12pm", "2026-07-27T03:00:00Z", "resets 12pm (UTC)", "2026-07-27T12:00:00Z"},
		{"a 24-hour clock", "2026-07-27T03:00:00Z", "resets 13:10 (UTC)", "2026-07-27T13:10:00Z"},
		{"minutes are optional", "2026-07-27T03:00:00Z", "resets 9pm (UTC)", "2026-07-27T21:00:00Z"},
		{"another zone entirely", "2026-07-27T03:10:00Z", "resets 1:10am (America/Chicago)", "2026-07-27T06:10:00Z"},
	} {
		t.Run(tc.what, func(t *testing.T) {
			line := `{"type":"assistant","timestamp":"` + tc.at + `","isApiErrorMessage":true,` +
				`"message":{"role":"assistant","content":[{"type":"text",` +
				`"text":"You've hit your session limit · ` + tc.says + `"}]}}`
			got, hit := view.ReadLimit(transcript(t, line))
			if !hit {
				t.Fatal("not read as a limit")
			}
			want, err := time.Parse(time.RFC3339, tc.want)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Reset.Equal(want) {
				t.Errorf("resets %s, want %s", got.Reset.UTC(), want)
			}
		})
	}
}

// A message that does not say when it lifts is still a limit. Saying so and having
// no time is a state the caller can act on; guessing a time is one it cannot.
func TestALimitWithNoTimeIsStillALimit(t *testing.T) {
	line := `{"type":"assistant","timestamp":"2026-07-27T03:10:00Z","isApiErrorMessage":true,` +
		`"message":{"role":"assistant","content":[{"type":"text",` +
		`"text":"You have reached your usage limit."}]}}`
	got, hit := view.ReadLimit(transcript(t, line))
	if !hit {
		t.Fatal("a limit with no reset time was not read as a limit at all")
	}
	if got.Known() {
		t.Errorf("a time was invented: %s", got.Reset)
	}
	// And it is never "over", because nobody knows that.
	if got.Over(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Error("an unknown reset was declared over")
	}
	if !strings.Contains(got.Says(time.Now()), "did not say") {
		t.Errorf("it does not admit that it does not know: %q", got.Says(time.Now()))
	}
}

// Over is what the waker branches on, and the boundary is the moment itself.
func TestOverIsTrueFromTheResetOnwards(t *testing.T) {
	reset := time.Date(2026, 7, 27, 6, 10, 0, 0, time.UTC)
	l := view.Limit{Reset: reset}
	if l.Over(reset.Add(-time.Second)) {
		t.Error("over a second early, which wakes the agent into another refusal")
	}
	if !l.Over(reset) {
		t.Error("not over at the reset itself")
	}
	if !l.Over(reset.Add(time.Hour)) {
		t.Error("not over an hour later")
	}
}

// The tail is bounded: a transcript is an agent's whole conversation, and this
// question is about its last few lines. A limit far enough back is one that was
// survived, and reading the file whole to find it would cost more than the check
// protects.
func TestOnlyTheEndOfTheTranscriptIsRead(t *testing.T) {
	lines := []string{hitIt}
	for i := 0; i < 200; i++ {
		lines = append(lines, working)
	}
	if _, hit := view.ReadLimit(transcript(t, lines...)); hit {
		t.Error("a limit two hundred turns ago was read as the current state")
	}
}
