// Package keys reads single keystrokes from a terminal.
//
// It exists for the commands that sit in the foreground doing something on a
// timer and want a key to mean "do it now" — `cq pace` is the first. Those need
// three things a normal read does not give: a keystroke without waiting for a
// line, no echo of the key itself, and — the one that actually bites — the
// terminal's own handling of the interesting keys switched off.
//
// That last one is why this is a package rather than four lines at a call site.
// The keys somebody reaches for are exactly the ones a terminal has already
// claimed:
//
//   - **^S is XOFF.** In the default line discipline it stops output. A program
//     that "handles ^S" without turning off IXON has instead frozen its own
//     terminal, and the operator's next keystrokes go nowhere visible.
//   - **^W is WERASE**, which deletes the word behind the cursor.
//   - **^T is VSTATUS** on BSD and macOS, which prints a load average from the
//     kernel — into the middle of whatever was on screen.
//
// So the mode this sets is cbreak rather than raw: canonical input, echo, and the
// extended functions are off, and everything else is left alone. In particular
// **signals stay on**. A long-running foreground cycle that swallowed ^C would be
// a process an operator has to hunt for a pid to stop, and no key worth binding is
// worth that.
//
// Restoring is a correctness requirement, not a courtesy. A command that exits
// without it leaves a shell with no echo and no line editing, which looks exactly
// like a hung machine — so Restore is idempotent and safe to call from both a
// defer and a signal handler.
package keys

import (
	"errors"
	"io"
	"os"
)

// The keys this is for, as the bytes a terminal delivers.
const (
	CtrlC = 0x03
	CtrlS = 0x13
	CtrlT = 0x14
	CtrlW = 0x17
	CtrlD = 0x04
)

// ErrNotATerminal is returned when the input is a pipe, a file, or anything else
// with no line discipline to change.
//
// Its own error because it is not a failure for most callers. A cycle told to run
// with its input redirected should keep running and simply have no keys — which is
// what happens on a service, in a container, and under every test — so the caller
// decides, rather than this refusing to start something that works perfectly well
// without a keyboard.
var ErrNotATerminal = errors.New("not a terminal")

// Reader delivers one keystroke at a time.
type Reader struct {
	in      *os.File
	restore func() error
	done    bool
}

// Open puts a terminal into cbreak mode and returns a reader over it.
//
// The caller must Close it. A nil file, or one that is not a terminal, returns
// ErrNotATerminal and changes nothing.
func Open(in *os.File) (*Reader, error) {
	if in == nil {
		return nil, ErrNotATerminal
	}
	restore, err := cbreak(in)
	if err != nil {
		return nil, err
	}
	return &Reader{in: in, restore: restore}, nil
}

// Read returns the next keystroke.
//
// One byte, not a rune: every key this exists for is a control code, and decoding
// UTF-8 to answer "was that ^S" would be work in aid of nothing. A key this
// package has no name for is returned as it arrived and the caller ignores it.
func (r *Reader) Read() (byte, error) {
	if r == nil || r.done {
		return 0, io.EOF
	}
	var b [1]byte
	n, err := r.in.Read(b[:])
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, io.EOF
	}
	return b[0], nil
}

// Close puts the terminal back. Idempotent: a deferred call and a signal handler
// may both run it.
func (r *Reader) Close() error {
	if r == nil || r.done {
		return nil
	}
	r.done = true
	if r.restore == nil {
		return nil
	}
	return r.restore()
}

// Name spells a keystroke for a message.
func Name(b byte) string {
	switch b {
	case CtrlC:
		return "^C"
	case CtrlS:
		return "^S"
	case CtrlT:
		return "^T"
	case CtrlW:
		return "^W"
	case CtrlD:
		return "^D"
	}
	return ""
}
