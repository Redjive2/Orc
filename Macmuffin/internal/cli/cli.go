// Package cli implements Macmuffin's commands.
//
// Each command is a small method on an App, which carries the streams to read
// from and write to. Commands return errors; the mapping from error to exit
// code lives in one place — orc/common/fault — so a new failure mode cannot
// accidentally exit zero, and so `muff`, `anno`, and `mailman` mean the same
// thing by the same number.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/identity"
	"orc/common/nudge"
	"orc/common/user"
	"orc/macmuffin/internal/control"
	"orc/macmuffin/internal/notify"
	"orc/macmuffin/internal/render"
	"orc/macmuffin/internal/store"
	"orc/macmuffin/internal/style"
	"orc/macmuffin/internal/task"
	"orc/theme"
)

// App carries everything a command needs from the outside world. Every field is
// injected so the whole CLI is testable without a terminal, a home directory, or
// a real clock.
type App struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// Env supplies environment variables: the Orc credential, and where the
	// store lives when Root is empty.
	Env func(string) (string, bool)
	// Home is the user's home directory, used only to locate the default store.
	Home string
	// Root overrides where the store lives.
	Root string
	// Clock supplies timestamps.
	Clock clock.Clock
	// Colour asks for colour; it is still refused unless the stream is a
	// terminal, the scheme allows it, and the process is not an agent.
	Colour bool
	// Terminal reports whether stdout is a real terminal. It is injected
	// because a test writes to a buffer, which never is one.
	Terminal bool
	// ErrTerminal is the same question for stderr. They are asked separately
	// because `muff pool > board.txt` still has a terminal to be diagnosed on,
	// and a diagnostic that lost its colour because stdout was redirected would
	// be answering the wrong question.
	ErrTerminal bool
	// Width is the terminal width to lay tables out for. Zero takes the
	// renderer's default.
	Width int
	// Cwd is the working directory, used to find the worktree in force. Empty
	// asks the process.
	Cwd string
	// Notify sends a notification. Nil uses the real `mailman` binary; a test
	// passes a recorder so nothing is ever executed.
	Notify notify.Run
	// Control asks Orc whether the caller may direct an agent. Nil uses the
	// real `orc` binary; a test answers for itself.
	Control control.Check
	// Identity asks Orc whether the caller is who they claim. Nil uses the
	// real `orc` binary; a test answers for itself.
	Identity control.Verifier

	// out and err are the resolved palettes, filled in by defaults(). They are
	// resolved before anything else runs so that a failure on the way to a
	// store — a missing credential, a bad argument — is painted like everything
	// else rather than being the one plain thing muff prints.
	out style.Palette
	err style.Palette
}

// EnvTask names the task in force, overriding the worktree binding.
const EnvTask = "MUFF_TASK"

// Colour flags. They are global rather than per-command, and are taken off the
// line before dispatch, so `muff --no-color pool` and `muff pool --no-color`
// both work and no command has to know they exist.
//
// The environment (NO_COLOR, ORC_AGENT, ORC_THEME=none) already turns colour
// off, but an environment variable is awkward for a caller assembling one
// command — which is what Orc will be doing — so there is a flag for the same
// thing. --color forces it on for the opposite case: a caller that pipes muff
// somewhere that renders escapes.
const (
	FlagNoColour = "--no-color"
	FlagColour   = "--color"
)

// takeColourFlags removes the colour flags and reports what they asked for.
func takeColourFlags(args []string) (rest []string, force, off bool) {
	rest = make([]string, 0, len(args))
	for _, arg := range args {
		switch arg {
		case FlagNoColour:
			off = true
		case FlagColour:
			force = true
		default:
			rest = append(rest, arg)
		}
	}
	return rest, force, off
}

// cwd returns the working directory the commands should reason about.
func (a App) cwd() string {
	if a.Cwd != "" {
		return a.Cwd
	}
	got, err := os.Getwd()
	if err != nil {
		return "."
	}
	return got
}

// Main runs a command line and returns the process exit code.
//
// It is the only function that turns an error into a status, and it recovers
// from panics so that even a defect produces a diagnosed exit rather than a
// crash with a task half-written.
func Main(app App, args []string) (code int) {
	defer func() {
		if r := recover(); r != nil {
			_, _ = fmt.Fprintf(app.Stderr, "%s %v\n", app.err.Alarm("muff:"), fault.Internal{
				Where:  "cli.Main",
				Detail: fmt.Sprintf("panic: %v", r),
			})
			code = fault.CodeInternal
		}
	}()

	if app.Stdout == nil || app.Stderr == nil {
		// Without streams there is nowhere to report; the code still says why.
		return fault.CodeInternal
	}

	args, force, off := takeColourFlags(args)
	app.Colour = app.Colour && !off
	if force {
		app.Colour, app.Terminal, app.ErrTerminal = true, true, true
	}
	app = app.defaults()

	err := app.dispatch(args)
	if err == nil {
		return fault.CodeOK
	}

	// Diagnostics go to stderr; if stderr itself is broken there is nowhere to
	// report that, so the exit code carries the outcome on its own.
	code = fault.Code(err)
	_, _ = fmt.Fprintf(app.Stderr, "%s %v\n", app.err.Alarm("muff:"), err)
	// The full screen no longer follows every usage error: a refusal that has
	// already said what was wrong reads better without fifty lines under it, and
	// the refusals that need one carry their own pointer to `muff help`.
	//
	// `muff` on its own is the exception, and the only one: nothing was named, so
	// there is no refusal to read and the useful answer is what the verbs are.
	if code == fault.CodeUsage && len(args) == 0 {
		_, _ = fmt.Fprintln(app.Stderr, "\n"+brief(app.err))
	}
	return code
}

