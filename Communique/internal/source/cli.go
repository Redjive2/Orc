package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"orc/common/nudge"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
	"orc/cq/internal/upgrade"
)

// Timeout bounds one invocation of another tool. A sync that hangs because
// something upstream is wedged should fail and be retried, not wait forever.
const Timeout = 30 * time.Second

// MaxOutputBytes bounds what a tool may print. The store it reads is capped
// upstream, so anything larger means something is wrong.
const MaxOutputBytes = protocol.MaxSnapshotBytes

// CLI reads Mailman and Macmuffin by running them under `--json`.
//
// It reads their CLIs rather than their files so their on-disk formats stay
// private to them, their validated read paths are reused rather than
// reimplemented, and their authentication comes along for free.
type CLI struct {
	// Mailman is the command to run, "mailman" by default.
	Mailman string
	// Muff is the Macmuffin command, "muff" by default.
	Muff string
	// Dock and Anno are the two lenses on the repository.
	Dock string
	Anno string
	// Orc is the orc command, "orc" by default. It reads the fleet and applies the
	// fleet verbs, as the mirrored account.
	Orc string
	// Upgrade says where this machine's checkout and binaries are. Its zero value
	// has no source, which makes every upgrade a refusal that says so — the right
	// answer for a machine that installs binaries rather than building them.
	Upgrade upgrade.Options
	// User is the account whose mailbox is mirrored. Required.
	User string
	// LibraryRoot is the checkout the library verbs may write, and the only part
	// of the filesystem they may touch. Empty means this machine mirrors no
	// repository and every edit is refused.
	LibraryRoot string
	// Home is the agent's own directory: its journal, its cursor, and the settings
	// an operator has chosen from the website. Needed to *change* the library
	// root, which is recorded there rather than in the environment — see
	// applyLibraryRoot. Empty means that action refuses and nothing else notices.
	Home string
	// Key is the mirrored account's Orc credential.
	//
	// When it is set, cq authenticates to Mailman as User itself rather than as
	// whoever happens to be in the environment. That is what lets an agent's
	// `mailman send` nudge the operator's mirror: the agent triggers the sync,
	// but the sync reads the operator's mailbox, not the agent's.
	//
	// Optional. Without it cq falls back to the ambient identity, and refuses
	// to run if that is not User.
	Key string
	// Warn reports something the operator should know that is not a failure.
	// It exists so a refused admin panel is explained rather than silently
	// absent. Defaults to discarding, so a caller that does not care need not
	// set it.
	Warn func(format string, args ...any)
	// Look reads the environment. It exists because the identity Mailman
	// answers as is ambient, and this adapter has to check it rather than
	// assume it. Defaults to the real environment.
	Look func(string) (string, bool)
	// Run executes a command; it exists so tests can drive this without either
	// tool installed. Defaults to running the real thing.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// EnsureWatch makes sure something is still mirroring this machine once an
	// upgrade has replaced the binaries.
	//
	// It is a hook rather than work done here because the decision needs the agent
	// home and the flags this machine syncs with, and neither is a fact about a
	// snapshot source. What it must guarantee is the promise in cli.ensureWatch:
	// after an upgrade, this machine is being watched by something.
	//
	// Optional. A nil hook means an upgrade changes nothing about what is running,
	// which is the old behaviour and the right one for a caller — a test, a probe —
	// that is not managing a real machine.
	EnsureWatch func() error
}

// NewCLI returns an adapter with the usual commands.
func NewCLI(user string) *CLI {
	return &CLI{Mailman: "mailman", Muff: "muff", Dock: "dock", Anno: "anno", Orc: "orc", User: user}
}

