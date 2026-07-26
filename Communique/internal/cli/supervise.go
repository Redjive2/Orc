package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"orc/cq/internal/fault"
)

// The supervisor.
//
// A process cannot exec its own replacement and still be there to report on it, so
// something has to outlive the restart. That something is this: `cq serve` runs a
// child `cq serve` and starts a new one whenever the child asks.
//
// The supervisor is deliberately the smallest program in the tree. It opens no
// port, reads no store, and has no state beyond a pid — because it is the one
// process that does *not* get replaced during an upgrade, and every line in it is
// a line that keeps running on the old build until somebody restarts it by hand.
//
// Except that it does replace itself, once, at the end: after the child exits
// asking for a restart, the supervisor `exec`s its own path. That is the whole
// trick. `exec` replaces the process image in place — same pid, same parent, same
// terminal, no gap for anything to notice — so the supervisor that comes back is
// the *new* binary, and nothing was left running the old one.
//
// What it does not do is decide anything. It does not know what an upgrade is,
// cannot start one, and cannot be asked to. It restarts what it was running,
// which is the only thing a supervisor should be trusted with.

// The exit code a server uses to say "start me again".
//
// A number rather than a signal because it has to survive the process actually
// exiting: the server wants to finish its shutdown, flush, and close its listener
// *before* anything replaces it, and a signal would arrive while it was still
// serving. 75 is EX_TEMPFAIL from sysexits.h — "try again" — which is exactly the
// message, and is far from anything the runtime or the shell uses.
const ExitRestart = 75

// supervisedEnv marks the child, so `cq serve` in a supervised process runs the
// server rather than starting a third one. Without it a supervisor would fork a
// supervisor forever, which is the classic way this design goes wrong.
const supervisedEnv = "CQ_SUPERVISED"

// How a crash is told apart from a build that will not start.
//
// The distinction is time, because there is nothing else to go on: the exit codes
// are the same, and a server that dies after a week of serving and one that dies
// in 40ms are the same event to `wait`. A process that stayed up for a minute got
// past its listener, its store, and its first requests — so whatever killed it
// was a condition, not a defect in the build, and it is worth trying again.
//
// Five, because the backoff doubles: five quick failures is about six seconds of
// trying, which is long enough to ride out a port still held by the last process
// and short enough that a genuinely broken build is reported promptly rather than
// after a minute of silence.
const (
	Healthy     = time.Minute
	CrashGiveUp = 5
)

// What to do once a child has gone away.
type supervision int

const (
	// stop: leave it down. Somebody decided that, and a supervisor that argued
	// with a ^C would be a supervisor nobody could turn off.
	stop supervision = iota
	// relaunch: start the same binary again. A crash.
	relaunch
	// replace: start it again, then exec this process too. An upgrade.
	replace
	// giveUp: it will not stay up, and saying so is more use than trying again.
	giveUp
)

// crashed reports whether an exit was a failure to recover from, as opposed to
// something asked for.
//
// Split out because "was that a crash" is asked twice — once to count it and once
// to decide — and two copies of this condition drifting apart is how a supervisor
// ends up counting restarts as crashes or vice versa.
func crashed(stopping bool, code int) bool {
	return !stopping && code != 0 && code != ExitRestart
}

// decide is the whole policy, as a function of three facts.
//
// Pure, and separate from the loop, because the loop cannot be tested without
// real processes and real signals — and the part worth being sure about is not
// the plumbing but this: that a ^C stays down, that a clean exit stays down, that
// a crash comes back, and that a build which will not start is eventually left
// alone. Those are four sentences and they should be four assertions, not a test
// that sends itself SIGTERM and hopes.
func decide(stopping bool, code, crashes int) supervision {
	switch {
	case stopping:
		// The child's code is its own business here: a server signalled
		// mid-shutdown may exit non-zero, and that is not a failure of anything.
		return stop
	case code == 0:
		// A clean exit is a decision. Something asked the server to stop and it
		// did; bringing it back would override whoever asked.
		return stop
	case code == ExitRestart:
		return replace
	case crashes > CrashGiveUp:
		return giveUp
	default:
		return relaunch
	}
}

