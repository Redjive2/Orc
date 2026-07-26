package theme

import (
	"os"
	"syscall"
)

// Preparing a Windows console to show what Orc prints.
//
// Two things are wrong by default, and both make the output look broken rather
// than plain — which is the opposite of what a colour layer is supposed to do
// when it cannot be shown.
//
//  1. **The output code page is not UTF-8.** A console decodes the bytes a
//     program writes using its code page, and the default is the machine's OEM
//     one — 437 on an English install. Every box rule, every `§`, every `▸` Orc
//     draws arrives as mojibake. This is the reason `chcp 65001` is folklore.
//  2. **Escape sequences are not interpreted** unless a console handle is put
//     into virtual-terminal mode. Without it the colour Orc emits is printed as
//     literal `←[38;2;…m` noise through the middle of every line.
//
// Both are asked for and neither is insisted on: a console that refuses is a
// console that was already in the right mode, or is not a console at all. The
// palette decision is made separately and still governs whether anything
// colourful is emitted in the first place.

// Windows constants that the standard library does not name.
const (
	utf8CodePage                 = 65001
	enableVirtualTerminalProcess = 0x0004
)

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleOutputCP = kernel32.NewProc("SetConsoleOutputCP")
	procSetConsoleMode     = kernel32.NewProc("SetConsoleMode")
)

// PrepareConsole makes the terminal able to render what this process writes.
//
// It is called for its effect and reports nothing: there is no version of "the
// console would not take UTF-8" that a caller can act on, and a tool that
// refused to start over it would be worse than one that prints oddly.
func PrepareConsole(streams ...*os.File) {
	// The code page belongs to the console as a whole rather than to a handle,
	// so it is set once however many streams are passed.
	_, _, _ = procSetConsoleOutputCP.Call(uintptr(utf8CodePage))

	for _, f := range streams {
		if f == nil {
			continue
		}
		handle := syscall.Handle(f.Fd())
		var mode uint32
		if err := syscall.GetConsoleMode(handle, &mode); err != nil {
			continue // redirected to a file or a pipe; there is no mode to set
		}
		if mode&enableVirtualTerminalProcess != 0 {
			continue
		}
		_, _, _ = procSetConsoleMode.Call(uintptr(handle), uintptr(mode|enableVirtualTerminalProcess))
	}
}
