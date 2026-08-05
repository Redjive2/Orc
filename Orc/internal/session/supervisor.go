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
	"unicode"
	"unicode/utf8"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/event"
	"orc/orc/internal/instruct"
	"orc/orc/internal/model"
	"orc/orc/internal/proc"
	"orc/orc/internal/provision"
	"orc/orc/internal/pty"
	"orc/orc/internal/store"
	"orc/orc/internal/view"
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
	// feedSize is the size of the event feed when it was last parsed, so a poll
	// waiting for a submission can skip the parse while nothing has been appended.
	// Only ever touched from Poke, which is serialised by the caller.
	feedSize int64

	// prompt is the composed system prompt: the fleet's layer, the role's, and the
	// identity's, under headings naming each. Empty where nothing is set, which is
	// every fleet that has not used the feature.
	//
	// Composed at **every** start rather than once per session. It used to be once,
	// on the reasoning that a restart continues one conversation and its
	// instructions should not change underneath it. That reasoning was wrong about
	// the thing that matters: Claude does not store a system prompt in a session's
	// transcript — it is rebuilt from the flags of whatever invocation resumed it —
	// so composing once meant a supervisor that had been up for a day was still
	// delivering the instructions the fleet had when it started, and a restart
	// silently *re-delivered* stale ones. Composing per start makes "what is
	// running" and "what is set" the same thing within one restart, which is what
	// somebody editing a prompt expects.
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

	// Composed here as well as at every start. `once` recomposes so a restart
	// delivers what the fleet says now; this first one exists so a supervisor is
	// never in a state where it knows its arguments but not its prompt — `Args` is
	// read by the log line and by tests, and one that silently omitted the
	// instructions before the first start would be a trap laid for the next person.
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
		"--model", s.spec.Model.String(),
		"--effort", s.spec.Effort.String(),
		"--name", s.spec.Identity.String(),
		// The permission mode, on the command line as well as in the compiled
		// settings. The settings comment claimed this was here and it was not — a
		// documented flag beats a settings key, and the two agreeing is what makes
		// the file a description of the session rather than a guess at one.
		"--permission-mode", provision.Mode(),
	}
	// One or the other, never both. `--session-id` *mints* a session with an id
	// Orc chose; `--resume` continues one that exists. Passing both is refused
	// outright — "--session-id can only be used with --continue or --resume if
	// --fork-session is also specified" — and a refusal at argument parsing means
	// the child dies instantly, the supervisor restarts it, and it dies again,
	// until the restart budget is spent. From outside that looks like an agent
	// that will not start, with the reason only visible by attaching.
	//
	// Not `--fork-session`, which is the other way to make the combination legal:
	// it mints a *new* id, and the id is what Orc records, what a session-scoped
	// grant is tied to, and what the next restart resumes. Orc would lose track of
	// the session it is supervising.
	if s.spec.Resume {
		args = append(args, "--resume", s.spec.ID)
	} else {
		args = append(args, "--session-id", s.spec.ID)
	}
	if s.prompt != "" {
		// `--append-system-prompt` rather than `--system-prompt`: appending leaves
		// Claude's own instructions in place and adds the fleet's to them. Replacing
		// them would mean Orc taking responsibility for everything an agent knows
		// about how to be one.
		args = append(args, "--append-system-prompt", s.prompt)
	}
	return args
}