// supervise runs `cq serve` as a child and starts it again when it should be.
//
// Four outcomes, and the whole policy is telling them apart:
//
//   - **asked to stop** — a ^C or a SIGTERM reached this process. It returns, and
//     the server stays down, because that is what was asked for.
//   - **exited cleanly** — something told the server to stop and it did. Also a
//     decision, also returns.
//   - **asked to restart** — an upgrade. The child is started again, and the
//     supervisor then replaces itself so nothing is left on the old build.
//   - **crashed** — started again, with a backoff, up to CrashGiveUp consecutive
//     quick failures.
//
// The last of those used to return instead, deferring to a service manager. That
// is right where one is configured and leaves the site down where none is — and
// none is, anywhere in this tree. A supervisor whose one job is keeping a server
// up should not be the reason it stays down.
//
// What it still will not do is loop on a build that cannot start. That failure is
// fast, and fast repeated failures are counted and given up on, with the child's
// exit code passed up so a service manager — if there ever is one — sees a
// process that plainly did not work.
func (a App) supervise(exe string, args []string) error {
	// Signals are forwarded rather than acted on. A supervisor that died on ^C
	// while its child kept the port would be worse than no supervisor at all.
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)

	// A restart that fails immediately, over and over, is the failure mode this
	// has to survive: a bad build that will not start. Backing off turns a busy
	// loop into a log somebody can read, and the delay is short enough that a
	// healthy restart is not noticed.
	const (
		floor   = 200 * time.Millisecond
		ceiling = 30 * time.Second
	)
	backoff := floor

	// stopping records that the operator asked for this to end, so that a child
	// exiting non-zero because it was signalled is not mistaken for one that
	// crashed. Without it, ^C would bring the server straight back up — which is
	// the most obvious way an auto-restarting supervisor becomes unusable.
	stopping := false

	// crashes counts consecutive *quick* failures. A child that ran a while and
	// then died is a crash worth recovering from; one that dies immediately, again
	// and again, is a build that cannot start, and coming back for ever would bury
	// the reason under its own log.
	crashes := 0

	for {
		started := time.Now()
		child := exec.Command(exe, args...)
		child.Env = append(os.Environ(), supervisedEnv+"=1")
		child.Stdin, child.Stdout, child.Stderr = a.Stdin, a.Stdout, a.Stderr

		if err := child.Start(); err != nil {
			return fault.IO{Op: "start", Subject: exe, Err: err}
		}

		done := make(chan error, 1)
		go func() { done <- child.Wait() }()

		var code int
	waiting:
		for {
			select {
			case sig := <-signals:
				// Passed straight through. The child owns the port and the
				// shutdown; this only has to not get in the way.
				//
				// Except that it is noted first. A ^C or a `systemctl stop` means
				// the operator wants the server *down*, and whatever exit code the
				// child produces on its way out must not read as a crash to
				// recover from. SIGHUP is not in that set: it is forwarded, and
				// what the child makes of it is the child's business.
				if sig == syscall.SIGINT || sig == syscall.SIGTERM {
					stopping = true
				}
				_ = child.Process.Signal(sig)
			case err := <-done:
				code = exitCode(err)
				break waiting
			}
		}

		// A child that ran for a while is a healthy one, whatever it did next: an
		// upgrade that asked to restart, or a crash after a week of serving. Either
		// way the backoff and the crash count start again from nothing.
		healthy := time.Since(started) > Healthy
		if healthy {
			backoff = floor
			crashes = 0
		}

		if crashed(stopping, code) {
			crashes++
		}
		switch decide(stopping, code, crashes) {
		case stop:
			return nil
		case giveUp:
			a.tell("cq: the server failed %d times without staying up; giving up", crashes)
			return exitStatus(code)
		case replace:
			a.tell("cq: restarting into the new build")
		case relaunch:
			a.tell("cq: the server exited with status %d; restarting (%d of %d)",
				code, crashes, CrashGiveUp)
		}

		time.Sleep(backoff)
		if backoff < ceiling {
			backoff *= 2
		}

		// Only an upgrade replaces the supervisor itself. A crash is the same
		// binary failing, and exec'ing it would restart this process to no purpose
		// — losing the crash count that is the only thing stopping a loop.
		if code != ExitRestart {
			continue
		}

		// And finally: become the new binary. `exec` rather than another loop
		// iteration, so the supervisor itself stops running the code that was on
		// disk when it started. Same pid, so whatever is watching *this* process —
		// systemd, a terminal, a launchd job — sees nothing at all.
		//
		// A failure here is not fatal: carrying on with the loop means the site
		// comes back on the old supervisor and the new server, which is worse than
		// both being new and much better than nothing being up.
		if err := syscall.Exec(exe, append([]string{exe}, args...), os.Environ()); err != nil {
			a.tell("cq: could not replace the supervisor, carrying on with the old one: %v", err)
		}
	}
}