func (a App) defaults() App {
	if a.Env == nil {
		a.Env = os.LookupEnv
	}
	if a.Clock == nil {
		a.Clock = clock.Real{}
	}
	a.out = a.resolve(a.Terminal)
	a.err = a.resolve(a.ErrTerminal)
	return a
}

// resolve picks the palette for one stream.
//
// A misspelled ORC_THEME leaves it plain here rather than failing: this runs
// before any command does, and the command itself still reports the bad setting
// as a usage error. Refusing to draw is the right answer; refusing to *say* why
// would leave the caller with a silent tool.
func (a App) resolve(terminal bool) style.Palette {
	cfg, err := theme.Resolve(theme.Look(a.Env), a.Colour && terminal)
	if err != nil {
		return style.Plain()
	}
	return style.New(cfg.Palette)
}

func (a App) dispatch(args []string) error {
	if len(args) == 0 {
		return fault.Usage{Reason: "no command given"}
	}
	command, rest := args[0], args[1:]

	// Help is answered before anything else: an agent with no identity should
	// still be able to find out what the identity is meant to be.
	switch command {
	case "help", "-h", "--help":
		return a.help(rest)
	}

	if err := a.route(command, rest); err != nil {
		return err
	}

	// The mirror is told only after the command succeeded, and only if it
	// actually changed something. Nothing about this can fail the command: the
	// work is already recorded, and cq being late is cq's problem.
	if mutates(command) {
		nudge.After()
	}
	return nil
}

// route runs one command. It is separate from dispatch so that "did it work"
// and "does the mirror need to know" are answered in one place rather than at
// each of a dozen returns.
func (a App) route(command string, rest []string) error {
	switch command {
	case "create":
		return a.create(rest)
	case "push":
		return a.push(rest)
	case "claim":
		return a.claim(rest)
	case "pool":
		return a.pool(rest)
	case "info":
		return a.info(rest)
	case "scope":
		return a.scope(rest)
	case "worktree":
		return a.worktree(rest)
	case "rebind":
		return a.rebind(rest)
	case "check-scope":
		return a.checkScope(rest)
	case "verify":
		return a.verify(rest)
	case "status":
		return a.status(rest)
	case "complete":
		return a.complete(rest)
	case "delete":
		return a.deleteTask(rest)
	case "invite":
		return a.invite(rest)
	case "kick":
		return a.kick(rest)
	case "leave":
		return a.leave(rest)
	case "assign":
		return a.assign(rest)
	default:
		return unknown(command)
	}
}

// session is an authenticated command's context: who is running it, and the
// store they are running it against.
type session struct {
	app   App
	store *store.Store
	who   user.Name
	paint style.Palette

	// verified records whether an authority confirmed the caller's identity.
	// It is not a permission — nothing branches on it — but `verify` reports
	// it, because "these permissions rest on an unchecked claim" is exactly the
	// sort of thing a health check exists to say out loud.
	verified bool
}

// verify asks Orc whether the claimed identity is the credential's real one.
//
// Only a definite no stops the command. Where there is no authority to ask, the
// claim stands and the session records that nobody checked — refusing every
// command because Orc is not installed would make `muff` unusable on a machine
// that has no fleet, which is how it worked before Orc existed and how the mock
// stores and the standalone case still work.
func (a App) confirm(claimed user.Name) (bool, error) {
	check := a.Identity
	if check == nil {
		check = control.Verified
	}

	err := check(claimed)
	if err == nil {
		return true, nil
	}

	var unverifiable control.Unverifiable
	if errors.As(err, &unverifiable) {
		return false, nil
	}
	return false, err
}

// begin opens the store and authenticates.
//
// Authentication happens before any argument is examined, so an agent with no
// identity is told that rather than told its arguments are malformed.
func (a App) begin() (session, error) {
	cred, err := identity.New(identity.Env(a.Env)).Resolve()
	if err != nil {
		return session{}, err
	}
	verified, err := a.confirm(cred.Name())
	if err != nil {
		return session{}, err
	}

	root := a.Root
	if root == "" {
		home := a.Home
		if home == "" {
			if h, err := os.UserHomeDir(); err == nil {
				home = h
			}
		}
		if root, err = store.DefaultRoot(store.Env(a.Env), home); err != nil {
			return session{}, err
		}
	}

	s, err := store.Open(root, a.Clock)
	if err != nil {
		return session{}, err
	}
	cfg, err := theme.Resolve(theme.Look(a.Env), a.Colour && a.Terminal)
	if err != nil {
		return session{}, fault.Usage{Reason: err.Error()}
	}
	got := session{app: a, store: s, who: cred.Name(), verified: verified, paint: style.New(cfg.Palette)}
	got.drain()
	return got, nil
}