// ComposeFor is compose, for callers outside this package.
//
// It exists so that anything which starts a session — including a test rig standing
// in for the supervisor — composes the instructions the same way the supervisor
// does. A second implementation would be a second answer to "what is this agent
// told", and the wrong one would be the one nobody was looking at.
func ComposeFor(s *store.Store, name user.Name) (string, error) { return compose(s, name) }

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

	// An identity that cannot be read is reported rather than treated as one with
	// no role. Swallowing it meant the role's layer silently vanished from the
	// composition — an agent missing a third of its instructions, with nothing
	// anywhere saying so.
	got, err := fleet.Identity(name)
	if err != nil {
		return "", err
	}
	role := got.Role()

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
		// What became of this session, written down *before* the state file goes.
		//
		// The state file describes a session that is running, so it is removed here;
		// everything about the conversation used to go with it, and `orc tend` then
		// found an employed identity with no session and started a blank one. An
		// agent that stopped because of something outside itself — a usage limit
		// reached mid-turn, a network that came and went — came back an hour later
		// with no memory of the work it had been part-way through.
		s.recordEnding()

		// Its own session and never a newer one: a supervisor that gave up, or that
		// is slow to unwind, exits after its replacement has started, and removing
		// by identity alone would delete the replacement's state file. See
		// store.RemoveOwnSession.
		if err := s.store.RemoveOwnSession(s.spec.Identity, s.spec.ID); err != nil {
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
	// The standing instructions, composed here so that every start — the first one
	// and every restart after it — delivers what the fleet says *now*.
	//
	// Not fatal if it fails. An agent that cannot think is worse than an agent
	// missing a layer somebody added, and one unreadable prompt file must not make
	// a fleet unstartable. It is carried rather than swallowed: it goes into the
	// session state and into the log, so "were the instructions delivered?" has an
	// answer that does not depend on reading a process's command line.
	s.mu.Lock()
	s.prompt, s.promptErr = compose(s.store, s.spec.Identity)
	if s.promptErr != nil {
		s.prompt = ""
	}
	s.mu.Unlock()

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
		// What the *fleet* set, not what was composed. Every agent now receives the
		// house writing rule as well, so a composed length is never zero and the
		// field it feeds — "instructed: nothing was set for it" — could never say so
		// again. This answers the question somebody asks it: did what I wrote reach
		// the agent?
		Instructed: instruct.Beyond(s.prompt),
	}
	if s.promptErr != nil {
		state.InstructError = s.promptErr.Error()
	}
	if err := s.store.WriteSession(s.spec.Identity, state); err != nil {
		// Not fatal to the session, but it is fatal to anybody finding it, so it is
		// reported rather than swallowed.
		s.note("state", "could not record the session: "+err.Error())
	}
	switch {
	case s.promptErr != nil:
		// Said at every start rather than once, because a session that restarts is
		// a session somebody is looking at the log of.
		s.note("instruct", "the standing instructions could not be composed, so this "+
			"session has none: "+s.promptErr.Error())
	case s.prompt != "":
		// The positive case is logged too. Without it, silence meant either "nothing
		// is set" or "something went wrong and you did not read the other line",
		// and those need telling apart by somebody asking why an agent is ignoring
		// an instruction.
		s.note("instruct", fmt.Sprintf("started with %d bytes of standing instructions", len(s.prompt)))
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

// recordEnding writes down what became of this session.
//
// The interesting half is `MidTurn`: whether the session went while it was *working*
// rather than while it was waiting for somebody. A session that ended waiting had
// finished its turn, and resuming it is enough. A session that ended mid-call was
// part-way through a turn nobody will finish — the model call it was inside never
// came back — so resuming it alone leaves an agent sitting silently on an unfinished
// thought. That is what a fleet looks like from outside when it "stops and does not
// come back", and it is the state a usage limit leaves behind.
//
// The feed answers it, because the hook writes a Stop when a turn ends: a last row
// that is not Waiting means the turn was still in progress. A feed that cannot be
// read answers "cannot say", which is recorded as not-mid-turn — the conservative
// direction, since the recovery for mid-turn nudges the agent and the recovery for
// the other does not, and a spurious nudge is cheaper than a spurious silence.
func (s *Supervisor) recordEnding() {
	s.mu.Lock()
	why, restarts, stopping := s.lastExit, s.restarts, s.stopping
	s.mu.Unlock()

	if stopping {
		// Asked to stop. Somebody decided this session was over, and reviving it
		// behind them would be Orc overruling the operator.
		if err := s.store.ForgetEnded(s.spec.Identity); err != nil {
			s.note("cleanup", err.Error())
		}
		return
	}

	midTurn := false
	if feed, err := view.Load(s.store.EventsPath(s.spec.Identity), s.spec.Identity); err == nil {
		if _, ok := feed.Last(); ok {
			midTurn = !feed.Waiting
		}
	}

	if err := s.store.RecordEnded(s.spec.Identity, store.Ended{
		Session: s.spec.ID, Why: why, MidTurn: midTurn, Restarts: restarts,
	}); err != nil {
		s.note("cleanup", "the ending could not be recorded, so a recovery will start a fresh session: "+err.Error())
		return
	}
	if midTurn {
		s.note("interrupted", "the session ended part-way through a turn; a recovery resumes it and nudges it on")
	}
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

	if err := Typeable(text); err != nil {
		return err
	}

	// What the agent had submitted before this, so the confirmation below can tell
	// "it arrived" from "something else arrived".
	before := s.submissions()

	if err := s.type_(p, text); err != nil {
		return err
	}
	return s.confirm(p, text, before)
}

// type_ writes one message into the terminal.
func (s *Supervisor) type_(p *pty.Pty, text string) error {
	payload := text
	// Bracketed on any line ending, not only "\n". A lone carriage return is Enter
	// to a terminal, so text carrying one submits early and the rest arrives as a
	// separate turn — the same failure bracketing exists to prevent.
	if strings.ContainsAny(text, "\n\r") {
		payload = pasteStart + text + pasteEnd
	}
	if _, err := p.Master.WriteString(payload + submit); err != nil {
		return fault.IO{Op: "write to", Path: p.Name, Err: err}
	}
	return nil
}

// confirm waits for the agent to say it received the message, and tries again in the
// two ways it can fail.
//
// Writing into a pty is not delivery. A write to the master succeeds whether or not
// anything on the other end was listening, and measured against the real binary a
// message written while it is starting is dropped — sometimes the whole thing,
// sometimes the submitting return on its own, leaving the text sitting in the box
// unsent. The app has finished painting by ~0.25s and is still losing input at 1s, so
// there is no moment that can be waited for and no output that says "ready". Orc was
// writing and hoping, and a prompt that never arrived looked exactly like one that
// did.
//
// The proof is Claude's own `UserPromptSubmit` hook, which Orc already installs and
// already records. A submitted prompt appends an event; nothing else does.
//
// The ladder matters as much as the retry, because the two failures need different
// answers and one of them must not be answered with the other:
//
//  1. **A bare return.** If the text is loaded and unsent, this submits it. It cannot
//     duplicate anything — there is no content in it — so it is safe to try first and
//     it fixes the more common failure outright.
//  2. **The whole message again.** Only if a bare return changed nothing, which means
//     the box was empty and the text never landed at all.
//
// Doing that in the other order would deliver the message twice whenever the first
// attempt was merely unsent, and an agent that acts on a duplicated instruction is a
// worse outcome than one that missed it.
//
// A session that is mid-turn is not waited for. Claude queues a prompt typed during a
// turn and submits it when that turn ends, so the hook may be minutes away — and the
// message is in the box, which is exactly where it should be.
func (s *Supervisor) confirm(p *pty.Pty, text string, before int) error {
	if s.store == nil || s.spec.ID == "" {
		// No feed to read: nothing can be confirmed, and refusing to poke at all
		// would be worse than the fire-and-forget this replaces.
		return nil
	}
	// And nothing is confirmed for a session that has never written an event.
	//
	// This is the guard that makes the ladder safe rather than reckless. Its second
	// rung sends the message again, which is only ever right when the *absence* of a
	// submission means something — and on a fleet whose hooks are not installed, or
	// in the moment before a session has recorded its start, absence means nothing
	// at all. Retrying there would deliver every prompt twice to every agent, which
	// is worse than the problem.
	//
	// A session that is running writes SessionStart before anybody can poke it, so
	// the ordinary case is covered and the exception is a fleet that genuinely
	// cannot report — where this correctly returns to writing and hoping.
	if s.events() == 0 {
		return nil
	}

	for attempt := 0; attempt < len(retries); attempt++ {
		if s.submitted(before, ConfirmWithin) {
			return nil
		}
		if s.midTurn() {
			// It is queued behind a turn, which is delivery — just not yet.
			return nil
		}
		// The same terminal, still open. A supervisor restarts its child on a
		// crash, which closes this one — and a retry written into a closed master
		// is an error about a session that no longer exists. The restart is its own
		// event with its own opening message, so there is nothing to say here.
		if s.currentPty() != p {
			return nil
		}
		var err error
		switch retries[attempt] {
		case retrySubmit:
			_, err = p.Master.WriteString(submit)
		case retryWhole:
			err = s.type_(p, text)
		}
		if err != nil {
			return fault.IO{Op: "write to", Path: p.Name, Err: err}
		}
	}
	if s.submitted(before, ConfirmWithin) {
		return nil
	}
	return fault.Unavailable{Peer: s.spec.Identity.String(), Err: errors.New(
		"the message was typed but the session never reported submitting it")}
}

// The two ways a poke is followed up, in the order they are safe to try.
type retry int

const (
	retrySubmit retry = iota // a bare return: submits text that is loaded and unsent
	retryWhole               // the message again: for text that never landed
)

var retries = []retry{retrySubmit, retryWhole}

// ConfirmWithin is how long a poke waits to be told it arrived, per attempt.
//
// Short, because this is the gap between a keystroke and a hook firing on the same
// machine, and because the ladder above spends it more than once. Long enough that a
// loaded machine is not declared broken.
const ConfirmWithin = 1500 * time.Millisecond

// submit is the key that sends whatever is in the box.
const submit = "\r"

// submitted waits for the agent's submission count to pass what it was.
//
// The feed's size is checked before it is parsed. A poll every 50ms that re-read and
// decoded a long session's whole feed each time would be megabytes of work per second
// to answer a question whose answer cannot change unless the file has grown — and the
// sessions with the largest feeds are exactly the ones that have been up longest.
func (s *Supervisor) submitted(before int, within time.Duration) bool {
	path := s.store.EventsPath(s.spec.Identity)
	grown := func() bool {
		info, err := os.Stat(path)
		if err != nil {
			// Gone or unreadable: fall through to the parse, which has its own
			// answer for that and is the one place the decision is made.
			return true
		}
		return info.Size() != s.feedSize
	}
	if info, err := os.Stat(path); err == nil {
		s.feedSize = info.Size()
	}

	deadline := time.Now().Add(within)
	for {
		if grown() {
			if info, err := os.Stat(path); err == nil {
				s.feedSize = info.Size()
			}
			if s.submissions() > before {
				return true
			}
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(ConfirmPoll)
	}
}

// ConfirmPoll is how often the feed is re-read while waiting. The file is small and
// local, and the whole wait is measured in a second or two.
const ConfirmPoll = 50 * time.Millisecond

// submissions counts the prompts this session has submitted.
//
// Unreadable is zero rather than an error. The count is only ever compared against an
// earlier count of the same thing, so a feed that cannot be read yields "no progress"
// and the ladder does what it does when a message did not arrive — which is the safe
// direction for a feed that is missing because the session has not written one yet.
func (s *Supervisor) submissions() int {
	events, _, err := event.Read(s.store.EventsPath(s.spec.Identity))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range events {
		if e.Name == "UserPromptSubmit" && e.Session == s.spec.ID {
			n++
		}
	}
	return n
}

// currentPty is the terminal this session is attached to right now, which is not
// necessarily the one a caller started writing to.
func (s *Supervisor) currentPty() *pty.Pty {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pty
}

// events counts everything this session has reported, which is how Poke tells "the
// prompt did not arrive" from "this fleet does not report".
func (s *Supervisor) events() int {
	got, _, err := event.Read(s.store.EventsPath(s.spec.Identity))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range got {
		if e.Session == s.spec.ID {
			n++
		}
	}
	return n
}

// midTurn reports whether the agent is working, in which case a prompt is queued
// rather than submitted and the hook is not the right thing to wait for.
//
// The same question recordEnding asks, and the same answer: the feed's last row says
// whether the turn ended. Where it cannot be read the answer is "not mid-turn", which
// sends the ladder down its retry path — the safe direction here, because the cost of
// a needless bare return is nothing and the cost of assuming a message is safely
// queued when it was dropped is the silence this whole thing exists to stop.
func (s *Supervisor) midTurn() bool {
	feed, err := view.Load(s.store.EventsPath(s.spec.Identity), s.spec.Identity)
	if err != nil {
		return false
	}
	if _, ok := feed.Last(); !ok {
		return false
	}
	return !feed.Waiting
}

// The terminal's paste markers. Named because the closing one is the sequence that
// must never appear *inside* a payload — see Typeable.
const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

// Typeable refuses text that must not be written into a session's terminal.
//
// This is the last gate in front of a pty, and it is here rather than only at the
// command line because the socket is a separate way in: `orc poke` validates before
// it dials, and something that dialled without validating would otherwise be typing
// straight into somebody's agent.
//
// What it refuses, and why each one is not paranoia:
//
//   - **Control characters.** Written raw into a terminal they do what they say: an
//     escape sequence repaints the screen, a NUL truncates, and a lone ^C or ^D at
//     the wrong moment ends the agent's turn or its session.
//   - **The bracketed-paste terminator.** Text carrying `ESC[201~` closes the paste
//     early, and everything after it is read as *keystrokes* rather than as content
//     — which is how a wake message becomes a command the agent never chose to run.
//     Bracketing is what makes a multi-line poke one message; a payload that can
//     end the bracket makes it several.
//
// Newlines and tabs are how prose is written, and are what bracketing is for.
//
// The same rules `instruct.Check` applies to a stored wake message. The two exist
// separately because they guard different doors — a store and a socket — and a
// guard that only stood at one of them would be a guard somebody walks around.
func Typeable(text string) error {
	if !utf8.ValidString(text) {
		return fault.Parse{Path: "poke", Reason: "the message is not valid UTF-8"}
	}
	if strings.Contains(text, pasteEnd) || strings.Contains(text, pasteStart) {
		return fault.Parse{Path: "poke", Reason: "the message contains a bracketed-paste marker; " +
			"it would end the paste early and the rest would be typed as keystrokes"}
	}
	for i, r := range text {
		if r == '\n' || r == '\t' || r == '\r' {
			continue
		}
		if unicode.IsControl(r) {
			return fault.Parse{Path: "poke", Reason: fmt.Sprintf(
				"there is a control character (%q) at byte %d; typed into a terminal it does "+
					"something nobody asked for", r, i)}
		}
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
