package proc_test

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"orc/orc/internal/proc"
)

// The questions this package answers are the same on every platform even though
// the calls behind them are not, so these tests are written once and run
// everywhere. What they are really guarding is that a port did not quietly
// change an answer.

func TestThisProcessIsAlive(t *testing.T) {
	if !proc.Alive(os.Getpid()) {
		t.Error("the running test process was reported dead")
	}
}

// A pid that cannot exist is not alive, and asking must not be an error — the
// caller is deciding whether a session is held, not whether the question was
// well formed.
func TestNonsensePidsAreNotAlive(t *testing.T) {
	for _, pid := range []int{0, -1, -12345} {
		if proc.Alive(pid) {
			t.Errorf("pid %d was reported alive", pid)
		}
	}
}

// The one that matters: a process that has exited is dead, promptly. A stale
// yes here means a session reported as held by a supervisor that is long gone,
// and Orc refusing to start a new one.
func TestAnExitedProcessIsDead(t *testing.T) {
	cmd := exec.Command(sleeper(), sleeperArgs()...)
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a child here: %v", err)
	}
	pid := cmd.Process.Pid

	if !proc.Alive(pid) {
		t.Fatalf("a just-started child (pid %d) was reported dead", pid)
	}
	if err := proc.Stop(pid); err != nil {
		t.Fatalf("stopping the child: %v", err)
	}
	// Reaped, or the pid lingers as a zombie on unix and stays "alive" forever.
	_ = cmd.Wait()

	deadline := time.Now().Add(2 * time.Second)
	for proc.Alive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("pid %d is still reported alive after it exited", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Detach must leave a command startable. It is easy to write a version of this
// that sets a flag the platform refuses, and the failure would only ever show
// up as a supervisor that will not start.
func TestADetachedCommandStarts(t *testing.T) {
	cmd := exec.Command(sleeper(), sleeperArgs()...)
	proc.Detach(cmd)

	if err := cmd.Start(); err != nil {
		t.Fatalf("a detached command would not start: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = proc.Stop(pid)
		_ = cmd.Wait()
	})
	if !proc.Alive(pid) {
		t.Error("the detached child was reported dead")
	}
}

// Stopping something that is not there is quiet. A supervisor tearing down a
// session it has already lost should not have to check first.
func TestStoppingNothingIsQuiet(t *testing.T) {
	for _, pid := range []int{0, -1} {
		if err := proc.Stop(pid); err != nil {
			t.Errorf("Stop(%d) = %v, want nothing", pid, err)
		}
		if err := proc.StopGroup(pid); err != nil {
			t.Errorf("StopGroup(%d) = %v, want nothing", pid, err)
		}
		if err := proc.KillGroup(pid); err != nil {
			t.Errorf("KillGroup(%d) = %v, want nothing", pid, err)
		}
	}
}

// A child that sits still long enough to be asked about, spelled for whichever
// shell the platform has.
func sleeper() string {
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	return "sleep"
}

func sleeperArgs() []string {
	if runtime.GOOS == "windows" {
		// timeout is the closest thing to sleep that ships with Windows.
		return []string{"/c", "timeout", "/t", "30", "/nobreak"}
	}
	return []string{"30"}
}
