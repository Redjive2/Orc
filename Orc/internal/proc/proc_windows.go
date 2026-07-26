package proc

import (
	"errors"
	"os/exec"
	"strconv"
	"syscall"

	"orc/common/fault"
)

// Windows access rights and flags the standard library does not name.
const (
	queryLimitedInformation = 0x1000
	detachedProcess         = 0x0008
	// The exit code a process reports while it is still running. It is a real
	// exit code too — a process that returns 259 is indistinguishable from a
	// running one — which is a documented wart of the API rather than something
	// to work around here. The pid in question is a supervisor Orc started, and
	// guessing wrong costs a session reported as held a moment longer than it
	// was.
	stillActive = 259
)

// Alive reports whether a pid exists.
//
// Windows has no signals to ask with, so the question is put directly: open the
// process for the smallest right there is, and read its exit code.
//
// A refusal counts as alive for the same reason EPERM does on unix — the process
// is there and belongs to somebody else, and the question being asked is whether
// anything is holding this session.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := syscall.OpenProcess(queryLimitedInformation, false, uint32(pid))
	if err != nil {
		return errors.Is(err, syscall.ERROR_ACCESS_DENIED)
	}
	defer func() { _ = syscall.CloseHandle(handle) }()

	var code uint32
	if err := syscall.GetExitCodeProcess(handle, &code); err != nil {
		return true // the handle opened, so something is there
	}
	return code == stillActive
}

// Detach starts a command with no console and a process group of its own.
//
// This is the nearest thing Windows has to setsid, and it buys the same thing:
// the supervisor outlives the command that spawned it, so a Ctrl-C in the
// terminal that ran `orc employ` must not reach it. DETACHED_PROCESS is what
// takes it off that console; the new group is what keeps console control events
// from finding it if it ever gains one.
func Detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess
}

// Stop ends one process.
//
// **It is not polite, because Windows has no polite version of this.** A console
// control event is the closest equivalent and it can only reach a process that
// shares a console — which a detached supervisor, by construction, does not. So
// this terminates, and the process gets no chance to tidy up.
//
// Orc reaches here only when a session is already half torn down: its socket has
// gone but its supervisor is still running, so there is nothing left to ask
// politely *through*. Terminating is the right answer to that on any platform;
// it just happens to be the only answer here.
func Stop(pid int) error {
	if pid <= 0 {
		return nil
	}
	handle, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return fault.IO{Op: "open the process to stop", Path: pidPath(pid), Err: err}
	}
	defer func() { _ = syscall.CloseHandle(handle) }()

	if err := syscall.TerminateProcess(handle, 1); err != nil {
		return fault.IO{Op: "stop", Path: pidPath(pid), Err: err}
	}
	return nil
}

// StopGroup ends a process. **It does not reach the children**, which is the one
// difference from unix that a caller has to know about.
//
// Reaching a whole tree on Windows means putting it in a job object when it is
// created and terminating that, which is a decision that belongs at spawn time
// rather than here. Orc does not make it yet, because a session cannot be
// supervised on Windows at all — see internal/pty — so nothing reaches this with
// children to leave behind.
func StopGroup(pid int) error { return Stop(pid) }

// KillGroup ends a process without asking. On Windows there is no difference
// between this and StopGroup: both terminate, because that is all there is.
func KillGroup(pid int) error { return Stop(pid) }

func pidPath(pid int) string { return "pid " + strconv.Itoa(pid) }
