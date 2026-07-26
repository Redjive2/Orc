package session_test

import (
	"strings"
	"testing"

	"orc/orc/internal/session"
)

// What may be typed into an agent's terminal.
//
// A poke and a wake message both end up as bytes written into a pty, and text that
// arrives from `orc instruct wake` was written by whoever holds the `instruct`
// permission — not necessarily by the person whose agent receives it. So this is a
// boundary, and the tests are about what must not cross it.

func TestTypeableRefusesWhatATerminalWouldObey(t *testing.T) {
	for _, tc := range []struct {
		what string
		text string
	}{
		// The one that turns a message into commands: closing the bracket early
		// makes everything after it keystrokes rather than content.
		{"the bracketed-paste terminator", "first line\n\x1b[201~/quit"},
		{"the bracketed-paste opener", "\x1b[200~pretending to be a paste"},
		{"an escape sequence", "before\x1b[31m after"},
		{"a NUL", "a\x00b"},
		{"an end-of-transmission", "done\x04"},
		{"an interrupt", "stop\x03"},
		{"invalid UTF-8", string([]byte{0xff, 0xfe})},
	} {
		if err := session.Typeable(tc.text); err == nil {
			t.Errorf("%s was accepted", tc.what)
		}
	}
}

// And what must cross it, because it is how people write.
func TestTypeableAcceptsProse(t *testing.T) {
	for _, tc := range []struct {
		what string
		text string
	}{
		{"a plain nudge", "continue"},
		{"several lines", "look at the parser\n\nthen the lexer\n"},
		{"a tab", "one\ttwo"},
		{"a carriage return, which bracketing exists for", "one\r\ntwo"},
		{"punctuation a shell would care about and a terminal would not", "run `make test` && report $STATUS"},
		{"something that looks like a flag", "--dangerously-skip-permissions is not a message"},
		{"non-ascii", "attention: café, naïve, 日本語"},
	} {
		if err := session.Typeable(tc.text); err != nil {
			t.Errorf("%s was refused: %v", tc.what, err)
		}
	}
}

// The refusal has to say which character and where, because the text usually came
// from a file somebody edited and "it has a control character in it somewhere" is
// not something anybody can act on.
func TestTypeableSaysWhereTheProblemIs(t *testing.T) {
	err := session.Typeable("a fine start\x07and then a bell")
	if err == nil {
		t.Fatal("a bell was accepted")
	}
	if !strings.Contains(err.Error(), "byte 12") {
		t.Errorf("the refusal does not locate it: %v", err)
	}
}
