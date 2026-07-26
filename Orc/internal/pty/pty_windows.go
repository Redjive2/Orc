// The Windows console. It is not a pseudo-terminal pair, and pretending it is
// would be the lie that makes every failure above here confusing.
//
// What works here is everything about the terminal Orc is *running in*: its
// size, and putting it into raw mode so `orc attach --direct` can hand every
// keystroke through. Those are ordinary console calls and they behave exactly as
// their unix counterparts do.
//
// What does not work is opening a pseudo-terminal for a child. Windows has one —
// the pseudoconsole, ConPTY — but a child can only be given it at creation time,
// through PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE on a process-thread attribute
// list, and the standard library's os/exec has no way to set one. Supporting it
// means calling CreateProcessW directly and rebuilding process startup around
// it, which is a different piece of work from making Orc run here.
//
// So Open refuses, and says so. A session cannot be supervised on Windows yet;
// everything else Orc does can.

package pty

import (
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"orc/common/fault"
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleScreenBufferInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
	procSetConsoleMode             = kernel32.NewProc("SetConsoleMode")
)

// Console modes. The standard library names GetConsoleMode but none of the bits.
const (
	enableProcessedInput    = 0x0001
	enableLineInput         = 0x0002
	enableEchoInput         = 0x0004
	enableVirtualTerminalIn = 0x0200
)

// Pty is the pair Orc would hold. It is never populated here — Open refuses —
// but the type exists so that everything above this package compiles and reads
// the same on every platform.
type Pty struct {
	Master *os.File
	Slave  *os.File
	Name   string
}

// Open would allocate a pseudo-terminal. On Windows it refuses.
//
// The refusal names the thing that is missing rather than the platform, because
// "Windows is not supported" is not something anybody can act on and "os/exec
// cannot hand a child a pseudoconsole" is.
func Open() (*Pty, error) {
	return nil, fault.Usage{Reason: "orc cannot supervise a session on Windows yet: a child here is " +
		"given a pseudoconsole only at creation, through a process attribute the standard library cannot " +
		"set — run the session on a unix machine and reach it from here with `orc attach`"}
}

// Close closes whatever is open. Nothing is, but a caller that unwinds through
// here should not have to know that.
func (p *Pty) Close() error {
	var first error
	for _, f := range []*os.File{p.Slave, p.Master} {
		if f == nil {
			continue
		}
		if err := f.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// CloseSlave closes the parent's copy of the child's side.
func (p *Pty) CloseSlave() error {
	if p.Slave == nil {
		return nil
	}
	err := p.Slave.Close()
	p.Slave = nil
	if err != nil {
		return fault.IO{Op: "close the child side of", Path: p.Name, Err: err}
	}
	return nil
}

// Attach points a command at the child side of the pty.
//
// Unreachable: nothing gets a Pty here, because Open refuses. It is left doing
// nothing rather than panicking, because a nil-safe no-op is the right answer
// for a shape that cannot occur.
func (p *Pty) Attach(cmd *exec.Cmd) {
	if p == nil || p.Slave == nil {
		return
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = p.Slave, p.Slave, p.Slave
}

// Resize sets a pseudoconsole's size. There are none here, so it refuses for the
// same reason Open does.
func Resize(f *os.File, size WinSize) error {
	if f == nil {
		return fault.Internal{Where: "pty.Resize", Detail: "no terminal given"}
	}
	_ = size
	return fault.Usage{Reason: "there is no session terminal to resize on Windows: orc cannot open a " +
		"pseudoconsole for a child here — resize the terminal you are attached from and the session follows it"}
}

// consoleInfo mirrors CONSOLE_SCREEN_BUFFER_INFO. Only the window rectangle is
// read, but the whole struct has to be the right size for the call to fill it.
type consoleInfo struct {
	size              coord
	cursorPosition    coord
	attributes        uint16
	window            smallRect
	maximumWindowSize coord
}

type coord struct{ X, Y int16 }

type smallRect struct{ Left, Top, Right, Bottom int16 }

// Size reads the console's window size.
//
// The *window* and not the buffer: a console's buffer is usually taller than
// what is on screen — that is what the scrollback is — and a TUI told it has
// nine thousand rows would draw off the bottom of the world.
func Size(f *os.File) (WinSize, error) {
	if f == nil {
		return WinSize{}, fault.Internal{Where: "pty.Size", Detail: "no terminal given"}
	}
	var info consoleInfo
	ret, _, errno := procGetConsoleScreenBufferInfo.Call(f.Fd(), uintptr(unsafe.Pointer(&info)))
	if ret == 0 {
		return WinSize{}, fault.IO{Op: "read the size of", Path: f.Name(), Err: errno}
	}
	return WinSize{
		Rows: uint16(info.window.Bottom - info.window.Top + 1),
		Cols: uint16(info.window.Right - info.window.Left + 1),
	}, nil
}

// Restorer puts a console back the way it was found.
type Restorer struct {
	handle syscall.Handle
	saved  uint32
	done   bool
}

// MakeRaw puts the console into raw mode and returns what undoes it.
//
// The same bargain as termios, spelled in this platform's bits: line editing,
// echo and the console's own handling of Ctrl-C go off, and virtual-terminal
// input goes on so that arrow keys and the rest arrive as the escape sequences a
// terminal program expects rather than as console key events.
//
// Restoring it is a correctness requirement rather than a courtesy. A command
// that exited without restoring would leave the operator's console with no echo
// and no line editing, which looks exactly like a hung machine.
func MakeRaw(f *os.File) (*Restorer, error) {
	if f == nil {
		return nil, fault.Internal{Where: "pty.MakeRaw", Detail: "no terminal given"}
	}
	handle := syscall.Handle(f.Fd())

	var saved uint32
	if err := syscall.GetConsoleMode(handle, &saved); err != nil {
		return nil, fault.IO{Op: "read the mode of", Path: f.Name(), Err: err}
	}

	raw := saved &^ (enableEchoInput | enableLineInput | enableProcessedInput)
	raw |= enableVirtualTerminalIn

	if ret, _, errno := procSetConsoleMode.Call(uintptr(handle), uintptr(raw)); ret == 0 {
		return nil, fault.IO{Op: "set the mode of", Path: f.Name(), Err: errno}
	}
	return &Restorer{handle: handle, saved: saved}, nil
}

// Restore puts the console back. It is idempotent, so a deferred call and a
// signal handler can both run it without the second undoing anything.
func (r *Restorer) Restore() error {
	if r == nil || r.done {
		return nil
	}
	r.done = true
	if ret, _, errno := procSetConsoleMode.Call(uintptr(r.handle), uintptr(r.saved)); ret == 0 {
		return fault.IO{Op: "restore the terminal mode of", Path: "the console", Err: errno}
	}
	return nil
}