// Snapshot collects the machine's whole state.
//
// Every step is required except the admin block, which is skipped when the
// operator turned it off. A partial snapshot is never returned: the server
// replaces its copy wholesale, so half a mailbox would read as a mailbox that
// had lost half its mail.
func (c *CLI) Snapshot(ctx context.Context, opts Options) (protocol.Snapshot, error) {
	if err := opts.Validate(); err != nil {
		return protocol.Snapshot{}, err
	}
	if c.User == "" {
		// The caller resolves this before building an adapter — see cli.mirrored,
		// which asks Orc when nothing is set. Reaching here means it did not, so
		// the message says what a snapshot needs rather than only that it is
		// missing.
		return protocol.Snapshot{}, fault.Usage{Reason: "no mailbox to mirror: set $CQ_USER, or run this where orc can say who the operator is"}
	}
	if err := c.checkIdentity(); err != nil {
		return protocol.Snapshot{}, err
	}

	inboxWire, archiveWire, sentWire, err := c.mailbox(ctx)
	if err != nil {
		return protocol.Snapshot{}, err
	}
	tasks, err := c.tasks(ctx)
	if err != nil {
		return protocol.Snapshot{}, err
	}

	snap := protocol.Snapshot{
		Machine: opts.Machine,
		User:    c.User,
		TakenAt: time.Now().UTC(),
		Inbox:   messages(inboxWire, true),
		Archive: messages(archiveWire, true),
		Sent:    messages(sentWire, true),
		Convos:  c.convos(ctx, inboxWire, archiveWire, sentWire),
		Tasks:   tasks,
	}

	// The fleet, when this machine has one. A machine that runs no agents has no
	// orc and says so in a line, which is not a failed sync — most machines that
	// mirror a mailbox are not also running a fleet.
	if fleet := c.orc().Fleet(ctx); fleet.Unreachable == "" || fleet.Operator != "" {
		snap.Fleet = &fleet
	}

	if opts.Library != "" {
		lib, err := c.Library(ctx, opts.Library)
		if err != nil {
			// The repository is something to read, not the mailbox. Failing the
			// whole sync over it would stop mail arriving because a directory
			// moved.
			c.warn("the library could not be collected, so it is not in this snapshot: %v", err)
		} else {
			snap.Library = lib
		}
	}

	if opts.Admin {
		admin, err := c.admin(ctx, opts.AdminBodies)
		// A refused panel is not a failed sync. The mailbox is what cq is for,
		// and Mailman refuses the whole-store view to anyone but the store's
		// owner — which is every machine that has not run `mailman admin owner`
		// yet. Failing the mirror over an extra would break mirroring for
		// exactly the setups that have not opted into the extra.
		switch {
		case denied(err):
			// Not silently: an empty panel with no explanation is the operator
			// wondering whether cq is broken.
			c.warn("the whole-store view was refused, so the admin panel is not "+
				"included; `mailman admin owner %s` grants it: %v", c.User, err)
		case err != nil:
			return protocol.Snapshot{}, err
		}
		snap.Admin = admin
	}

	// The snapshot is validated here rather than only on the wire, so a tool
	// that reports something cq cannot represent is caught on the machine where
	// it can be diagnosed.
	if err := snap.Validate(); err != nil {
		return protocol.Snapshot{}, err
	}
	return snap, nil
}

// mailbox reads the three listings Mailman keeps apart.
//
// Three commands and not one, because Mailman genuinely answers three different
// questions: `inbox --all` is mail you have been sent and not filed away,
// `archive` is mail you filed, and `inbox --sent` is mail you wrote. There is
// no command that unions them, and cq should not invent one by reading the
// store behind Mailman's back.
//
// All three are always read. A mirror that omitted what the user sent would
// drop their own half of every conversation, and would report a thread's length
// as only the messages they received.
// ExitDenied is Orc's shared exit code for "authenticated, but not permitted".
// It is the same number in every tool; see Claude/Docs/ExitCodes.md.
const ExitDenied = 8

// withCause reports one message and remembers another error underneath.
//
// The message is the tool's own, which is the useful one; the cause is the
// process failure, which carries the exit status. Keeping both means a refusal
// can be recognised without the status leaking into what the operator reads.
type withCause struct {
	err   error
	cause error
}

