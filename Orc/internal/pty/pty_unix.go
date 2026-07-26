//go:build unix

// The unix pseudo-terminal: a master/slave pair from the kernel, and termios for
// raw mode. Both are what the platform gives; see pty_windows.go for the shape
// Windows has instead, which is not the same shape at all.
package pty

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"orc/common/fault"
)

// Pty is an open pseudo-terminal pair.
//
// Master is the side Orc holds: what it writes appears as keystrokes to the
// child, and what the child prints comes back out of it. Slave is the side the
// child gets as its stdin, stdout, and stderr — and as its controlling terminal,
// which is what makes signals and window-size changes reach it.
type Pty struct {
	Master *os.File
	Slave  *os.File
	// Name is the slave's device path, kept for diagnostics: a session whose pty
	// has gone is much easier to reason about when the log says which one it was.
	Name string
}

// Open allocates a pseudo-terminal.
//
// Both sides are returned open. The caller closes the slave once the child has it
// — while the parent holds it open, a read on the master never reports EOF, so a
// supervisor that forgot would wait forever for a child that had already gone.
func Open() (*Pty, error) {
	master, name, err := openMaster()
	if err != nil {
		return nil, err
	}

	slave, err := os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()
		return nil, fault.IO{Op: "open the terminal at", Path: name, Err: err}
	}
	return &Pty{Master: master, Slave: slave, Name: name}, nil
}

// Close closes both sides. It is safe on a partly-closed pair.
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

// CloseSlave closes the parent's copy of the child's side, which is what lets a
// read on the master see the child exit.
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

// Attach points a command at the child side of the pty and makes it the leader of
// its own session with that terminal as its controlling one.
//
// Setsid plus Setctty is what makes the child a real terminal program rather than
// a process that merely has terminal-shaped file descriptors: without it, Ctrl-C
// in an attached terminal would signal Orc's own process group, and the child
// would never see a window-size change.
func (p *Pty) Attach(cmd *exec.Cmd) {
	cmd.Stdin, cmd.Stdout, cmd.Stderr = p.Slave, p.Slave, p.Slave
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true
}

// Resize sets a pty's window size and signals the child.
//
// The kernel sends SIGWINCH to the child's process group on this ioctl, which is
// what makes the TUI redraw. Orc does not send that signal itself, and must not:
// signalling the child directly would reach a process group Orc is not in.
func Resize(f *os.File, size WinSize) error {
	if f == nil {
		return fault.Internal{Where: "pty.Resize", Detail: "no terminal given"}
	}
	if size.Rows == 0 || size.Cols == 0 {
		size = Sane()
	}
	if err := ioctlPtr(f.Fd(), uintptr(setWinSize), uintptr(unsafePointer(&size))); err != nil {
		return fault.IO{Op: "resize", Path: f.Name(), Err: err}
	}
	return nil
}

// Size reads a terminal's window size, which is how `orc attach` learns how big
// the operator's terminal is before it proxies anything into it.
func Size(f *os.File) (WinSize, error) {
	if f == nil {
		return WinSize{}, fault.Internal{Where: "pty.Size", Detail: "no terminal given"}
	}
	var size WinSize
	if err := ioctlPtr(f.Fd(), uintptr(getWinSize), uintptr(unsafePointer(&size))); err != nil {
		return WinSize{}, fault.IO{Op: "read the size of", Path: f.Name(), Err: err}
	}
	return size, nil
}

// Restorer puts a terminal back the way it was found.
type Restorer struct {
	fd    uintptr
	saved syscall.Termios
	done  bool
}

// MakeRaw puts a terminal into raw mode and returns what undoes it.
//
// Raw is what `--direct` needs: every keystroke reaches the session unprocessed,
// including the ones the local terminal would otherwise act on itself. That makes
// restoring it a correctness requirement rather than a courtesy — a command that
// exited without restoring would leave the operator's shell with no echo and no
// line editing, which looks exactly like a hung machine.
func MakeRaw(f *os.File) (*Restorer, error) {
	if f == nil {
		return nil, fault.Internal{Where: "pty.MakeRaw", Detail: "no terminal given"}
	}

	saved, err := getAttr(f.Fd())
	if err != nil {
		return nil, fault.IO{Op: "read the mode of", Path: f.Name(), Err: err}
	}

	raw := saved
	// The classic cfmakeraw set, spelled out rather than borrowed, so that what is
	// switched off is readable: input translation and echo, output post-processing,
	// and the signal-generating and line-editing behaviour of the line discipline.
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP |
		syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	raw.Oflag &^= syscall.OPOST
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	// A read returns as soon as one byte is there, and never blocks on a timer:
	// a keystroke that waited for a buffer to fill would make the session feel
	// laggy in a way that reads as the agent being slow.
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0

	if err := setAttr(f.Fd(), raw); err != nil {
		return nil, fault.IO{Op: "set the mode of", Path: f.Name(), Err: err}
	}
	return &Restorer{fd: f.Fd(), saved: saved}, nil
}

// Restore puts the terminal back. It is idempotent, so a deferred call and a
// signal handler can both run it without the second undoing anything.
func (r *Restorer) Restore() error {
	if r == nil || r.done {
		return nil
	}
	r.done = true
	if err := setAttr(r.fd, r.saved); err != nil {
		return fault.IO{Op: "restore the terminal mode of", Path: fmt.Sprintf("fd %d", r.fd), Err: err}
	}
	return nil
}
