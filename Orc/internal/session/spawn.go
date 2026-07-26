package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/model"
	"orc/orc/internal/proc"
	"orc/orc/internal/store"
)

// Binary is the supervisor's name.
const Binary = "orc-session"

// EnvBinary overrides where to find it, which is what a test uses to point at a
// binary it built itself.
const EnvBinary = "ORC_SESSION_BIN"

// PopulateWait is how long Populate waits for the session it started to appear.
//
// It waits at all because the alternative is worse: `orc employ` returning
// immediately would print "employed" and leave the operator to discover on their own
// that the supervisor died on startup — a bad model name, no credential, no claude
// binary. Waiting a moment turns that into a refusal with the reason in it.
const PopulateWait = 5 * time.Second

// Find locates the supervisor binary.
//
// Beside the running `orc` first, then the PATH. Beside first is what makes a
// freshly built pair work without installing anything: the two binaries are built
// together and belong to the same version, and a stale `orc-session` on the PATH
// speaking an older protocol is exactly the mismatch this ordering avoids.
func Find() (string, error) {
	if override := os.Getenv(EnvBinary); override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", fault.NotFound{Target: EnvBinary + " points at " + override + ", which is not there"}
		}
		return override, nil
	}

	if self, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(self), Binary)
		if _, err := os.Stat(sibling); err == nil {
			return sibling, nil
		}
	}

	path, err := exec.LookPath(Binary)
	if err != nil {
		return "", fault.NotFound{Target: fmt.Sprintf(
			"%s, which holds a session open; it is built beside orc and has to be on the PATH or beside it", Binary)}
	}
	return path, nil
}

// Populate starts a supervisor for an identity and waits for it to come up.
//
// The child is detached — its own session, streams to the null device, never waited
// on — because it has to outlive the `orc` command that started it. That is the same
// shape orc/common/nudge uses to fire a sync, for the same reason, and the one thing
// it must not do is inherit this process's streams: a supervisor writing to the
// terminal that started it would interleave with whatever printed next.
func Populate(s *store.Store, name user.Name, id string, m model.Model, e model.Effort, resume bool) error {
	if s == nil {
		return fault.Internal{Where: "session.Populate", Detail: "no store given"}
	}
	if _, live, err := s.Session(name); err != nil {
		return err
	} else if live {
		return fault.Conflict{Path: name.String(), Reason: fmt.Sprintf("%s already has a session", name)}
	}

	bin, err := Find()
	if err != nil {
		return err
	}

	args := []string{
		"--identity", name.String(),
		"--session-id", id,
		"--model", m.String(),
		"--effort", e.String(),
		"--root", s.Root(),
	}
	if resume {
		args = append(args, "--resume")
	}

	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fault.IO{Op: "open", Path: os.DevNull, Err: err}
	}
	defer func() { _ = null.Close() }()

	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	cmd.Env = os.Environ()
	// Its own session, so a Ctrl-C in the terminal that ran `orc employ` does not
	// reach the fleet it just started.
	proc.Detach(cmd)

	if err := cmd.Start(); err != nil {
		return fault.IO{Op: "start", Path: bin, Err: err}
	}
	// Deliberately no Wait: the supervisor outlives this process and is reaped by
	// init. Waiting is the one thing this function must not do.

	// Now wait for the session to exist. A supervisor that dies on startup writes
	// its reason to the session log, so the failure names something the operator can
	// act on rather than "it did not start".
	deadline := time.Now().Add(PopulateWait)
	for time.Now().Before(deadline) {
		if _, live, err := s.Session(name); err == nil && live {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fault.Unavailable{Peer: name.String(), Err: fmt.Errorf(
		"the session did not come up within %s; %s says why", PopulateWait, s.SessionLogPath(name))}
}

// Depopulate ends a session and waits for the supervisor to go.
//
// It asks over the socket rather than signalling the pid, because the supervisor
// knows how to end a session properly — SIGTERM to the child's process group, a
// grace period, then SIGKILL — and a caller that signalled the supervisor directly
// would race that.
//
// A session that is not there is not an error: `orc fire` and `orc tend` both call
// this to make a fact true, and one that refuses when the fact already holds is one
// nobody can run twice.
func Depopulate(s *store.Store, name user.Name) error {
	state, live, err := s.Session(name)
	if err != nil {
		return err
	}
	if !live {
		// Tidy up after a supervisor that was killed, so nothing is left claiming a
		// session that has gone.
		return s.RemoveSession(name)
	}

	client, err := Dial(state.Socket)
	if err != nil {
		// The socket is gone but the supervisor is alive — a half-torn-down session.
		// SIGTERM is the fallback, and the supervisor's own handler turns it into a
		// proper stop.
		if state.Supervisor > 0 {
			_ = proc.Stop(state.Supervisor)
		}
	} else if err := client.Stop(); err != nil {
		return err
	}

	deadline := time.Now().Add(GraceStop + 2*time.Second)
	for time.Now().Before(deadline) {
		if _, live, err := s.Session(name); err == nil && !live {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fault.Unavailable{Peer: name.String(), Err: fmt.Errorf(
		"the supervisor did not stop within %s", GraceStop+2*time.Second)}
}
