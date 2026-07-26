//go:build unix

package proc

import (
	"os/exec"
	"syscall"
)

// Alive reports whether a pid exists.
//
// Signal 0 is the portable way to ask. EPERM counts as alive: it means the
// process is there and belongs to somebody else, which for Orc's purposes — is
// there something holding this session? — is a yes.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// Detach starts a command in a session of its own.
//
// It is what stops a Ctrl-C in the terminal that ran `orc employ` from reaching
// the fleet it just started: the supervisor outlives the command that spawned
// it, so it must not share its terminal's fate.
func Detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}

// Stop asks one process to leave.
func Stop(pid int) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

// StopGroup asks a process and everything it started to leave.
//
// The whole group, because Claude starts helpers of its own and a SIGTERM to the
// leader alone can leave them behind holding the terminal.
func StopGroup(pid int) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(-pid, syscall.SIGTERM)
}

// KillGroup ends a process and everything it started, without asking.
func KillGroup(pid int) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(-pid, syscall.SIGKILL)
}
