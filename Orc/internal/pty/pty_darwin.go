//go:build darwin

package pty

import (
	"bytes"
	"os"
	"syscall"
	"unsafe"

	"orc/common/fault"
)

// The ioctls this platform names differently. Everything above is written against
// these two constants rather than against the platform's names.
const (
	getWinSize = syscall.TIOCGWINSZ
	setWinSize = syscall.TIOCSWINSZ
)

// openMaster allocates a pty and returns the master side with the slave's path.
//
// The darwin sequence is grant, unlock, then ask for the name — in that order. The
// name is only valid after the grant, so a version of this that read it first
// would work on a quiet machine and hand back somebody else's terminal on a busy
// one.
func openMaster() (*os.File, string, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, "", fault.IO{Op: "open", Path: "/dev/ptmx", Err: err}
	}

	fail := func(op string, cause error) (*os.File, string, error) {
		_ = master.Close()
		return nil, "", fault.IO{Op: op, Path: "/dev/ptmx", Err: cause}
	}

	if err := ioctl(master.Fd(), syscall.TIOCPTYGRANT, 0); err != nil {
		return fail("take ownership of the terminal from", err)
	}
	if err := ioctl(master.Fd(), syscall.TIOCPTYUNLK, 0); err != nil {
		return fail("unlock the terminal from", err)
	}

	// TIOCPTYGNAME writes a NUL-terminated path into a fixed 128-byte buffer.
	var buf [128]byte
	if err := ioctlPtr(master.Fd(), syscall.TIOCPTYGNAME, uintptr(unsafe.Pointer(&buf[0]))); err != nil {
		return fail("read the terminal name from", err)
	}
	name := string(buf[:bytes.IndexByte(buf[:], 0)])
	if name == "" {
		return fail("read the terminal name from", syscall.EINVAL)
	}
	return master, name, nil
}

func getAttr(fd uintptr) (syscall.Termios, error) {
	var t syscall.Termios
	err := ioctlPtr(fd, syscall.TIOCGETA, uintptr(unsafe.Pointer(&t)))
	return t, err
}

func setAttr(fd uintptr, t syscall.Termios) error {
	// TIOCSETA applies immediately. The draining variants (TIOCSETAW, TIOCSETAF)
	// wait for output to flush, which on a busy session is a stall at exactly the
	// moment somebody is trying to get their terminal back.
	return ioctlPtr(fd, syscall.TIOCSETA, uintptr(unsafe.Pointer(&t)))
}

// ioctl and ioctlPtr are the two shapes this package needs: a bare request, and a
// request carrying a pointer to a struct.
func ioctl(fd, request, arg uintptr) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, arg); errno != 0 {
		return errno
	}
	return nil
}

func ioctlPtr(fd, request, arg uintptr) error { return ioctl(fd, request, arg) }

// unsafePointer keeps the one unsafe import in the platform files rather than in
// the shared one, so the portable half of this package has none.
func unsafePointer[T any](v *T) unsafe.Pointer { return unsafe.Pointer(v) }
