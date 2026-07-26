// Package session runs a Claude session and lets other processes reach it.
//
// One supervisor per populated identity, and it is the one long-lived process in
// this tree. Every other Orc binary is a command that exits; this one has to
// outlive the `orc` that started it, because the *session* has to. That is the
// whole reason it exists as a separate binary rather than a goroutine.
//
// What it owns:
//
//   - the pty, and the claude process on the other side of it;
//   - the scrollback, so an attach can show what happened before it arrived;
//   - a unix socket, so `orc attach`, `orc poke`, and `orc tend` can reach the
//     session without being the process that started it;
//   - restarting the child when it dies, with the same session id, so a crash
//     costs a restart and not a conversation.
//
// What it deliberately does not own: whether the identity *should* be running.
// That is the worklist's, in the store, and the supervisor exits when told to
// rather than deciding for itself.
package session

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/instruct"
	"orc/orc/internal/model"
	"orc/orc/internal/proc"
	"orc/orc/internal/pty"
	"orc/orc/internal/store"
)

// Restart policy.
//
// A session that dies is restarted with the same id, so the conversation
// continues; the backoff climbs so that a session failing instantly — a bad
// model name, no credential, a missing binary — does not spend a machine's
// evening restarting. After MaxRestarts the supervisor gives up **loudly**: it
// writes why, removes its state, and exits, leaving the identity employed and
// unpopulated for `orc tend` to report.
const (
	MaxRestarts  = 5
	FirstBackoff = time.Second
	MaxBackoff   = 30 * time.Second
	// GraceStop is how long a child gets to leave on its own after SIGTERM before
	// SIGKILL. A Claude session flushes a transcript on the way out, and killing
	// it outright would lose the tail of the conversation it just had.
	GraceStop = 5 * time.Second
	// Scrollback is how much output an attach can be shown. It is a ring, so a
	// session that runs for a week does not fill a disk or a heap.
	Scrollback = 256 << 10
)

// EnvClaude names the binary to run, so a test can point Orc at something
// harmless and a machine that installs Claude under another name can say so.
const EnvClaude = "ORC_CLAUDE_BIN"

// DefaultClaude is the command a session runs when the environment is silent.
const DefaultClaude = "claude"

// Spec is everything a supervisor needs to start a session.
type Spec struct {
	// Identity is whose session this is.
	Identity user.Name
	// ID is the Claude session id. Orc mints it rather than reading one back, so
	// `--resume` after a crash is deterministic and a session-scoped grant has
	// something stable to be tied to.
	ID string
	// Model and Effort are the load this session runs at.
	Model  model.Model
	Effort model.Effort
	// Resume asks Claude to continue the conversation under ID rather than start
	// one. It is set on a restart and never on a refresh: that is the whole
	// difference between recovering and starting fresh.
	Resume bool
}

// Supervisor holds one session. The zero value is not usable; build one with New.
type Supervisor struct {
	store *store.Store
	spec  Spec
	env   []string
	bin   string

	mu       sync.Mutex
	pty      *pty.Pty
	child    *exec.Cmd
	ring     *ring
	watchers map[chan []byte]struct{}
	restarts int
	stopping bool
	lastExit string

	// prompt is the composed system prompt: the fleet's layer, the role's, and the
	// identity's, under headings naming each. Empty where nothing is set, which is
	// every fleet that has not used the feature.
	prompt string
	// promptErr is why there is no prompt, when there should have been one.
	promptErr error
}

// New builds a supervisor.
//
// env is the whole environment the session runs with — Orc composes it rather than
// inheriting, so what a session can see is a decision somebody made and can read
// back. bin is the command; an empty one takes the environment's or the default.
func New(s *store.Store, spec Spec, env []string, bin string) (*Supervisor, error) {
	switch {
	case s == nil:
		return nil, fault.Internal{Where: "session.New", Detail: "no store given"}
	case spec.Identity.Zero():
		return nil, fault.Internal{Where: "session.New", Detail: "no identity named"}
	case spec.ID == "":
		return nil, fault.Internal{Where: "session.New", Detail: "no session id given"}
	case !spec.Model.Valid() || !spec.Effort.Valid():
		return nil, fault.Internal{Where: "session.New", Detail: "session needs a model and an effort"}
	}
	if bin == "" {
		bin = DefaultClaude
	}

	// The standing instructions, composed once for the session's whole life.
	//
	// Once rather than per restart, for the reason Prepare gives about the
	// permission snapshot: a restart continues the same conversation, and a prompt
	// that changed underneath it would mean an agent whose instructions differ from
	// the ones it has been following since the first turn. `orc refresh` is what
	// asks for new instructions, and it gets them by getting a new session.
	// Not fatal if it fails. An agent that cannot think is worse than an agent
	// missing a layer somebody added, and one unreadable prompt file must not make
	// a fleet unstartable. It is carried rather than swallowed: `start` writes it
	// to the session log, where somebody asking why an instruction is not being
	// followed will find it.
	prompt, promptErr := compose(s, spec.Identity)
	if promptErr != nil {
		prompt = ""
	}

	return &Supervisor{
		store:     s,
		spec:      spec,
		prompt:    prompt,
		promptErr: promptErr,
		env:       env,
		bin:       bin,
		ring:      newRing(Scrollback),
		watchers:  map[chan []byte]struct{}{},
	}, nil
}

