//go:build darwin || linux

package keys

import (
	"os"
	"syscall"
	"unsafe"
)

// cbreak switches off the line discipline's claim on individual keys.
//
// Deliberately not cfmakeraw. Raw also clears ISIG and OPOST, and both matter
// here: a foreground cycle that swallowed ^C would need a pid to stop, and
// clearing OPOST means every "\n" this prints leaves the cursor in the middle of
// the line. What comes off is exactly what stands between a keypress and the
// program — canonical buffering, echo, the extended functions (^T's status line
// on BSD among them), and software flow control (^S).
func cbreak(f *os.File) (func() error, error) {
	fd := f.Fd()
	saved, err := getAttr(fd)
	if err != nil {
		// The one error worth translating: an fd with no line discipline is a
		// pipe or a file, which is a normal way to run and not a fault.
		return nil, ErrNotATerminal
	}

	mode := saved
	mode.Iflag &^= syscall.IXON | syscall.ICRNL | syscall.INLCR | syscall.ISTRIP
	mode.Lflag &^= syscall.ICANON | syscall.ECHO | syscall.ECHONL | syscall.IEXTEN
	// A read returns as soon as one byte is there and never waits on a timer. A
	// key that sat in a buffer until it had company would make the command feel
	// like it had missed the press.
	mode.Cc[syscall.VMIN] = 1
	mode.Cc[syscall.VTIME] = 0

	if err := setAttr(fd, mode); err != nil {
		return nil, err
	}
	return func() error { return setAttr(fd, saved) }, nil
}

func ioctlPtr(fd, request, arg uintptr) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, arg); errno != 0 {
		return errno
	}
	return nil
}

func setAttrWith(fd uintptr, set uintptr, t syscall.Termios) error {
	return ioctlPtr(fd, set, uintptr(unsafe.Pointer(&t)))
}

func getAttrWith(fd uintptr, get uintptr) (syscall.Termios, error) {
	var t syscall.Termios
	err := ioctlPtr(fd, get, uintptr(unsafe.Pointer(&t)))
	return t, err
}
