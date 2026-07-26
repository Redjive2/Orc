//go:build unix

package cli_test

import (
	"os"
	"syscall"
)

// raiseResize tells this process its terminal changed size, the way a window
// manager would.
func raiseResize() error { return syscall.Kill(os.Getpid(), syscall.SIGWINCH) }
