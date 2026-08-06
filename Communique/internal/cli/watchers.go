package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"orc/common/watch"
	"orc/cq/internal/fault"
)

// Where a machine records the watchers it is running, and what to call the one an
// upgrade starts when it finds none.
//
// These live together because they are two halves of one promise: an upgrade will
// not leave a machine with nothing keeping its mirror current. The registry is how
// it finds out, and the defaults are what it does about it.

// watchers is the registry directory for an agent home.
//
// Beside the agent's own state rather than in a machine-wide place, so that a
// sandboxed run — a probe, a test, a second account — records into its own sandbox
// without anything having to know it is sandboxed. Two agents with different homes
// are two independent machines as far as this is concerned, which is exactly what
// they are.
func watchers(home string) string { return filepath.Join(home, "watchers") }

// DefaultWatch and DefaultWatchFor are what an upgrade starts when it finds
// nothing watching this machine.
//
// Five minutes because that is the interval the setup instructions have always
// given, and a machine that has just been upgraded should behave like one somebody
// set up rather than like a special case.
//
// One hour, and not for ever, because nobody asked for this watcher. It exists so
// that a machine upgraded from the browser keeps reporting for long enough that
// the operator can see the upgrade landed and start a real one — not so that an
// unattended `cq sync` accumulates on every machine that was ever upgraded. A
// watcher nobody started should not outlive the reason it was started.
//
// The hour restarts if the watcher re-execs into a new build, because a restart
// reproduces the command line and the lifetime is part of it. That is left alone
// rather than carried across as a deadline: a machine being upgraded is a machine
// somebody is working on, and the reason this watcher exists — keeping the mirror
// current across upgrades — is still true each time.
const (
	DefaultWatch    = 5 * time.Minute
	DefaultWatchFor = time.Hour
)

// selfArgs is this process's own arguments, without the program name.
//
// Read from os.Args rather than rebuilt from parsed flags, because what a restart
// has to reproduce is the command somebody actually ran — including the flags this
// build does not know about, which is precisely the case when the new binary knows
// one the old one did not.
func selfArgs() []string {
	if len(os.Args) < 2 {
		return nil
	}
	return append([]string(nil), os.Args[1:]...)
}

// round trims a duration to something worth reading on a status line.
func round(d time.Duration) string {
	switch {
	case d >= time.Hour:
		return d.Round(time.Minute).String()
	default:
		return d.Round(time.Second).String()
	}
}

// ensureWatch is the promise an upgrade keeps: this machine is still being
// mirrored when the upgrade is over.
//
// There are two ways that is already true and one way it is not.
//
// It is true if a `cq sync --watch` is running, including *this* process — a
// watcher that applied the upgrade itself is the ordinary case, and it restarts
// into the new build on its own between rounds. Starting a second one beside it
// would double every machine's sync traffic for no reason.
//
// It is not true when the upgrade arrived through a one-shot `cq sync` — a cron
// line, a nudge, somebody at a terminal. That process is about to exit, and when
// it does nothing is left mirroring this machine. The website then goes stale with
// no error anywhere: the upgrade succeeded, the queue drained, and the mirror
// simply stopped. So one is started, detached, with a life of its own.
func (a App) ensureWatch(p watchPlan) func() error {
	// Resolved now, before the upgrade runs, and not inside the closure.
	//
	// The closure is called *after* the build has replaced this binary. `go build
	// -o` unlinks the destination and creates a new file, so on Linux
	// `os.Executable` then reads `/proc/self/exe` as `…/cq (deleted)` and the spawn
	// fails — leaving nothing mirroring the machine, which is the one thing this
	// function exists to prevent. Reading the path first costs nothing and is true
	// either way.
	exe, exeErr := os.Executable()

	return func() error {
		if exeErr != nil {
			return fault.IO{Op: "find", Subject: "this executable", Err: exeErr}
		}
		needed, err := watchNeeded(p.Home)
		if err != nil {
			return err
		}
		if !needed {
			return nil
		}

		if err := watch.Spawn(exe, p.args()); err != nil {
			return err
		}

		// And then check that it is really there.
		//
		// Spawn returns as soon as the child has been started, which says nothing
		// about whether it stayed up: a watcher missing a setting exits in
		// milliseconds. Announcing one that has already died is worse than
		// announcing nothing, because the message is the only thing anybody will
		// read — the mirror then stops, and the last word on the subject was that
		// it had been taken care of.
		//
		// So the promise is verified against the registry the watcher writes
		// itself. Nothing else in this file trusts those files as evidence of a
		// live process; this does not either, which is why it asks Running rather
		// than looking for a file.
		if err := a.awaitWatcher(p.Home); err != nil {
			return err
		}
		a.tell("cq: nothing was mirroring this machine, so a %s watch was started for %s",
			round(DefaultWatch), round(DefaultWatchFor))
		return nil
	}
}

// awaitWatcher waits for a spawned watcher to say it is running.
//
// The window is short because the thing being waited for is early: a watcher
// registers before its first round, so it is either up within a moment or it is
// not coming up at all.
func (a App) awaitWatcher(home string) error {
	const (
		patience = 3 * time.Second
		interval = 100 * time.Millisecond
	)
	deadline := time.Now().Add(patience)
	for {
		running, err := watch.Registry{Dir: watchers(home)}.Running(watch.Sync)
		if err != nil {
			return err
		}
		if running {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fault.Conflict{Subject: "the mirror", Reason: fmt.Sprintf(
				"a watch was started and stopped again within %s, so nothing is mirroring "+
					"this machine; run `cq sync --watch %s` by hand to see why",
				round(patience), round(DefaultWatch))}
		}
		time.Sleep(interval)
	}
}

// watchNeeded reports whether this machine has nothing mirroring it.
//
// Split from ensureWatch so the decision can be tested without starting a real
// process: the answer is the whole policy, and spawning is just what follows from
// it.
func watchNeeded(home string) (bool, error) {
	running, err := watch.Registry{Dir: watchers(home)}.Running(watch.Sync)
	if err != nil {
		return false, err
	}
	return !running, nil
}

// watchPlan is what the watch an upgrade starts needs in order to be the same
// sync as the one that started it.
//
// Every field is a *resolved* value, and that is the whole point. A sync is
// configured by flags as often as by variables, and the child inherits only the
// variables — so a machine synced with `cq sync --server https://… --machine
// studio` would spawn a watcher with neither, which fails before its first round
// and leaves nothing mirroring. Passing them explicitly costs four strings and is
// the difference between the feature working and only appearing to.
//
// It is the same reasoning as the service unit in service.go: what a process is
// started with later must be written down now, because the environment it is
// started in will not be this one.
type watchPlan struct {
	Home    string
	Server  string
	Machine string
	User    string
	Library string
}

// args is the command line for the watch.
func (p watchPlan) args() []string {
	args := []string{"sync",
		"--watch", DefaultWatch.String(),
		"--for", DefaultWatchFor.String(),
		"--home", p.Home,
	}
	// Only what was actually resolved. An empty `--user ""` is not the same as
	// leaving it out: the second lets the mirror ladder ask Orc who the operator
	// is, and the first is a name that is not anybody.
	for _, opt := range []struct{ flag, value string }{
		{"--server", p.Server},
		{"--machine", p.Machine},
		{"--user", p.User},
		{"--library", p.Library},
	} {
		if opt.value != "" {
			args = append(args, opt.flag, opt.value)
		}
	}
	return args
}
