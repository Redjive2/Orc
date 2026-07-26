//go:build unix

package watch

import (
	"os/exec"
	"syscall"
)

// Alive reports whether a pid is a process that is really there.
//
// Signal 0 is the ask-without-sending: the kernel does every check it would do
// for a real signal and delivers nothing. EPERM means a process exists that this
// user may not signal, which is still a live watcher — a record written by another
// account is a fact about the machine, and reading it as dead would start a second
// watcher beside the first.
//
// This duplicates Orc's internal/proc rather than sharing it, and not by
// preference: `Orc/internal/proc` is an internal package of another module, so
// Communique cannot import it at all. That is a rule of the language, not a
// judgement about the code, and the alternative — hoisting all of proc into
// Common — would move signal handling, process groups, and detaching for the sake
// of the one question asked here.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// execSelf replaces this process image. It does not return on success.
func execSelf(exe string, argv, env []string) error {
	return syscall.Exec(exe, argv, env)
}

// detach puts a spawned watcher in a session of its own.
//
// Without it the watcher shares the caller's process group, so a Ctrl-C aimed at
// the command that started it — or the shell reaping the group when that command
// returns — takes the watcher with it. Which would defeat the point entirely: the
// watcher exists precisely because the process starting it is about to end.
func detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}
