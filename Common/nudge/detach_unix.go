//go:build unix

package nudge

import (
	"os/exec"
	"syscall"
)

// detach puts the child in its own session.
//
// Without this the nudge shares the caller's process group, so a Ctrl-C aimed at
// `mailman send` — or the shell reaping the group when the command returns —
// kills the sync partway through. A half-finished sync is recoverable, but it
// leaves the website stale for no reason.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