func (e withCause) Error() string   { return e.err.Error() }
func (e withCause) Unwrap() []error { return []error{e.err, e.cause} }

// denied reports whether a tool refused rather than broke.
//
// It looks for the exit status rather than for a wrapper of cq's own, so it
// works on an error from anywhere: Orc's codes are shared, and 8 means the same
// thing whichever tool produced it.
func denied(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == ExitDenied
}

// The variables Mailman authenticates with. cq either sets them, from its own
// stored credential, or inherits them — and inheriting is why the identity has
// to be checked rather than assumed.
const (
	OrcUser = "ORC_USER"
	OrcKey  = "ORC_KEY"
)

// warn reports a non-fatal problem, if anyone is listening.
func (c *CLI) warn(format string, args ...any) {
	if c.Warn != nil {
		c.Warn(format, args...)
	}
}

// checkIdentity refuses to mirror one account's mail as another's.
//
// Mailman answers as whoever ORC_USER names, and cq only *labels* the snapshot
// with the user it was told to mirror. Nothing connects the two, so an
// environment where they disagree produces a snapshot that says "redjive" and
// contains somebody else's mail — which the server then serves as the
// operator's own inbox.
//
// This is not hypothetical. A nudge is spawned by whichever tool changed
// something, and inherits that tool's environment: on a machine where several
// agents run Mailman under their own names, every one of them would otherwise
// publish its mailbox into the operator's mirror.
func (c *CLI) checkIdentity() error {
	look := c.Look
	if look == nil {
		look = os.LookupEnv
	}
	// With its own credential, cq does not care what the environment says: it
	// overrides both variables for every child it runs.
	if c.Key != "" {
		return nil
	}
	who, ok := look(OrcUser)
	if !ok || who == "" {
		// Nothing to contradict. Mailman will refuse the first command for the
		// same reason, and its message about credentials is the better one.
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(who), strings.TrimSpace(c.User)) {
		return fault.Conflict{Reason: fmt.Sprintf(
			"%s is %q but this machine mirrors %q; refusing to publish one account's mail as another's.\n"+
				"  set CQ_KEY to %s's orc key so cq authenticates as %s regardless of who runs it",
			OrcUser, who, c.User, c.User, c.User)}
	}
	return nil
}

func (c *CLI) mailbox(ctx context.Context) (inbox, archive, mine []wireMessage, err error) {
	if inbox, err = c.listing(ctx, "inbox", "--all"); err != nil {
		return nil, nil, nil, err
	}
	if archive, err = c.listing(ctx, "archive"); err != nil {
		return nil, nil, nil, err
	}
	if mine, err = c.listing(ctx, "inbox", "--sent"); err != nil {
		return nil, nil, nil, err
	}
	return inbox, archive, mine, nil
}

// listing runs one Mailman listing command and decodes it.
func (c *CLI) listing(ctx context.Context, args ...string) ([]wireMessage, error) {
	out, err := c.run(ctx, c.mailman(), append(args, "--json")...)
	if err != nil {
		return nil, err
	}
	var msgs []wireMessage
	return msgs, decodeJSON(out, &msgs, c.mailman()+" "+strings.Join(args, " "))
}

// convos asks Mailman about each conversation the mail mentions.
//
// One command per thread, which is a handful: a conversation cq cannot ask
// about falls back to what its messages say, so a thread never disappears from
// the mailbox because one command failed.
func (c *CLI) convos(ctx context.Context, lists ...[]wireMessage) []protocol.Convo {
	uids := convoUIDs(lists...)
	if len(uids) == 0 {
		return []protocol.Convo{}
	}

	out := make([]protocol.Convo, 0, len(uids))
	missed := false
	for _, uid := range uids {
		raw, err := c.run(ctx, c.mailman(), "convo", uid, "--all", "--json")
		if err != nil {
			missed = true
			continue
		}
		var t wireThread
		if err := decodeJSON(raw, &t, c.mailman()+" convo"); err != nil || t.ID == "" {
			missed = true
			continue
		}
		out = append(out, t.protocol())
	}
	if !missed {
		return out
	}

	// Fill the gaps from the messages themselves, keeping what Mailman said
	// about the threads it did answer for.
	have := map[string]bool{}
	for _, cv := range out {
		have[cv.UID] = true
	}
	for _, cv := range convosFrom(lists...) {
		if !have[cv.UID] {
			out = append(out, cv)
		}
	}
	return out
}

