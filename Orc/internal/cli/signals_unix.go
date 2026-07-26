//go:build unix

package cli

import (
	"os"
	"syscall"
)

// attachSignals are what an attached terminal listens for.
//
// SIGWINCH is the one that matters: the kernel sends it when the operator's
// terminal changes size, which is how a resize reaches the session without
// anybody polling for it.
func attachSignals() []os.Signal {
	return []os.Signal{syscall.SIGWINCH, syscall.SIGINT, syscall.SIGTERM}
}

// resized reports whether a signal means the terminal changed size, as opposed
// to meaning it is time to leave.
func resized(sig os.Signal) bool { return sig == syscall.SIGWINCH }
