//go:build windows

package keys

import (
	"os"
	"syscall"
)

// The console mode bits this needs off. Windows has no line discipline; the
// equivalent claim on keystrokes is made by the console itself.
//
// ENABLE_LINE_INPUT is the canonical buffering — without clearing it, nothing
// arrives until Enter. ENABLE_ECHO_INPUT is the echo. ENABLE_PROCESSED_INPUT is
// left **on**, deliberately: it is what turns ^C into an interrupt, and the same
// argument applies here as to keeping ISIG on unix — a foreground cycle nobody
// can stop with ^C is one they need a pid for.
const (
	enableProcessedInput = 0x0001
	enableLineInput      = 0x0002
	enableEchoInput      = 0x0004
)

func cbreak(f *os.File) (func() error, error) {
	handle := syscall.Handle(f.Fd())
	var saved uint32
	if err := syscall.GetConsoleMode(handle, &saved); err != nil {
		// Not a console: a pipe, a file, a service with no window. Normal, and
		// not this package's business to refuse.
		return nil, ErrNotATerminal
	}

	mode := saved &^ uint32(enableLineInput|enableEchoInput)
	mode |= enableProcessedInput
	if err := setConsoleMode(handle, mode); err != nil {
		return nil, err
	}
	return func() error { return setConsoleMode(handle, saved) }, nil
}

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleMode = kernel32.NewProc("SetConsoleMode")
)

// setConsoleMode is the setter Go's syscall package leaves out: it declares
// GetConsoleMode and not its pair, so the call is made by hand.
func setConsoleMode(handle syscall.Handle, mode uint32) error {
	r, _, err := procSetConsoleMode.Call(uintptr(handle), uintptr(mode))
	if r == 0 {
		return err
	}
	return nil
}