func (c *CLI) tasks(ctx context.Context) ([]protocol.Task, error) {
	out, err := c.run(ctx, c.muff(), "pool", "--all", "--json")
	if err != nil {
		return nil, err
	}
	var wire []wireTask
	if err := decodeJSON(out, &wire, c.muff()+" pool"); err != nil {
		return nil, err
	}
	tasks := make([]protocol.Task, 0, len(wire))
	for _, t := range wire {
		task := t.protocol()
		task.Subtasks, task.Description = c.detail(ctx, t)
		tasks = append(tasks, task)
	}
	return tasks, nil
}

// detail asks Macmuffin for the two things one task carries that the board does not:
// its steps by name, and the prose that says what the work is.
//
// A second command per task, and only for the tasks that have either. `muff pool` is
// a board: it deliberately carries neither every step of every task nor forty
// descriptions, which is the right shape for a listing and the wrong one for a
// mirror somebody opens a task in. Reading them off the store behind Macmuffin's
// back would be the other way, and cq does not do that anywhere.
//
// The board says which tasks have what, so the extra call is skipped entirely for a
// task with neither — which in a pool of drafts and one-shot jobs is most of them.
//
// A task whose `info` fails keeps its counts and loses the detail: the board is what
// a mirror is for, and one command failing should cost the detail rather than the
// sync. The description then reads as described-but-empty, which is why `Described`
// travels separately — a browser must not offer to write a first description over
// the top of one it merely could not read.
func (c *CLI) detail(ctx context.Context, t wireTask) ([]protocol.Subtask, string) {
	if t.Total <= 0 && !t.Described {
		return nil, ""
	}
	raw, err := c.run(ctx, c.muff(), "info", t.Name, "--json")
	if err != nil {
		c.warn("the detail of %s could not be read, so only the board's summary is in this snapshot: %v", t.Name, err)
		return nil, ""
	}
	var full wireTask
	if err := decodeJSON(raw, &full, c.muff()+" info"); err != nil {
		c.warn("the detail of %s could not be read: %v", t.Name, err)
		return nil, ""
	}
	out := make([]protocol.Subtask, 0, len(full.Subtasks))
	for _, sub := range full.Subtasks {
		out = append(out, protocol.Subtask{Name: sub.Name, Done: sub.Done})
	}
	return out, full.Description
}