// drain retries queued notifications before the command does its own work.
//
// Every command does this, so a notice that failed once is retried by whichever
// agent next touches the store — no daemon, no timer, and no notice waiting for
// the process that queued it to happen to run again.
//
// It never fails a command. The work the caller asked for has nothing to do
// with somebody else's undelivered mail, and a tracker that refused to show a
// board because a notification was stuck would be worse than one that mentions
// it and carries on.
func (s session) drain() {
	courier, err := notify.New(s.store, s.app.Notify)
	if err != nil {
		return
	}
	sent, waiting, stuck, err := courier.Drain()
	if err != nil {
		s.app.note("the outbox could not be read: %v", err)
		return
	}
	if sent > 0 {
		s.app.note("delivered %d queued notice%s", sent, plural(sent))
	}
	if waiting > 0 {
		s.app.note("%d notice%s still undelivered; they will be retried", waiting, plural(waiting))
	}
	if stuck > 0 {
		s.app.note("%d notice%s gave up after %d attempts — %s lists them",
			stuck, plural(stuck), store.MaxAttempts, s.app.err.Command("muff verify"))
	}
}

// resolve turns a caller's spelling into a task name, and reports the mapping
// when normalisation changed it — so `muff push "Fix The Parser"` says which
// task it actually pushed.
func (s session) resolve(raw string) (task.Name, error) {
	name, err := task.ParseName(raw)
	if err != nil {
		return task.Name{}, err
	}
	if name.Renamed(raw) {
		s.app.note("%q is task %s", raw, name)
	}
	return name, nil
}

// palette resolves the colour scheme for stdout. A misspelled ORC_THEME is a
// usage error rather than a silent fall back to the default: a setting that
// quietly does nothing is one the operator concludes is broken.
func (s session) palette() style.Palette { return s.paint }

// width is the terminal width to lay tables out for.
func (a App) width() int {
	if a.Width > 0 {
		return a.Width
	}
	return render.DefaultWidth
}

// write emits pre-rendered output verbatim.
func (a App) write(text string) error {
	_, err := io.WriteString(a.Stdout, text)
	return err
}

// say writes a line to stdout, returning the error rather than dropping it: a
// closed pipe should fail the command, not be ignored.
func (a App) say(line string) error {
	_, err := io.WriteString(a.Stdout, line+"\n")
	return err
}

// note reports something the caller should know that is not a failure. It goes
// to stderr so stdout stays pipeable.
func (a App) note(format string, args ...any) {
	// Nothing can be done about a broken stderr, and failing a command that
	// otherwise succeeded because its footnote could not be printed would be
	// worse than the footnote being lost.
	_, _ = fmt.Fprintf(a.Stderr, "%s %s\n", a.err.Alarm("muff:"), fmt.Sprintf(format, args...))
}

// exactly checks an argument count, naming what was expected.
func exactly(args []string, n int, what string) error {
	if len(args) == n {
		return nil
	}
	return fault.Usage{Reason: fmt.Sprintf("%s, got %d argument%s", what, len(args), plural(len(args)))}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// options describes the flags a command accepts: switches that are simply
// present, and values that take the argument after them.
type options struct {
	switches map[string]*bool
	values   map[string]*string
}

// flagged splits recognised options out of the arguments.
//
// Everything after a bare "--" is positional whatever it looks like, which is
// what lets a value legitimately begin with a dash. An unknown option is a
// usage error rather than a positional argument: `muff complete x --forse`
// silently treating the typo as a task name is exactly the mistake a tracker
// must not make.
func flagged(args []string, opts options) ([]string, error) {
	var rest []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return append(rest, args[i+1:]...), nil

		case strings.HasPrefix(arg, "--"):
			name, inline, joined := strings.Cut(arg, "=")

			if set, ok := opts.switches[name]; ok {
				if joined {
					return nil, fault.Usage{Reason: fmt.Sprintf("%s takes no value", name)}
				}
				*set = true
				continue
			}
			if set, ok := opts.values[name]; ok {
				if joined {
					*set = inline
					continue
				}
				if i+1 >= len(args) {
					return nil, fault.Usage{Reason: fmt.Sprintf("%s needs a value", name)}
				}
				i++
				*set = args[i]
				continue
			}
			return nil, fault.Usage{Reason: fmt.Sprintf("unknown option %q", arg)}

		default:
			rest = append(rest, arg)
		}
	}
	return rest, nil
}

// switches builds an option set with no value flags.
func switches(m map[string]*bool) options { return options{switches: m} }
