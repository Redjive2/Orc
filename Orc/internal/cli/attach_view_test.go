package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orc/common/user"
	"orc/orc/internal/pty"
	"orc/orc/internal/style"
	"orc/orc/internal/view"
)

// The clean view's own state, tested inside the package.
//
// Everything worth testing about the view proper is in internal/view and
// internal/render, which are pure and need no terminal. What is left here is the
// small mutable part — what happens when you try to leave with something typed, and
// whether a redraw actually puts a pane on the terminal — and it is tested from
// inside because that state is deliberately not exported: nothing outside this file
// should be able to reach into a live attach.

func watcherFor(t *testing.T, out *bytes.Buffer) *watcher {
	t.Helper()

	who, err := user.Parse("ember")
	if err != nil {
		t.Fatal(err)
	}
	return &watcher{
		app:    App{Stdout: out, Stderr: out, out: style.Plain(), err: style.Plain()},
		who:    who,
		feed:   filepath.Join("..", "view", "testdata", "events.jsonl"),
		facts:  view.NoFacts(),
		width:  80,
		height: 20,
	}
}

// TestDetachingWithUnsentTextWarns is one of Finish.md's conditions for this stream.
//
// It warns once and leaves on the second press. Discarding silently would throw away
// the thing the composed buffer exists to protect; refusing outright would make ^\ d
// unreliable, which is worse for the key somebody presses to get out.
func TestUnsentTextWarnsOnceThenLeaves(t *testing.T) {
	var out bytes.Buffer
	w := watcherFor(t, &out)

	// Nothing typed: leaving is immediate.
	if !w.confirmLeave() {
		t.Fatal("an empty buffer should not hold a detach up")
	}

	w.compose.Feed([]byte("half a thought"))
	if w.confirmLeave() {
		t.Error("detaching discarded unsent text without a word")
	}
	if !strings.Contains(w.notice, "unsent") {
		t.Errorf("the warning does not say what is unsent: %q", w.notice)
	}
	if !w.alarm {
		t.Error("the warning is not marked as one")
	}
	// And it says both ways forward, because a warning that only says "no" is one
	// people learn to press through.
	if !strings.Contains(w.notice, "^S") {
		t.Errorf("the warning does not offer to send it: %q", w.notice)
	}

	if !w.confirmLeave() {
		t.Error("the second attempt should leave")
	}
}

// A send that worked clears the buffer and the warning with it.
func TestSendingClearsTheWarning(t *testing.T) {
	var out bytes.Buffer
	w := watcherFor(t, &out)

	w.compose.Feed([]byte("something"))
	w.confirmLeave() // arms the warning
	w.compose.Clear()
	w.warned = false

	if !w.confirmLeave() {
		t.Error("an empty buffer still held the detach up after a send")
	}
}

// TestTheViewDrawsFromTheFeedAlone. No session, no socket, no pty: the screen is a
// function of a file, which is what makes the whole thing testable.
func TestDrawRendersTheFeed(t *testing.T) {
	var out bytes.Buffer
	w := watcherFor(t, &out)
	w.compose.Feed([]byte("a reply"))

	if err := w.draw(); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	for _, want := range []string{"ember", "Common/user/user.go", "✗ denied", "compose", "a reply", "^S send"} {
		if !strings.Contains(got, want) {
			t.Errorf("the drawn screen lacks %q:\n%s", want, got)
		}
	}
	// Raw mode: every newline must be a carriage return too, or each row starts
	// where the last one ended and the pane comes out as a staircase.
	if bare, paired := strings.Count(got, "\n"), strings.Count(got, "\r\n"); bare != paired {
		t.Errorf("%d of %d line breaks are missing their carriage return", bare-paired, bare)
	}
	if !strings.HasPrefix(got, "\x1b[H") {
		t.Error("the screen is not homed before drawing, so it would scroll")
	}
}

// A feed that will not read is worth saying on the screen rather than tearing the
// view down: the session is still running, and the operator may want --direct.
func TestABrokenFeedKeepsThePane(t *testing.T) {
	var out bytes.Buffer
	w := watcherFor(t, &out)
	w.feed = filepath.Join(t.TempDir(), "broken.jsonl")

	if err := writeFile(w.feed, "{ this is not an event\n{ nor is this\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.draw(); err != nil {
		t.Fatalf("a broken feed tore the view down: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "will not read") {
		t.Errorf("the screen does not say the feed is broken:\n%s", got)
	}
	if !strings.Contains(got, "ember") {
		t.Errorf("the pane went with the feed:\n%s", got)
	}
}

// leave says what happened to the session, and to anything typed.
func TestLeaveReportsUnsentText(t *testing.T) {
	var out bytes.Buffer
	w := watcherFor(t, &out)
	w.compose.Feed([]byte("the thing I was going to say"))

	if err := w.leave(noRestore()); err != nil {
		t.Fatal(err)
	}

	got := out.String()
	if !strings.Contains(got, "still running") {
		t.Errorf("detaching should say the session survives it:\n%s", got)
	}
	if !strings.Contains(got, "the thing I was going to say") {
		t.Errorf("what was typed was lost without a word:\n%s", got)
	}
}

// noRestore is the terminal restorer a test has: there is no raw mode to undo.
func noRestore() *pty.Restorer { return nil }

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o600)
}
