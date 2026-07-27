package session

import (
	"bytes"
	"encoding/json"
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
	// The log says why, so say it rather than pointing at a file.
	//
	// A supervisor that dies on startup — refused the session lock, no pty, a
	// `claude` that is not there — writes its reason and exits in milliseconds, and
	// the caller then waits out the whole deadline and reports a timeout. The
	// timeout is true and it is not the fault: "already has a supervisor" is, and it
	// was on disk the entire time somebody was reading "did not come up".
	if why := lastReason(s, name, id); why != "" {
		return fault.Unavailable{Peer: name.String(), Err: fmt.Errorf(
			"the session did not come up within %s: %s (%s has the rest)",
			PopulateWait, why, s.SessionLogPath(name))}
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
	// Whatever the last session was stuck on is not the next one's problem. The mark
	// is keyed by session so a new one would ignore it anyway; removing it keeps a
	// file from outliving what it describes, which is how somebody eventually reads
	// one as current.
	if err := s.ForgetWake(name); err != nil {
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

	// Waiting for the *lock*, not only for the state file.
	//
	// The supervisor removes its state on the way out and releases the session lock
	// after that, so a caller that stopped at "the file is gone" would start a
	// replacement while the old one still held the lock. The replacement is refused
	// it — one session per identity — and dies, leaving the identity employed with
	// nothing running. That is `orc refresh` appearing to end a session without
	// starting one, and being a race it happens on a busy machine rather than on the
	// one it was tested on.
	//
	// Taking the lock and letting it go again is the honest end condition, because
	// it is the very thing the replacement needs and cannot get. It says nothing
	// about *how* the supervisor went — process, goroutine, killed, or finished — and
	// asks only whether it has let go.
	deadline := time.Now().Add(GraceStop + 2*time.Second)
	for time.Now().Before(deadline) {
		if _, live, err := s.Session(name); err == nil && !live && free(s, name) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fault.Unavailable{Peer: name.String(), Err: fmt.Errorf(
		"the supervisor did not stop within %s", GraceStop+2*time.Second)}
}

// lastReason reads the newest thing the session log has to say about this session.
//
// Best effort by design: this runs on a path that is already reporting a failure,
// so a log that will not read costs the detail and never replaces the diagnosis
// with a complaint about the diagnosis.
func lastReason(s *store.Store, name user.Name, id string) string {
	data, err := os.ReadFile(s.SessionLogPath(name))
	if err != nil {
		return ""
	}
	// Two passes: this session's own lines first, then the identity's most recent
	// reason whatever session it belonged to.
	//
	// The second pass is what makes this useful in the case it exists for. A
	// supervisor that cannot exec `claude` may be killed by the deadline before it
	// has written a single line under the new id, while the identical reason from
	// thirty seconds ago sits in the same file under the old one. Reporting "says
	// why" over a log that plainly says why is the failure this was written to fix.
	if why := reasonFor(data, id); why != "" {
		return why
	}
	// Labelled, because it is not this attempt's. An older reason is usually the
	// same reason — the same missing binary, the same dead workspace — and quoting
	// it unlabelled would let a stale one be read as current, which is worse than
	// saying nothing at all.
	if why := reasonFor(data, ""); why != "" {
		return "the last session ended with: " + why
	}
	return ""
}

// free reports whether the session lock is nobody's.
//
// Taking it and releasing it immediately: this is a test, not a claim, and holding
// it any longer would be Depopulate standing in the way of the replacement it exists
// to make room for. An error is read as "not free" — a lock that cannot be taken is
// not one to start a session against.
func free(s *store.Store, name user.Name) bool {
	release, held, err := s.HoldSession(name)
	if err != nil || !held {
		return false
	}
	release()
	return true
}

// reasonFor scans a session log backwards for the newest failure. An empty id takes
// the newest from any session.
func reasonFor(data []byte, id string) string {
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		var ev store.SessionEvent
		if err := json.Unmarshal(lines[i], &ev); err != nil {
			continue
		}
		if id != "" && ev.ID != "" && ev.ID != id {
			continue
		}
		switch ev.Op {
		case "exit", "gave-up", "failed", "prepare", "cleanup":
			if ev.Detail != "" {
				return ev.Detail
			}
		}
	}
	return ""
}