// admin collects the whole-Mailman view.
//
// Mailman has no command that lists every account's mail, and inventing one
// would mean cq deciding who may read what — which is Mailman's business, not
// cq's. So the panel is built from what the mirroring account can legitimately
// see: the accounts that exist, and the read receipts for its own mail.
// admin collects the whole-Mailman view.
//
// It is one command — `mailman admin mail` — because Mailman answers the
// question properly now. cq used to assemble this from what the mirrored
// account could see, plus a `check` per sent message, which was slow and,
// worse, incomplete: mail between two agents never reached the operator's
// mailbox and so never reached the panel that claims to show the whole store.
//
// The command is refused unless the mirrored account owns the store. That is
// Mailman's rule and cq does not work around it: a panel that showed everyone's
// mail to whoever happened to be running the sync would be the same disclosure
// the rule exists to prevent.
func (c *CLI) admin(ctx context.Context, bodies bool) (*protocol.AdminState, error) {
	usersOut, err := c.run(ctx, c.mailman(), "admin", "user", "list", "--json")
	if err != nil {
		return nil, err
	}
	var wireUsers []wireUser
	if err := decodeJSON(usersOut, &wireUsers, c.mailman()+" admin user list"); err != nil {
		return nil, err
	}
	users := make([]protocol.AdminUser, 0, len(wireUsers))
	for _, u := range wireUsers {
		users = append(users, u.protocol())
	}

	args := []string{"admin", "mail", "--json"}
	if !bodies {
		// Asked for, and stripped again below. The guarantee has to be cq's:
		// an operator who has not enabled bodies must not see one because a
		// flag was spelled differently upstream.
		args = append(args, "--no-bodies")
	}
	mailOut, err := c.run(ctx, c.mailman(), args...)
	if err != nil {
		return nil, err
	}
	var whole []wireWhole
	if err := decodeJSON(mailOut, &whole, c.mailman()+" admin mail"); err != nil {
		return nil, err
	}

	state := &protocol.AdminState{
		Users:        users,
		Messages:     make([]protocol.Message, 0, len(whole)),
		Receipts:     []protocol.Receipt{},
		MetadataOnly: !bodies,
	}
	for _, w := range whole {
		state.Messages = append(state.Messages, w.protocol(bodies))
		state.Receipts = append(state.Receipts, w.receipts()...)
	}
	return state, nil
}

// Apply performs one queued action by running the Mailman command it names.
//
// Each operation maps to exactly one command, because cq is a mirror of that
// API: inventing a verb here would mean inventing a behaviour Mailman does not
// have.
func (c *CLI) Apply(ctx context.Context, action protocol.Action) error {
	if err := action.Validate(); err != nil {
		return err
	}

	// The library verbs write files rather than running another tool, so they
	// leave here before anything is turned into a command line.
	if action.Op.TouchesLibrary() {
		return c.applyLibrary(action)
	}

	if action.Op.TouchesTasks() {
		return c.applyTask(ctx, action)
	}
	if action.Op.TouchesFleet() {
		return c.orc().Apply(ctx, action)
	}
	if action.Op == protocol.OpUpgrade {
		return c.upgrade(ctx)
	}
	if action.Op == protocol.OpLibraryRoot {
		return c.applyLibraryRoot(action)
	}

	var args []string
	switch action.Op {
	case protocol.OpSend:
		args = append([]string{"send", action.Args.Subject}, action.Args.To...)
		args = append(args, action.Args.Body)
	case protocol.OpReply:
		args = []string{"reply", puidQuery(action.Args.PUID), action.Args.Subject, action.Args.Body}
	case protocol.OpRead:
		args = []string{"read", puidQuery(action.Args.PUID)}
	case protocol.OpArchive:
		args = []string{"archive", puidQuery(action.Args.PUID)}
	case protocol.OpCC:
		args = []string{"cc", convoQuery(action.Args.ConvoUID), action.Args.User}
	default:
		return fault.Internal{Where: "source.CLI.Apply", Detail: "no command for operation " + string(action.Op)}
	}

	_, err := c.run(ctx, c.mailman(), args...)
	return err
}