// exitStatus carries a child's exit code back to Main without a message. The
// child has already said whatever there was to say on its own stderr.
type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("the server exited with status %d", int(e)) }

// Status is what fault.Exit reads to pass a child's code through unchanged.
func (e exitStatus) Status() int { return int(e) }

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return 1
}

// restartable returns the path this process can start a copy of itself from, or
// says why it cannot.
//
// The check exists because `os/exec` resolves *every* command through the
// platform's rules, even one handed an absolute path it was told is an
// executable. On Windows those rules are PATHEXT, so a file whose name carries
// no recognised extension cannot be started at all — not even by itself. And
// `go build -o cq` writes exactly such a file: no extension is added when `-o`
// names the output, on any platform.
//
// The result is a binary a shell runs happily and `os/exec` will not touch, and
// the error it gives back is "executable file not found in %PATH%" naming a path
// that is plainly right there. Asking here turns that into something an operator
// can act on, and asks before the server starts rather than after the first
// upgrade tries to restart it.
func restartable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fault.IO{Op: "find", Subject: "this executable", Err: err}
	}
	if _, err := exec.LookPath(exe); err != nil {
		return "", fault.Usage{Reason: cannotStart(runtime.GOOS, exe)}
	}
	return exe, nil
}

// cannotStart explains a binary this process may not start, in the terms of
// whichever platform is refusing it.
//
// The platform is a parameter rather than read from runtime, so that the message
// somebody on Windows will actually read is pinned by a test that runs
// everywhere. It is the message that carries the fix, and it is the one thing
// here that cannot be checked by running the code on the machine that wrote it.
func cannotStart(goos, exe string) string {
	const shared = "cq cannot start a copy of itself from %s"
	if name := leaf(exe); goos == "windows" && !strings.Contains(name, ".") {
		return fmt.Sprintf(shared+
			": windows starts a program only through a name it recognises, and this one has no "+
			"extension — rename it to %s.exe and start it again", exe, name)
	}
	return fmt.Sprintf(shared+": it is not something this system will run — check that it is executable", exe)
}

// leaf is the last element of a path, spelled for either platform.
//
// `path/filepath` asks the *host* how paths are written, and this decides a
// message about Windows while possibly running somewhere else: a Windows path
// read on unix is one long element, and `.local` in the middle of it would look
// like the extension the binary is missing.
func leaf(path string) string {
	if i := strings.LastIndexAny(path, `\/`); i >= 0 {
		return path[i+1:]
	}
	return path
}

// supervised reports whether this process is the child of a supervisor.
func (a App) supervised() bool {
	v, ok := a.Env(supervisedEnv)
	return ok && v != ""
}
