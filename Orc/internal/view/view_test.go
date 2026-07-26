package view_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/event"
	"orc/orc/internal/view"
)

func agent(t *testing.T, s string) user.Name {
	t.Helper()
	n, err := user.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// fixture is the hand-written feed. The schema is settled in Finish.md, so a file
// like this is a valid input and is what lets the whole view be built and tested
// with no session, no pty, and no hook.
func fixture(t *testing.T) view.Session {
	t.Helper()
	got, err := view.Load(filepath.Join("testdata", "events.jsonl"), agent(t, "ember"))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestFoldsTheFixture(t *testing.T) {
	got := fixture(t)

	if got.Identity.String() != "ember" {
		t.Errorf("identity = %s", got.Identity)
	}
	if got.Turn != 1 {
		t.Errorf("turn = %d, want 1", got.Turn)
	}
	if got.ID == "" {
		t.Error("the session id was not picked up")
	}
	// The transcript path arrives on the first event and is how the view finds
	// Claude's own file without knowing how the path is derived.
	if !strings.HasSuffix(got.Transcript, "transcript.jsonl") {
		t.Errorf("transcript = %q", got.Transcript)
	}
	// Eight events, one of them a PostToolUse, which is dropped.
	if len(got.Rows) != 7 {
		t.Fatalf("%d rows, want 7:\n%+v", len(got.Rows), got.Rows)
	}
	if got.Waiting != true {
		t.Error("the feed ends on Stop, so the session is waiting for input")
	}
}

// TestPostToolUseIsDropped: it says a tool finished, which the next row implies.
// Showing both would double every screen to add nothing.
func TestPostToolUseIsNotARow(t *testing.T) {
	for _, r := range fixture(t).Rows {
		if r.Tool == "Read" && r.Kind == view.Action && r.Verdict == "" {
			t.Errorf("a PostToolUse became a row: %+v", r)
		}
	}
}

func TestBlockedRowKeepsItsReason(t *testing.T) {
	var blocked []view.Row
	for _, r := range fixture(t).Rows {
		if r.Blocked() {
			blocked = append(blocked, r)
		}
	}
	if len(blocked) != 1 {
		t.Fatalf("%d blocked rows, want 1", len(blocked))
	}
	if blocked[0].Reason == "" {
		t.Error("a refusal with no reason sends the reader to the permission table")
	}
	if blocked[0].Detail != "Common/account/account.go" {
		t.Errorf("detail = %q", blocked[0].Detail)
	}
}

// TestWaitingFollowsTheLastEvent. It is the most useful fact on the screen for
// somebody watching four agents, so it is derived rather than remembered.
func TestWaitingIsDerived(t *testing.T) {
	name := agent(t, "ember")

	waiting := []event.Event{
		{At: "2026-07-25T14:00:00.000Z", Name: "Stop"},
	}
	got, err := view.Fold(name, waiting, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Waiting {
		t.Error("a feed ending on Stop is waiting")
	}

	// And a tool call after the stop means it went back to work.
	working := append(waiting, event.Event{
		At: "2026-07-25T14:00:01.000Z", Name: "PreToolUse", Tool: "Read", Path: "a.go",
	})
	got, err = view.Fold(name, working, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Waiting {
		t.Error("a feed that resumed is not waiting")
	}
}

// TestAFreshSessionIsWaiting. A session that has started and been asked nothing is
// sitting at an empty prompt, which is the same fact about the same session as a
// Stop — it simply has not had a turn to end. Reading it as working is what made
// every restarted agent invisible to `orc wake`.
func TestWaitingCoversAFreshSession(t *testing.T) {
	name := agent(t, "ember")

	fresh := []event.Event{
		{At: "2026-07-25T14:00:00.000Z", Name: "SessionEnd"},
		{At: "2026-07-25T14:00:02.000Z", Name: "SessionStart"},
	}
	got, err := view.Fold(name, fresh, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Waiting {
		t.Error("a session at a fresh prompt is waiting to be spoken to")
	}

	// A child that has gone is not waiting for anybody: nothing can be typed at it,
	// and it is `orc tend`'s to bring back.
	got, err = view.Fold(name, fresh[:1], 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Waiting {
		t.Error("a session that has ended is not waiting")
	}
}

// TestAnUnknownEventIsStillShown. A session doing something this build has no name
// for is exactly what an operator wants to see, and refusing to draw it would make
// every Claude release an outage of the view.
func TestUnknownEventsSurvive(t *testing.T) {
	got, err := view.Fold(agent(t, "ember"), []event.Event{
		{At: "2026-07-25T14:00:00.000Z", Name: "SomethingClaudeAddedLater", Path: "a.go"},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 1 || got.Rows[0].Kind != view.Unknown {
		t.Fatalf("rows = %+v", got.Rows)
	}
	if got.Rows[0].Detail != "a.go" {
		t.Errorf("what it was about was lost: %+v", got.Rows[0])
	}
}

// TestTheOldestRowsAreDroppedAndSaidSo. A screen that quietly kept the last N would
// imply the session did less than it did.
func TestRowsAreBounded(t *testing.T) {
	var events []event.Event
	for range view.MaxRows + 10 {
		events = append(events, event.Event{
			At: "2026-07-25T14:00:00.000Z", Name: "PreToolUse", Tool: "Read", Path: "a.go",
		})
	}

	got, err := view.Fold(agent(t, "ember"), events, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != view.MaxRows {
		t.Errorf("%d rows, want %d", len(got.Rows), view.MaxRows)
	}
	if got.Dropped != 10 {
		t.Errorf("dropped = %d, want 10", got.Dropped)
	}
}

// A feed that is not there is not an error: a session that has just started has no
// file, and the pane's job then is to say so rather than to fail.
func TestMissingFeedIsEmptyNotBroken(t *testing.T) {
	got, err := view.Load(filepath.Join(t.TempDir(), "nothing.jsonl"), agent(t, "ember"))
	if err != nil {
		t.Fatalf("a missing feed = %v", err)
	}
	if got.Live() {
		t.Error("a missing feed produced rows")
	}
	if got.Identity.String() != "ember" {
		t.Error("the identity was lost")
	}
}

// An unreadable feed is a different problem from an absent one — conflating them
// would hide a permissions mistake.
func TestUnreadableFeedIsReported(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o000); err != nil {
		t.Fatal(err)
	}

	if _, err := view.Load(path, agent(t, "ember")); err == nil {
		t.Error("an unreadable feed was reported as empty")
	}
}

// TestATornFinalLineIsDroppedAndCounted. A hook writes on every tool call, so an
// interrupted append is ordinary rather than damage.
func TestInterruptedAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	good := `{"at":"2026-07-25T14:00:00.000Z","session":"s","event":"Stop"}` + "\n"
	if err := os.WriteFile(path, []byte(good+`{"at":"2026-07-2`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := view.Load(path, agent(t, "ember"))
	if err != nil {
		t.Fatalf("a torn final line failed the load: %v", err)
	}
	if len(got.Rows) != 1 {
		t.Errorf("%d rows, want the one complete event", len(got.Rows))
	}
	if got.Skipped == 0 {
		t.Error("the torn bytes were not counted")
	}
}

// Corruption anywhere but the end is not an interrupted write, and is refused.
func TestCorruptionIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	body := "{ not an event\n" + `{"at":"2026-07-25T14:00:00.000Z","session":"s","event":"Stop"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := view.Load(path, agent(t, "ember")); err == nil {
		t.Error("corruption in the middle of a feed was accepted")
	}
}

func TestFoldRejectsBadArguments(t *testing.T) {
	if _, err := view.Fold(user.Name{}, nil, 0); err == nil {
		t.Error("Fold with no identity was accepted")
	}
	_, err := view.Fold(agent(t, "ember"), []event.Event{{At: "half past four", Name: "Stop"}}, 0)
	if err == nil {
		t.Error("an unparseable timestamp was accepted")
	} else if !strings.Contains(err.Error(), "timestamp") {
		t.Errorf("err = %v", err)
	}
	_ = fault.ErrParse
}