// applyTask performs one queued action by running the Macmuffin command it names.
//
// One operation, one command, exactly as the Mailman half works and for the same
// reason: cq mirrors Macmuffin's API rather than reimplementing the pool, so a
// verb invented here would be a rule about tasks that Macmuffin does not have.
//
// Every command runs as the mirrored account, which is what makes the queue's
// authority the operator's rather than whoever's shell happened to trigger the
// sync — the same property `checkIdentity` protects for mail.
func (c *CLI) applyTask(ctx context.Context, action protocol.Action) error {
	a := action.Args

	var args []string
	switch action.Op {
	case protocol.OpTaskCreate:
		args = []string{"create", a.Task, strconv.Itoa(a.Priority), strconv.Itoa(a.Difficulty)}
	case protocol.OpTaskSubtask:
		// A subtask is `create --sub`, which is Macmuffin's own spelling. cq gives
		// it a separate operation because the queue has to be able to say which of
		// the two happened.
		args = []string{"create", a.Task, "--sub", a.Sub}
	case protocol.OpTaskPush:
		args = []string{"push", a.Task}
	case protocol.OpTaskClaim:
		args = []string{"claim", a.Task}
	case protocol.OpTaskAssign:
		args = []string{"assign", a.User, a.Task}
	case protocol.OpTaskInvite:
		args = []string{"invite", a.User, a.Task}
	case protocol.OpTaskKick:
		args = []string{"kick", a.User, a.Task}
	case protocol.OpTaskLeave:
		args = []string{"leave", a.Task}
	case protocol.OpTaskScope:
		args = append([]string{"scope", a.Task}, a.Paths...)
	case protocol.OpTaskDescribe:
		// Through a file rather than argv, for the reason tempMarkdown gives: a
		// description is up to 32 KiB of somebody's prose, and `ps` is public.
		path, done, err := tempMarkdown("cq-describe-*.md", a.Text)
		if err != nil {
			return err
		}
		defer done()
		args = []string{"describe", a.Task, "--set", path}
	case protocol.OpTaskDescribeClear:
		args = []string{"describe", a.Task, "--clear"}
	case protocol.OpTaskWorktree:
		args = []string{"worktree", a.Task, a.Path}
	case protocol.OpTaskStatus:
		args = []string{"status", a.Task, strconv.Itoa(a.Status)}
	case protocol.OpTaskComplete:
		args = []string{"complete", a.Task}
		if a.Sub != "" {
			args = append(args, "--sub", a.Sub)
		}
		if a.Force {
			args = append(args, "--force")
		}
	case protocol.OpTaskDelete:
		// --yes always: Macmuffin requires it whenever stdin is not a terminal,
		// which for a queued action is always. The confirmation happened in the
		// browser, hours ago and on another machine.
		args = []string{"delete", a.Task}
		if a.Sub != "" {
			args = append(args, "--sub", a.Sub)
		}
		args = append(args, "--yes")
	default:
		return fault.Internal{Where: "source.CLI.applyTask", Detail: "no command for operation " + string(action.Op)}
	}

	_, err := c.run(ctx, c.muff(), args...)
	return err
}

func puidQuery(puid int) string    { return `id="` + strconv.Itoa(puid) + `"` }
func convoQuery(uid string) string { return `convo="` + uid + `"` }

func (c *CLI) mailman() string {
	if c.Mailman == "" {
		return "mailman"
	}
	return c.Mailman
}

func (c *CLI) dock() string {
	if c.Dock == "" {
		return "dock"
	}
	return c.Dock
}

func (c *CLI) anno() string {
	if c.Anno == "" {
		return "anno"
	}
	return c.Anno
}

