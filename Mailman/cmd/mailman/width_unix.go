//go:build unix

package main

import (
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

// winsize is the struct TIOCGWINSZ fills in.
type winsize struct {
	rows, cols, xpixels, ypixels uint16
}

// terminalWidth asks the terminal how wide it is.
//
// COLUMNS wins when it is set, because a caller who exported it is overriding
// deliberately — and it is also how a test or a script pins the width without
// a terminal at all. Otherwise the ioctl answers, and any failure — a pipe, a
// file, a terminal that will not say — returns zero so the renderer falls back
// to its own default rather than laying out for a zero-width screen.
//
// The ioctl is raw rather than golang.org/x/sys, because Mailman is stdlib
// only, as Anno is.
func terminalWidth() int {
	if v, ok := os.LookupEnv("COLUMNS"); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}

	var ws winsize
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		os.Stdout.Fd(),
		uintptr(syscall.TIOCGWINSZ),
		uintptr(unsafe.Pointer(&ws)),
	)
	if errno != 0 || ws.cols == 0 {
		return 0
	}
	return int(ws.cols)
}