// Args is the command line a session runs.
//
// It is a method rather than a literal so the flags are in one place and a test can
// assert them: the session id, the load, the compiled settings, and a display name
// so an attach is labelled. `--resume` appears only on a restart.
func (s *Supervisor) Args() []string {
	args := []string{
		"--session-id", s.spec.ID,
		"--model", s.spec.Model.String(),
		"--effort", s.spec.Effort.String(),
		"--name", s.spec.Identity.String(),
	}
	if s.prompt != "" {
		// `--append-system-prompt` rather than `--system-prompt`: appending leaves
		// Claude's own instructions in place and adds the fleet's to them. Replacing
		// them would mean Orc taking responsibility for everything an agent knows
		// about how to be one.
		args = append(args, "--append-system-prompt", s.prompt)
	}
	if s.spec.Resume {
		args = append(args, "--resume", s.spec.ID)
	}
	return args
}

// compose gathers an identity's three prompt layers and joins them.
//
// The fleet is derived here rather than passed in for the reason Prepare gives: the
// supervisor is a separate process from the `orc employ` that spawned it, and
// whatever that command derived describes a fleet from before this session existed.
func compose(s *store.Store, name user.Name) (string, error) {
	fleet, err := s.Fleet()
	if err != nil {
		return "", err
	}

	var role model.Name
	if got, err := fleet.Identity(name); err == nil {
		role = got.Role()
	}

	layers, err := s.Instructions(name, role)
	if err != nil {
		return "", err
	}
	return instruct.Compose(layers)
}

// Run holds the session until it is told to stop or gives up.
//
// It is the supervisor's whole life, and the order in it matters:
//
//  1. take the session lock, so a second supervisor for one identity is refused
//     rather than quietly racing the first;
//  2. serve the socket, so an attach that arrives during a restart waits rather
//     than failing;
//  3. run the child, restarting it until told to stop or out of attempts;
//  4. remove the state file, so nothing is left claiming a session that has gone.
func (s *Supervisor) Run() error {
	release, held, err := s.store.HoldSession(s.spec.Identity)
	if err != nil {
		return err
	}
	if !held {
		return fault.Conflict{Path: s.spec.Identity.String(), Reason: fmt.Sprintf(
			"%s already has a supervisor; one session per identity", s.spec.Identity)}
	}
	defer release()

	// The settings and the permission snapshot, once per session rather than once per
	// restart — see Prepare. A failure is reported and the session still starts: the
	// hook is the boundary either way, and an agent that cannot think is worse than
	// one whose cheap first layer is missing.
	if err := Prepare(s.store, s.spec.Identity, s.spec.ID); err != nil {
		s.note("prepare", "the session's permissions could not be compiled: "+err.Error())
	}

	listener, err := s.listen()
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	go s.serve(listener)

	defer func() {
		if err := s.store.RemoveSession(s.spec.Identity); err != nil {
			s.note("cleanup", err.Error())
		}
	}()

	for {
		err := s.once()
		s.mu.Lock()
		stopping, restarts := s.stopping, s.restarts
		s.mu.Unlock()

		if stopping {
			s.note("stopped", "asked to stop")
			return nil
		}
		if err != nil {
			s.note("exit", err.Error())
		} else {
			s.note("exit", "the session ended on its own")
		}

		if restarts >= MaxRestarts {
			// Loudly: the log says it gave up, and the state file goes, so `orc
			// tend` sees an employed identity with no session and reports it rather
			// than finding a file that claims one.
			s.note("gave-up", fmt.Sprintf("%d restarts did not hold; leaving %s unpopulated",
				restarts, s.spec.Identity))
			return fault.Unavailable{Peer: s.bin, Err: errors.New("the session would not stay up")}
		}

		wait := backoff(restarts)
		s.note("restarting", fmt.Sprintf("attempt %d in %s", restarts+1, wait))
		time.Sleep(wait)

		s.mu.Lock()
		s.restarts++
		s.mu.Unlock()
		// Every restart resumes: the conversation is the thing worth keeping, and a
		// crash is not a request for a new one. `orc refresh` is.
		s.spec.Resume = true
	}
}

