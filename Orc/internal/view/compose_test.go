package view_test

import (
	"strings"
	"testing"

	"orc/orc/internal/view"
)

// TestTypingNeverReachesTheSession is the property the compose-then-confirm rule
// exists for, and Finish.md names it as a condition of this stream being done.
//
// It is tested here, away from the terminal, because "does a keystroke reach the
// session" is worth testing exactly and is not worth testing through a pty.
func TestTypingComposes(t *testing.T) {
	var c view.Composer

	// A whole message, including the Enter that would submit it in any other TUI.
	for _, chunk := range []string{"the account verifier", "\r", "goes in Common"} {
		if got := c.Feed([]byte(chunk)); got != view.Nothing {
			t.Fatalf("%q asked for %v; nothing may leave before ^S", chunk, got)
		}
	}

	want := "the account verifier\ngoes in Common"
	if c.Text() != want {
		t.Errorf("composed %q, want %q", c.Text(), want)
	}
	if len(c.Lines()) != 2 {
		t.Errorf("lines = %v, want two", c.Lines())
	}

	// And only ^S releases it.
	if got := c.Feed([]byte{view.KeySend}); got != view.Send {
		t.Errorf("^S asked for %v, want Send", got)
	}
	if c.Text() != want {
		t.Error("^S must not clear the buffer; the caller does, once the send worked")
	}
}

// TestSendStopsAtTheControlByte. A paste containing ^S sends what preceded it and
// leaves the rest composed — anything else would make a paste's meaning depend on
// where its control bytes fell.
func TestFeedReturnsOnTheFirstIntent(t *testing.T) {
	var c view.Composer

	if got := c.Feed([]byte("before\x13after")); got != view.Send {
		t.Fatalf("intent = %v, want Send", got)
	}
	if c.Text() != "before" {
		t.Errorf("composed %q, want only what preceded ^S", c.Text())
	}

	// The rest is still in the pipe as far as the caller is concerned; feeding it
	// again is the caller's business, and the buffer is untouched by the send.
	if got := c.Feed([]byte("after")); got != view.Nothing {
		t.Errorf("intent = %v", got)
	}
	if c.Text() != "beforeafter" {
		t.Errorf("composed %q", c.Text())
	}
}

func TestDetachSequence(t *testing.T) {
	var c view.Composer

	// ^\ alone does nothing: it is a prefix, and the next key decides.
	if got := c.Feed([]byte{view.KeyDetach}); got != view.Nothing {
		t.Errorf("^\\ alone asked for %v", got)
	}
	if got := c.Feed([]byte{view.DetachKey}); got != view.Detach {
		t.Errorf("^\\ d asked for %v, want Detach", got)
	}

	// ^\ followed by anything else is not a detach — and is not text either. A
	// control byte inserted into a prompt would be sent to the agent.
	var other view.Composer
	other.Feed([]byte{view.KeyDetach, 'x'})
	if other.Text() != "x" {
		t.Errorf("composed %q, want the ordinary key kept", other.Text())
	}
}

func TestOtherKeys(t *testing.T) {
	var c view.Composer

	if got := c.Feed([]byte{view.KeyDirect}); got != view.Direct {
		t.Errorf("^] asked for %v", got)
	}
	if got := c.Feed([]byte{view.KeyRefresh}); got != view.Refresh {
		t.Errorf("^R asked for %v", got)
	}

	c.Feed([]byte("hello"))
	c.Feed([]byte{view.KeyBack})
	if c.Text() != "hell" {
		t.Errorf("backspace left %q", c.Text())
	}
	c.Feed([]byte{view.KeyKill})
	if !c.Empty() {
		t.Errorf("^U left %q", c.Text())
	}
}

// TestEscapeSequencesDoNotBecomeText. Arrow keys arrive as control bytes, and a
// buffer that kept them would poke escape codes into somebody's session.
func TestControlBytesAreDropped(t *testing.T) {
	var c view.Composer

	c.Feed([]byte("a\x1b[Ab")) // 'a', up-arrow, 'b'
	if strings.ContainsRune(c.Text(), 0x1b) {
		t.Errorf("an escape sequence reached the buffer: %q", c.Text())
	}
	if !strings.HasPrefix(c.Text(), "a") || !strings.HasSuffix(c.Text(), "b") {
		t.Errorf("the ordinary keys were lost: %q", c.Text())
	}
}

func TestInvalidUTF8IsDropped(t *testing.T) {
	var c view.Composer

	c.Feed([]byte{'a', 0xff, 0xfe, 'b'})
	if c.Text() != "ab" {
		t.Errorf("composed %q, want the text bytes only", c.Text())
	}
}

// A terminal pasting a megabyte by accident must not become a megabyte poked into a
// session, and the pane says the buffer is full rather than silently swallowing.
func TestComposeIsBounded(t *testing.T) {
	var c view.Composer

	c.Feed([]byte(strings.Repeat("x", view.MaxCompose+500)))
	if len([]rune(c.Text())) != view.MaxCompose {
		t.Errorf("buffer holds %d runes, want %d", len([]rune(c.Text())), view.MaxCompose)
	}
	if !c.Full() {
		t.Error("hitting the limit was not reported")
	}

	c.Feed([]byte{view.KeyBack})
	if c.Full() {
		t.Error("making room did not clear the full mark")
	}
}

// Multi-byte text survives, which a naive byte loop would mangle.
func TestUnicodeComposes(t *testing.T) {
	var c view.Composer

	c.Feed([]byte("café ✓ 日本語"))
	if c.Text() != "café ✓ 日本語" {
		t.Errorf("composed %q", c.Text())
	}
}

func TestEmptyAndClear(t *testing.T) {
	var c view.Composer

	if !c.Empty() {
		t.Error("a fresh composer is not empty")
	}
	c.Feed([]byte("   \n  "))
	if !c.Empty() {
		t.Error("whitespace is not a message worth sending")
	}
	c.Feed([]byte("real"))
	if c.Empty() {
		t.Error("text is a message")
	}
	c.Clear()
	if !c.Empty() || c.Text() != "" {
		t.Errorf("Clear left %q", c.Text())
	}
	if len(c.Lines()) != 1 {
		t.Error("the input row must never collapse")
	}
}
