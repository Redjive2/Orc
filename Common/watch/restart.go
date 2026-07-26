package watch

import (
	"os"
	"os/exec"
	"time"

	"orc/common/fault"
)

// Stamp identifies the build a watcher is running.
//
// Size and modification time, not a digest. A watcher checks this between every
// round, for as long as it runs — hashing a twenty-megabyte binary every five
// minutes to answer a question that changes about once a week is work nobody
// asked for. The two together are wrong only if a rebuild produces a byte-identical
// binary at the same timestamp, and a rebuild that changed nothing is exactly the
// case where not restarting is the right answer.
type Stamp struct {
	Size int64
	Mod  time.Time
}

// Own is the running executable and the stamp it had when this was called.
//
// Called once, at the top of a loop, so that what a watcher compares against is
// the build it actually started with rather than whatever was on disk a moment
// ago. A failure is returned rather than swallowed: a watcher that cannot find its
// own binary cannot restart into a new one, and the operator should be told that
// now rather than discovering it after an upgrade that appeared to work.
func Own() (string, Stamp, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", Stamp{}, fault.IO{Op: "find", Path: "this executable", Err: err}
	}
	stamp, err := Look(exe)
	if err != nil {
		return "", Stamp{}, err
	}
	return exe, stamp, nil
}

// Look is the stamp of a file now.
func Look(path string) (Stamp, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Stamp{}, fault.IO{Op: "read", Path: path, Err: err}
	}
	return Stamp{Size: info.Size(), Mod: info.ModTime()}, nil
}

// Replaced reports whether the binary at this path is no longer the one stamped.
//
// A path that cannot be read is *not* replaced, deliberately. Mid-build the file
// can be absent for a moment, and a watcher that treated "gone" as "new" would
// exec a file that is not there, fail, and log about it every round of a build
// that is going perfectly well. The next round asks again, and by then the build
// has either finished or truly broken — and a truly broken build is a thing the
// upgrade itself reports, not something a watcher should be guessing at.
func Replaced(path string, was Stamp) bool {
	now, err := Look(path)
	if err != nil {
		return false
	}
	return now.Size != was.Size || !now.Mod.Equal(was.Mod)
}

// Restart replaces this process with a fresh copy of the binary at exe.
//
// It does not return when it works. `exec` overwrites the process image in place:
// same pid, same parent, same terminal, same file descriptors — so a watcher under
// launchd or in a `while` loop is not noticed to have gone anywhere, because it
// did not go anywhere. It is the same move `cq serve`'s supervisor makes, and the
// reason both can restart into a new build without anything supervising them.
//
// Windows has no exec, and Go's syscall.Exec there returns an error rather than
// pretending. That is the honest outcome and it is left to the caller, because on
// Windows the situation cannot arise as cleanly anyway: a running .exe cannot be
// overwritten, so the build that would have prompted this restart fails first, and
// loudly. A caller that gets an error here should say so and carry on with the
// build it has.
func Restart(exe string, args []string) error {
	// Resolved through the platform's rules rather than trusted as a path, for the
	// reason `cq serve`'s restartable() documents at length: on Windows a file with
	// no recognised extension cannot be started even by itself, and the error for
	// that names a path that is plainly right there. Asking first turns it into
	// something an operator can act on.
	if _, err := exec.LookPath(exe); err != nil {
		return fault.IO{Op: "start", Path: exe, Err: err}
	}
	return execSelf(exe, append([]string{exe}, args...), os.Environ())
}

// Spawn starts a watcher that outlives the process starting it.
//
// Detached, so that the shell which ran the one-shot command that started this —
// a cron line, a nudge, somebody's terminal — does not take the new watcher down
// with it when it returns. That is the whole point: the process calling this is
// about to exit, and the watcher must not.
//
// Its streams go to the null device rather than being inherited. A watcher that
// wrote to the parent's standard output would corrupt whatever that parent was
// printing, and one holding the stream open would hang a shell waiting on a pipe
// that nothing is going to close.
func Spawn(exe string, args []string) error {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fault.IO{Op: "open", Path: os.DevNull, Err: err}
	}
	// The parent does not wait, so it must not leave the only handle it needs to
	// close held open by a child it has stopped caring about.
	defer func() { _ = null.Close() }()

	cmd := exec.Command(exe, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	cmd.Env = os.Environ()
	detach(cmd)

	if err := cmd.Start(); err != nil {
		return fault.IO{Op: "start", Path: exe, Err: err}
	}
	// Deliberately no Wait. The child outlives this process and is reaped by init;
	// waiting is the one thing this function must not do.
	return nil
}

// Until is the moment a watcher with a time to live should stop, or nil for one
// that runs until it is stopped.
//
// A zero or negative lifetime means no expiry rather than an immediate one. The
// flag it comes from is absent far more often than it is set, and "unset" must be
// the harmless reading: a watcher that stopped the instant it started would be a
// mirror that silently never updated, reported as a success.
func Until(started time.Time, ttl time.Duration) *time.Time {
	if ttl <= 0 {
		return nil
	}
	at := started.Add(ttl)
	return &at
}
