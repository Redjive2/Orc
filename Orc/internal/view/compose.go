package view

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Typing composes; ^S sends.
//
// Confirmed with the user (Plan.md §6.2): Enter inserts a newline and nothing reaches
// the session until ^S. The reason is the case this view exists for — watching four
// agents at once, one of them mid-turn — where a stray keystroke landing in a working
// session is a real cost and one extra key is not.
//
// The state machine is here, away from the terminal, because "does a keystroke reach
// the session" is the property most worth testing and least worth testing through a
// pty. Feed it bytes, ask what it decided.

// Keys the composer acts on. Everything else is text.
const (
	KeySend    = 0x13 // ^S
	KeyDirect  = 0x1d // ^]
	KeyRefresh = 0x12 // ^R
	KeyDetach  = 0x1c // ^\ — the prefix; the next key decides
	KeyReturn  = '\r'
	KeyNewline = '\n'
	KeyBack    = 0x7f
	KeyBackAlt = 0x08
	KeyKill    = 0x15 // ^U clears the line, as a shell does
)

// DetachKey is what follows ^\ to detach. It matches the raw proxy's sequence, so one
// pair of fingers works in both views.
const DetachKey = 'd'

// MaxCompose bounds the buffer.
//
// A composed message is a prompt, not a document. The limit is generous enough that
// nobody writing a paragraph meets it and small enough that a terminal pasting a
// megabyte by accident does not become a megabyte poked into a session.
const MaxCompose = 16 << 10

// Intent is what a batch of keystrokes asked for.
type Intent int

const (
	// Nothing happened that the caller must act on; the buffer may have changed.
	Nothing Intent = iota
	// Send delivers the buffer to the session.
	Send
	// Detach leaves the view.
	Detach
	// Direct switches to the raw session.
	Direct
	// Refresh asks for a redraw.
	Refresh
)

// Composer holds what has been typed and has not been sent.
//
// The buffer lives in the attacher rather than the session, which is what makes
// detaching with text unsent a warning rather than a silent loss.
type Composer struct {
	buf     []rune
	armed   bool // ^\ seen; the next key decides
	full    bool // the limit was hit, so the pane can say so
	dropped bool // something unprintable was dropped
}

// Text is what has been composed.
func (c *Composer) Text() string { return string(c.buf) }

// Empty reports whether there is nothing to send.
func (c *Composer) Empty() bool { return len(strings.TrimSpace(string(c.buf))) == 0 }

// Full reports whether the buffer hit its limit.
func (c *Composer) Full() bool { return c.full }

// Clear empties the buffer, which is what sending does.
func (c *Composer) Clear() {
	c.buf = c.buf[:0]
	c.full = false
	c.dropped = false
}

// Feed consumes raw terminal bytes and reports what they asked for.
//
// It returns on the *first* intent in the batch rather than draining it, so a paste
// containing ^S sends exactly what preceded it and the rest stays composed. Anything
// else would make a paste's meaning depend on where its control bytes fell.
func (c *Composer) Feed(in []byte) Intent {
	for len(in) > 0 {
		r, size := utf8.DecodeRune(in)
		if r == utf8.RuneError && size <= 1 {
			// Not valid UTF-8. A terminal sending it means a paste of binary or a
			// half-delivered rune; dropping the byte keeps the buffer text.
			in = in[1:]
			c.dropped = true
			continue
		}
		in = in[size:]

		if c.armed {
			c.armed = false
			if r == DetachKey {
				return Detach
			}
			// ^\ followed by anything else is not a detach, and the pair is not
			// text either: inserting a control byte into a prompt would send it
			// to the agent.
			c.insert(r)
			continue
		}

		switch r {
		case KeyDetach:
			c.armed = true
		case KeySend:
			return Send
		case KeyDirect:
			return Direct
		case KeyRefresh:
			return Refresh
		case KeyReturn, KeyNewline:
			// Enter is a newline. This is the whole rule.
			c.insert('\n')
		case KeyBack, KeyBackAlt:
			if len(c.buf) > 0 {
				c.buf = c.buf[:len(c.buf)-1]
				c.full = false
			}
		case KeyKill:
			c.Clear()
		default:
			c.insert(r)
		}
	}
	return Nothing
}

// insert adds one rune, refusing what should not reach a session.
func (c *Composer) insert(r rune) {
	// Control characters other than newline are dropped rather than shown. A
	// terminal sends them constantly — arrow keys arrive as escape sequences —
	// and passing them through would put escape codes inside a prompt.
	if r != '\n' && unicode.IsControl(r) {
		c.dropped = true
		return
	}
	if len(c.buf) >= MaxCompose {
		c.full = true
		return
	}
	c.buf = append(c.buf, r)
}

// Lines is the composed text as the pane draws it, always at least one line so the
// input row never collapses.
func (c *Composer) Lines() []string {
	if len(c.buf) == 0 {
		return []string{""}
	}
	return strings.Split(string(c.buf), "\n")
}
