package cli

import (
	"os"
	"path/filepath"
	"time"

	"orc/common/watch"
)

// Orc's two long-running loops, and what makes them survive an upgrade.
//
// `orc wake --every` and `orc tend --watch` are the fleet's other heartbeat: one
// keeps agents moving and the other keeps them running. Neither is supervised,
// because neither serves anything — they are loops somebody started and left.
//
// Which is why an upgrade is their problem in particular. Replacing a binary on
// unix leaves the running process on the inode it started with, so a sweep started
// last week keeps sweeping with last week's code, indefinitely and silently. The
// fleet looks healthy, every binary on disk is current, and the two processes
// actually holding it together are the two that are not.
//
// So each loop takes a watchdog: it records that it is running, and between rounds
// — never during one — it notices its own binary has changed and re-execs into the
// new build, keeping its pid and its arguments.

// watchdog is one loop's registration and its knowledge of its own build.
type watchdog struct {
	app     App
	exe     string
	stamp   watch.Stamp
	release func()
}

// watching registers this process as a watcher of the given kind.
//
// A failure to register is complained about rather than returned: a sweep that
// cannot write a bookkeeping file is still a sweep, and refusing to run one
// because of it would stop the fleet's heartbeat over the thing meant to record
// it. The same goes for not being able to find this executable — that only costs
// the restart, and a loop running an old build beats no loop at all.
func (a App) watching(kind watch.Kind, period time.Duration, args []string) *watchdog {
	dog := &watchdog{app: a, release: func() {}}

	exe, stamp, err := watch.Own()
	if err != nil {
		a.note("cannot watch for new builds: %v", err)
		return dog
	}
	dog.exe, dog.stamp = exe, stamp

	root, err := a.root()
	if err != nil {
		a.note("cannot record this watcher: %v", err)
		return dog
	}

	started := time.Now()
	release, err := watch.Registry{Dir: filepath.Join(root, "watchers")}.Register(watch.Record{
		Kind: kind, Exe: exe, Args: args,
		Period: watch.Duration(period), Started: started,
	})
	if err != nil {
		a.note("cannot record this watcher: %v", err)
		return dog
	}
	dog.release = release
	return dog
}

// done removes this watcher's record.
func (w *watchdog) done() { w.release() }

// renew restarts into a new build when one has appeared, and otherwise returns.
//
// Called between rounds. A round half-applied when the process image changes
// underneath it is the one outcome worse than running an old build for another
// cycle, so this is never called during one.
func (w *watchdog) renew() {
	if w.exe == "" || !watch.Replaced(w.exe, w.stamp) {
		return
	}
	// Removed before the exec, not deferred: after exec there is no stack left to
	// unwind, and a record naming this pid would then describe the new image while
	// carrying the old one's claim. The process that comes back writes its own.
	w.release()
	w.release = func() {}
	w.app.note("restarting into the new build")

	handedOff, err := watch.Restart(w.exe, selfArgs())
	if handedOff {
		// Windows: the replacement is up, so this one is done. See watch.Restart.
		w.app.note("the new build is watching now; this one is standing down")
		os.Exit(0)
	}
	if err != nil {
		// Not fatal. Carrying on means the sweep keeps running on the build it has,
		// which is the whole point of the loop. The stamp moves on so that a build
		// this process cannot exec into is complained about once rather than every
		// round for ever.
		w.app.note("could not restart into the new build: %v", err)
		if now, err := watch.Look(w.exe); err == nil {
			w.stamp = now
		}
	}
}

// selfArgs is this process's own arguments, without the program name.
//
// Read from os.Args rather than rebuilt from parsed flags, because what a restart
// has to reproduce is the command somebody actually ran — including flags this
// build does not know about, which is exactly the case when the new binary
// understands one the old one did not.
func selfArgs() []string {
	if len(os.Args) < 2 {
		return nil
	}
	return append([]string(nil), os.Args[1:]...)
}
