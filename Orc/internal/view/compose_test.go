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

// --- keys that are not characters ----------------------------------------

// A key that is not a character arrives as several bytes, and only the first of
// them is a control byte. Dropping controls and keeping the rest put the tail of
// every one of them in the buffer as text: an up arrow composed `[A`.
//
// That is not merely untidy. A non-empty buffer turns the first ^\ d into a warning
// about unsent text instead of a detach, so pressing an arrow key once — which is
// the first thing anybody does in a full-screen view — cost the ability to leave on
// the first try, over two characters nobody typed.
func TestKeysThatAreNotCharactersLeaveNothingBehind(t *testing.T) {
	for _, k := range []struct {
		what string
		in   string
	}{
		{"up", "\x1b[A"},
		{"down", "\x1b[B"},
		{"right", "\x1b[C"},
		{"left", "\x1b[D"},
		{"home", "\x1b[H"},
		{"end", "\x1b[F"},
		{"delete", "\x1b[3~"},
		{"page up", "\x1b[5~"},
		{"f1 (application mode)", "\x1bOP"},
		{"shift-f5", "\x1b[15;2~"},
		{"alt-b", "\x1bb"},
		{"a bare escape", "\x1b"},
	} {
		var c view.Composer
		if got := c.Feed([]byte(k.in)); got != view.Nothing {
			t.Errorf("%s asked for %v; it is not a command here", k.what, got)
		}
		if c.Text() != "" {
			t.Errorf("%s composed %q", k.what, c.Text())
		}
		if !c.Empty() {
			t.Errorf("%s left the buffer non-empty, which holds up a detach", k.what)
		}
	}
}

// The words on either side survive, and only the key between them goes.
func TestAnArrowBetweenWordsTakesNothingWithIt(t *testing.T) {
	var c view.Composer
	c.Feed([]byte("half"))
	c.Feed([]byte("\x1b[A"))
	c.Feed([]byte(" a thought"))

	if c.Text() != "half a thought" {
		t.Errorf("composed %q", c.Text())
	}
}

// The bug this is really about: an arrow key must not cost the first press of ^\ d.
func TestDetachIsNotHeldUpByAnArrowKey(t *testing.T) {
	var c view.Composer
	c.Feed([]byte("\x1b[A\x1b[B"))
	if !c.Empty() {
		t.Fatalf("the arrows composed %q, so a detach would warn about unsent text", c.Text())
	}
	if got := c.Feed([]byte{0x1c, 'd'}); got != view.Detach {
		t.Errorf("^\\ d asked for %v", got)
	}
}

// A terminal is free to split a sequence across reads, and one that reset between
// them would put its own tail on the screen.
func TestASequenceSplitAcrossReadsIsStillOneKey(t *testing.T) {
	var c view.Composer
	for _, part := range []string{"\x1b", "[", "1", ";", "2", "A"} {
		c.Feed([]byte(part))
	}
	c.Feed([]byte("text"))
	if c.Text() != "text" {
		t.Errorf("a split sequence composed %q", c.Text())
	}
}

// `d` and `S` occur inside real sequences. Neither may be read as a command: a
// detach triggered by an arrow key would be the same bug wearing the other hat.
func TestACommandLetterInsideASequenceIsNotACommand(t *testing.T) {
	for _, in := range []string{"\x1bOd", "\x1b[1d", "\x1bOS"} {
		var c view.Composer
		if got := c.Feed([]byte(in)); got != view.Nothing {
			t.Errorf("%q asked for %v", in, got)
		}
		if c.Text() != "" {
			t.Errorf("%q composed %q", in, c.Text())
		}
	}
}

// Bracketed paste wraps what is pasted in two sequences. The markers go and the
// text stays.
func TestPasteMarkersAreNotPastedText(t *testing.T) {
	var c view.Composer
	c.Feed([]byte("\x1b[200~pasted words\x1b[201~"))
	if c.Text() != "pasted words" {
		t.Errorf("composed %q", c.Text())
	}
}

// TestThereIsAOneKeyWayOut.
//
// `^\` then a letter is the only sequence that is safe in the raw proxy, where every
// keystroke belongs to the agent. In this pane nothing reaches the agent until ^S
// says so, so it can afford a single key — and it needs one, because `^\` is awkward
// on a US keyboard and can be impossible on layouts where a backslash needs AltGr.
// An operator who cannot type the way out is an operator stuck in an attach.
func TestLeavingTakesOneKey(t *testing.T) {
	var c view.Composer
	if got := c.Feed([]byte{0x11}); got != view.Detach {
		t.Errorf("^Q asked for %v, want a detach", got)
	}

	// And it leaves whether or not something has been typed: the buffer is not the
	// question, getting out is.
	var typed view.Composer
	typed.Feed([]byte("half a thought"))
	if got := typed.Feed([]byte{0x11}); got != view.Detach {
		t.Errorf("^Q with text asked for %v, want a detach", got)
	}
}

// The prefix takes any of the three letters, matching the raw proxy, so one habit
// works in both and nobody has to know which mode they are in.
func TestTheDetachPrefixTakesAnyOfItsLetters(t *testing.T) {
	for _, key := range []byte{'d', 'q', '.'} {
		var c view.Composer
		if got := c.Feed([]byte{0x1c, key}); got != view.Detach {
			t.Errorf("^\\ %q asked for %v, want a detach", key, got)
		}
	}

	// A letter that is not one of them is not a detach, and the pair is not text
	// either — a control byte in a prompt would be sent to the agent.
	var c view.Composer
	if got := c.Feed([]byte{0x1c, 'x'}); got != view.Nothing {
		t.Errorf("^\\ x asked for %v, want nothing", got)
	}
}

// TestAnArrowKeyStillIsNotADetach. Every arrow, function key and paste marker begins
// with Escape, so a bare Escape cannot be told from the start of one without a
// timeout — and a detach on a timeout fires when somebody presses Left on a slow
// connection.
func TestEscapeIsNotAWayOut(t *testing.T) {
	for _, seq := range []string{"\x1b[A", "\x1b[D", "\x1bOP", "\x1b[200~pasted\x1b[201~"} {
		var c view.Composer
		if got := c.Feed([]byte(seq)); got == view.Detach {
			t.Errorf("%q detached", seq)
		}
	}
}