// once starts the child and waits for it.
func (s *Supervisor) once() error {
	p, err := pty.Open()
	if err != nil {
		return err
	}
	if err := pty.Resize(p.Master, pty.Sane()); err != nil {
		_ = p.Close()
		return err
	}

	cmd := exec.Command(s.bin, s.Args()...)
	cmd.Env = s.env
	cmd.Dir = s.store.WorkspaceDir(s.spec.Identity)
	p.Attach(cmd)

	if err := cmd.Start(); err != nil {
		_ = p.Close()
		return fault.IO{Op: "start", Path: s.bin, Err: err}
	}
	// The parent's copy of the child's side goes now, so a read on the master sees
	// the child exit instead of blocking forever.
	if err := p.CloseSlave(); err != nil {
		return err
	}

	s.mu.Lock()
	s.pty, s.child = p, cmd
	s.mu.Unlock()

	state := store.SessionState{
		ID:         s.spec.ID,
		Supervisor: os.Getpid(),
		Child:      cmd.Process.Pid,
		Model:      s.spec.Model.String(),
		Effort:     s.spec.Effort.String(),
		Workspace:  cmd.Dir,
		Started:    clock.Format(s.store.Now()),
		Restarts:   s.restarts,
		LastExit:   s.lastExit,
	}
	if err := s.store.WriteSession(s.spec.Identity, state); err != nil {
		// Not fatal to the session, but it is fatal to anybody finding it, so it is
		// reported rather than swallowed.
		s.note("state", "could not record the session: "+err.Error())
	}
	if s.promptErr != nil {
		// Said at every start rather than once, because a session that restarts is
		// a session somebody is looking at the log of.
		s.note("instruct", "the standing instructions could not be composed, so this "+
			"session has none: "+s.promptErr.Error())
	}
	s.note("started", fmt.Sprintf("%s %s", s.bin, strings.Join(s.Args(), " ")))

	// The pump ends when the pty reports the child has gone, which is also how a
	// stop is noticed: closing the master makes the read fail.
	s.pump(p)

	err = cmd.Wait()
	s.mu.Lock()
	s.lastExit = exitReason(err)
	s.pty, s.child = nil, nil
	s.mu.Unlock()
	_ = p.Close()
	return err
}

// pump copies the session's output into the scrollback and to every watcher.
//
// A watcher whose channel is full is skipped rather than waited on. That is the
// rule that keeps one slow attach — a person on a bad connection, a terminal that
// has stopped reading — from stalling the agent: the session is the thing that must
// keep running, and an attacher who cannot keep up loses output rather than
// blocking everybody.
func (s *Supervisor) pump(p *pty.Pty) {
	buf := make([]byte, 32<<10)
	for {
		n, err := p.Master.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])

			s.mu.Lock()
			s.ring.write(chunk)
			for w := range s.watchers {
				select {
				case w <- chunk:
				default:
				}
			}
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// Poke writes text into the session as if it had been typed.
//
// Multi-line text is wrapped in a bracketed paste, so a TUI treats it as one
// message rather than submitting at the first newline — which is what makes a
// composed buffer from `orc attach` deliverable in one piece.
func (s *Supervisor) Poke(text string) error {
	s.mu.Lock()
	p := s.pty
	s.mu.Unlock()
	if p == nil {
		return fault.Unavailable{Peer: s.spec.Identity.String(), Err: errors.New("the session is restarting")}
	}

	payload := text
	if strings.Contains(text, "\n") {
		payload = "\x1b[200~" + text + "\x1b[201~"
	}
	if _, err := p.Master.WriteString(payload + "\r"); err != nil {
		return fault.IO{Op: "write to", Path: p.Name, Err: err}
	}
	return nil
}

// Resize passes a new window size to the session, which makes the kernel signal the
// child to redraw.
func (s *Supervisor) Resize(size pty.WinSize) error {
	s.mu.Lock()
	p := s.pty
	s.mu.Unlock()
	if p == nil {
		return nil // restarting; the next start sets a size of its own
	}
	return pty.Resize(p.Master, size)
}

