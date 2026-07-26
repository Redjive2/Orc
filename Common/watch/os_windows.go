//go:build windows

package watch

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

// The access right that answers "is this pid still running" and nothing else.
// Asking for more than the question needs is how a liveness check turns into an
// access-denied on a process owned by somebody else.
const queryLimitedInformation = 0x1000

// stillActive is what GetExitCodeProcess reports for a process that has not
// exited. Windows spells "no exit code yet" as a reserved exit code, which is why
// a running process and one that exited with 259 are indistinguishable here — a
// wrinkle worth knowing about and not worth working around for this.
const stillActive = 259

// The two creation flags that together take a child off this console and out of
// this process group, which is as close as Windows gets to a detached session.
const (
	detachedProcess = 0x00000008
	newProcessGroup = 0x00000200
)

// Alive reports whether a pid is a process that is really there.
//
// See the unix file for why this is here rather than shared with Orc's
// internal/proc: an internal package of another module cannot be imported.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := syscall.OpenProcess(queryLimitedInformation, false, uint32(pid))
	if err != nil {
		// Access denied means something is there to be denied access to.
		return errors.Is(err, syscall.ERROR_ACCESS_DENIED)
	}
	defer func() { _ = syscall.CloseHandle(handle) }()

	var code uint32
	if err := syscall.GetExitCodeProcess(handle, &code); err != nil {
		return true // the handle opened, so something is there
	}
	return code == stillActive
}

// execSelf cannot be done on Windows, and says so rather than pretending.
//
// There is no exec: a process cannot become another program in place, and the
// nearest equivalent — start a replacement and exit — is a different thing with
// different consequences (a new pid, a broken parent relationship, a gap where
// nothing is watching). Doing that silently under a function named for exec would
// be the worst of the three options.
//
// It costs less than it looks. A running .exe cannot be replaced on Windows, so
// the build that would have prompted this restart fails before it gets here, and
// the operator is told by the upgrade rather than by a watcher.
// It is a plain error rather than one of fault's kinds because no caller exits on
// it: a watcher that cannot restart carries on watching, correctly, on the build
// it has. An exit code would be claiming this ended something, and it does not.
func execSelf(exe string, _, _ []string) error {
	return fmt.Errorf("windows cannot replace a running program in place; "+
		"stop and start %s to pick up the new build", exe)
}

// detach starts a watcher with no console and a process group of its own.
//
// The nearest thing Windows has to setsid, and it buys the same thing: the
// watcher must not be taken down by a console event aimed at whatever started it.
func detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= detachedProcess | newProcessGroup
}
