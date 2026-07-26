//go:build linux

package pty

import (
	"os"
	"strconv"
	"syscall"
	"unsafe"

	"orc/common/fault"
)

const (
	getWinSize = syscall.TIOCGWINSZ
	setWinSize = syscall.TIOCSWINSZ
)

// openMaster allocates a pty and returns the master side with the slave's path.
//
// The linux sequence is unlock, then ask for the number: the slave is
// /dev/pts/<n>, and it cannot be opened until TIOCSPTLCK has cleared the lock.
func openMaster() (*os.File, string, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, "", fault.IO{Op: "open", Path: "/dev/ptmx", Err: err}
	}

	fail := func(op string, cause error) (*os.File, string, error) {
		_ = master.Close()
		return nil, "", fault.IO{Op: op, Path: "/dev/ptmx", Err: cause}
	}

	unlock := int32(0)
	if err := ioctlPtr(master.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); err != nil {
		return fail("unlock the terminal from", err)
	}

	var n int32
	if err := ioctlPtr(master.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); err != nil {
		return fail("read the terminal number from", err)
	}
	return master, "/dev/pts/" + strconv.Itoa(int(n)), nil
}

func getAttr(fd uintptr) (syscall.Termios, error) {
	var t syscall.Termios
	err := ioctlPtr(fd, syscall.TCGETS, uintptr(unsafe.Pointer(&t)))
	return t, err
}

func setAttr(fd uintptr, t syscall.Termios) error {
	// TCSETS applies immediately, for the same reason darwin uses TIOCSETA rather
	// than the draining variants.
	return ioctlPtr(fd, syscall.TCSETS, uintptr(unsafe.Pointer(&t)))
}

func ioctl(fd, request, arg uintptr) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, arg); errno != 0 {
		return errno
	}
	return nil
}

func ioctlPtr(fd, request, arg uintptr) error { return ioctl(fd, request, arg) }

func unsafePointer[T any](v *T) unsafe.Pointer { return unsafe.Pointer(v) }