// Stop ends the session: SIGTERM, a grace period, then SIGKILL.
//
// The signal goes to the child's process *group* rather than to the child, because
// Claude starts helpers of its own and a SIGTERM to the leader alone can leave them
// behind holding the terminal.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	s.stopping = true
	child := s.child
	p := s.pty
	s.mu.Unlock()

	if child == nil || child.Process == nil {
		return
	}
	pid := child.Process.Pid
	_ = proc.StopGroup(pid)

	deadline := time.Now().Add(GraceStop)
	for time.Now().Before(deadline) {
		if !proc.Alive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.note("kill", "the session did not leave within "+GraceStop.String())
	_ = proc.KillGroup(pid)
	if p != nil {
		_ = p.Master.Close()
	}
}

// watch registers a channel for output, and returns what unregisters it.
func (s *Supervisor) watch() (<-chan []byte, []byte, func()) {
	ch := make(chan []byte, 64)

	s.mu.Lock()
	s.watchers[ch] = struct{}{}
	history := s.ring.bytes()
	s.mu.Unlock()

	return ch, history, func() {
		s.mu.Lock()
		delete(s.watchers, ch)
		s.mu.Unlock()
	}
}

// write sends bytes from an attached terminal to the session.
func (s *Supervisor) write(data []byte) error {
	s.mu.Lock()
	p := s.pty
	s.mu.Unlock()
	if p == nil {
		return nil // restarting; keystrokes during a restart have nowhere to go
	}
	_, err := p.Master.Write(data)
	return err
}

// note records what the supervisor did. A log it cannot write is not a reason to
// stop running a session, so the error is deliberately dropped here.
func (s *Supervisor) note(op, detail string) {
	s.mu.Lock()
	child := 0
	if s.child != nil && s.child.Process != nil {
		child = s.child.Process.Pid
	}
	s.mu.Unlock()

	_ = s.store.AppendSessionEvent(s.spec.Identity, store.SessionEvent{
		Op: op, ID: s.spec.ID, Child: child, Detail: detail,
	})
}

// exitReason turns a Wait error into the text session.json carries.
func exitReason(err error) string {
	if err == nil {
		return "ended cleanly"
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.String()
	}
	return err.Error()
}

// backoff climbs to a ceiling, so a session that fails instantly does not spin.
func backoff(restarts int) time.Duration {
	wait := FirstBackoff << restarts
	if wait > MaxBackoff || wait <= 0 {
		return MaxBackoff
	}
	return wait
}

// ring is a fixed-size buffer of the most recent output.
//
// It is in memory rather than on disk, and that is a real limit worth naming: the
// scrollback an attach can show is what this supervisor has seen, so a supervisor
// that has just restarted after a crash shows a short history. The session
// transcript Claude keeps is the durable record; this is only what the screen can
// replay.
type ring struct {
	buf  []byte
	size int
	full bool
	at   int
}

func newRing(size int) *ring { return &ring{buf: make([]byte, size), size: size} }

func (r *ring) write(p []byte) {
	if len(p) >= r.size {
		copy(r.buf, p[len(p)-r.size:])
		r.at, r.full = 0, true
		return
	}
	n := copy(r.buf[r.at:], p)
	if n < len(p) {
		copy(r.buf, p[n:])
		r.at = len(p) - n
		r.full = true
	} else {
		r.at += n
		if r.at == r.size {
			r.at, r.full = 0, true
		}
	}
}

func (r *ring) bytes() []byte {
	if !r.full {
		out := make([]byte, r.at)
		copy(out, r.buf[:r.at])
		return out
	}
	out := make([]byte, 0, r.size)
	out = append(out, r.buf[r.at:]...)
	return append(out, r.buf[:r.at]...)
}

// listen creates the session socket, replacing a stale one.
//
// A socket left by a killed supervisor cannot be bound over, and refusing to start
// because of a file whose owner is gone would mean an identity that can never be
// populated again. The state file's liveness check is what makes removing it safe:
// by the time a supervisor gets here it holds the lock, so no live supervisor owns
// that socket.
func (s *Supervisor) listen() (net.Listener, error) {
	path := s.store.SocketPath(s.spec.Identity)
	// The socket may live outside the store when the store's own path is too long
	// for sun_path (see store.SocketPath), so its directory is made here rather
	// than assumed.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fault.IO{Op: "create the directory for", Path: path, Err: err}
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fault.IO{Op: "remove the stale socket at", Path: path, Err: err}
	}

	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fault.IO{Op: "listen on", Path: path, Err: err}
	}
	// 0600: the socket is a way to type into somebody's agent, so it is exactly as
	// private as the keyring beside it.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		return nil, fault.IO{Op: "set permissions on", Path: path, Err: err}
	}
	return l, nil
}