// upgrade pulls the tree and rebuilds every tool on this machine.
//
// The action carries nothing: what to pull and where to install are this machine's
// own settings. A path arriving over the wire and being handed to a build script is
// the shape of every remote-execution hole there has ever been, and the server is
// on somebody else's computer.
//
// It does not restart anything. Replacing a binary on unix leaves the running
// process on its old inode, so every tool here keeps working until it next execs —
// an orc session supervisor carries on, and picks the new binary up when it spawns.
// The one process that must come back new is whichever `cq` is watching, and that
// is its own decision: see cli.sync, which re-execs after the round it was in.
func (c *CLI) upgrade(ctx context.Context) error {
	report, err := c.Upgrade.Upgrade(ctx)
	if err != nil {
		return err
	}
	// Reported rather than returned: an action's result is a yes or a no plus a
	// message, and what an operator wants in the queue is which revision this
	// machine is on now.
	c.warn("upgraded %s: %s → %s, built %s", report.Source,
		orNone(report.Before), orNone(report.After), strings.Join(report.Built, " "))

	// And then: make sure something is still watching this machine.
	//
	// After the hook, not before, because a machine whose build failed does not
	// want a watcher started on the binaries that failed to replace. And its
	// failure does not fail the upgrade — the upgrade *worked*, and reporting it as
	// failed would have the operator chasing a build that is fine while the real
	// problem, that nothing is mirroring, goes unsaid. So it is said, separately.
	if c.EnsureWatch != nil {
		if err := c.EnsureWatch(); err != nil {
			c.warn("upgraded, but could not make sure this machine is still being mirrored: %v", err)
		}
	}
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// orc is the adapter for the fleet, pinned to the mirrored account.
//
// The credential matters here more than anywhere else in this file: an action
// queued from the browser is the *operator's* decision, and running it as whoever
// happened to trigger the sync would either fail on authority or, worse, act with
// somebody else's.
func (c *CLI) orc() *Orc {
	return &Orc{Command: c.orcCommand(), Env: c.childEnv(), Run: c.Run}
}

func (c *CLI) orcCommand() string {
	if c.Orc == "" {
		return "orc"
	}
	return c.Orc
}

func (c *CLI) muff() string {
	if c.Muff == "" {
		return "muff"
	}
	return c.Muff
}

// run invokes a tool and returns its standard output.
//
// Arguments are passed as a list, never through a shell, so a subject line
// containing a semicolon is a subject line and not a second command.
func (c *CLI) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if c.Run != nil {
		return c.Run(ctx, name, args...)
	}

	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = c.childEnv()
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut

	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, fault.IO{Op: "run", Subject: name, Err: fmt.Errorf("timed out after %s", Timeout)}
	}
	if err != nil {
		detail := strings.TrimSpace(errOut.String())
		if detail == "" {
			detail = err.Error()
		}
		failure := fault.IO{Op: "run", Subject: name + " " + strings.Join(args, " "), Err: fmt.Errorf("%s", detail)}

		// Orc's exit codes are shared, so a refusal is distinguishable from a
		// breakage without reading the message. cq needs the difference: being
		// told no is a state it can carry on in, and anything else is not.
		// The tool's message is what the operator should read; the process
		// error is kept underneath so a refusal stays recognisable.
		return nil, withCause{err: failure, cause: err}
	}
	if out.Len() > MaxOutputBytes {
		return nil, fault.IO{Op: "read the output of", Subject: name,
			Err: fmt.Errorf("output is %d bytes, limit is %d", out.Len(), MaxOutputBytes)}
	}
	return out.Bytes(), nil
}

// childEnv is the environment every Mailman and Macmuffin child runs with.
//
// Two things are forced. The nudge is suppressed, because reads have nothing to
// announce and an applied action was cq's own doing — a nudge there would ask cq
// to sync because cq synced. And the Orc identity is pinned to the mirrored
// account when cq has its credential, so a sync triggered by an agent still
// reads the operator's mailbox rather than the agent's.
func (c *CLI) childEnv() []string {
	env := append(os.Environ(), nudge.Suppress+"=1")
	if c.Key == "" {
		return env
	}
	// Appended last: for duplicate keys the child takes the final value, so
	// these override whatever was inherited.
	return append(env, OrcUser+"="+c.User, OrcKey+"="+c.Key)
}

// decodeJSON reads a tool's output.
//
// Unknown fields are accepted here, and only here. cq's own wire format refuses
// them, because both ends of it are cq and a field one end does not know means
// they disagree. The far end of *this* boundary is a tool with its own release
// cycle: refusing a field Mailman added would mean every improvement to Mailman
// breaks the mirror until cq is rebuilt.
func decodeJSON(data []byte, v any, what string) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		// A tool with nothing to report prints nothing, which is not an error.
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	if err := dec.Decode(v); err != nil {
		return fault.Parse{Where: what, Reason: err.Error()}
	}
	if dec.More() {
		return fault.Parse{Where: what, Reason: "output carries more than one JSON document"}
	}
	return nil
}
