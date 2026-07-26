// Package cli implements Mailman's commands.
//
// Each command is a small method on an App, which carries the streams to read
// from and write to. Commands return errors; the mapping from error to exit
// code lives in one place, Code, so a new failure mode cannot accidentally exit
// zero. The shape is Anno's, and the shared exit codes mean the same things in
// both tools.
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
	"orc/mailman/internal/query"
	"orc/mailman/internal/render"
	"orc/mailman/internal/store"
	"orc/mailman/internal/style"
	"orc/mailman/internal/view"
	"orc/theme"
)

// Exit codes. They are stable: hooks branch on them, and they agree with
// Anno's for every code both tools can produce.
const (
	CodeOK        = 0
	CodeUsage     = 1
	CodeNotFound  = 2
	CodeAmbiguous = 3
	CodeParse     = 4
	CodeIO        = 5
	CodeConflict  = 6
	CodeAuth      = 7
	// CodeDenied is authenticated, but not permitted: in practice, an account
	// that is not the store's owner asking to read the store whole. It is
	// distinct from CodeAuth because the fix is different — a wrong key is
	// fixable by the caller, and being the wrong account is not.
	CodeDenied = 8
	// CodeEscape is a path that resolved outside the root it was measured
	// against: in practice, this tool inside an Orcprobe probe being pointed at
	// a real store. It is 11 to match the shared table in
	// Claude/Docs/ExitCodes.md — a containment failure must not read as an
	// ordinary refusal to whatever is watching.
	CodeEscape   = 11
	CodeInternal = 70
)

// App carries everything a command needs from the outside world. Every field
// is injected so the whole CLI is testable without a terminal, a home
// directory, or a real clock.
type App struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// Env supplies environment variables. It carries both the Orc credential
	// and, when Root is empty, where the store lives.
	Env func(string) (string, bool)
	// Home is the user's home directory, used only to locate the default store.
	// The credential never comes from a file, so this has nothing to do with
	// authentication.
	Home string
	// Root overrides where the store lives. Empty means resolve it from Env.
	Root string
	// Clock supplies timestamps.
	Clock clock.Clock
	// Width is the terminal width to lay tables out for.
	Width int
	// Colour asks for colour; it is still refused unless the stream is a
	// terminal, the scheme allows it, and the process is not an agent.
	Colour bool
	// Terminal reports whether stdout is a real terminal. It is injected
	// because a test writes to a buffer, which never is one, and a rule that
	// cannot be tested is a rule that will eventually be wrong.
	Terminal bool
	// ErrTerminal is the same question for stderr. They are asked separately
	// because `mailman inbox > mail.txt` still has a terminal to be diagnosed on,
	// and a diagnostic that lost its colour because stdout was redirected would be
	// answering the wrong question.
	ErrTerminal bool

	// out and err are the resolved palettes, filled in by defaults().
	out style.Palette
	err style.Palette
}

// Main runs a command line and returns the process exit code.
//
// It is the only function that converts an error into a status, and it recovers
// from panics so that even a defect in Mailman produces a diagnosed exit rather
// than a crash with mail half-written.
func Main(app App, args []string) (code int) {
	defer func() {
		if r := recover(); r != nil {
			_, _ = fmt.Fprintf(app.Stderr, "%s %v\n", app.err.Alarm("mailman:"), fault.Internal{
				Where:  "cli.Main",
				Detail: fmt.Sprintf("panic: %v", r),
			})
			code = CodeInternal
		}
	}()

	if app.Stdout == nil || app.Stderr == nil {
		// Without streams there is nowhere to report; the code still says why.
		return CodeInternal
	}
	app = app.defaults()

	err := app.dispatch(args)
	if err == nil {
		return CodeOK
	}

	// Diagnostics go to stderr; if stderr itself is broken there is nowhere to
	// report that, so the exit code carries the outcome on its own.
	code = Code(err)
	_, _ = fmt.Fprintf(app.Stderr, "%s %v\n", app.err.Alarm("mailman:"), err)
	// The full screen no longer follows every usage error: a refusal that has
	// already said what was wrong reads better without sixty lines under it, and
	// the refusals that need one carry their own pointer to `mailman help`.
	//
	// `mailman` on its own is the exception, and the only one: nothing was named,
	// so there is no refusal to read and the useful answer is what the verbs are.
	if code == CodeUsage && len(args) == 0 {
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
	if a.Width <= 0 {
		a.Width = render.DefaultWidth
	}
	// Resolved before anything else runs, so that a failure on the way to the store
	// — a missing credential, a bad argument — is painted like everything else
	// rather than being the one plain thing mailman prints.
	a.out = a.palette(a.Terminal)
	a.err = a.palette(a.ErrTerminal)
	return a
}

// palette picks the colours for one stream.
//
// A misspelled ORC_THEME leaves it plain here rather than failing: this runs before
// any command does, and the command itself still reports the bad setting as a usage
// error. Refusing to draw is right; refusing to *say* why would leave a silent tool.
func (a App) palette(terminal bool) style.Palette {
	cfg, err := theme.Resolve(theme.Look(a.Env), a.Colour && terminal)
	if err != nil {
		return style.Plain()
	}
	return style.New(cfg.Palette)
}

// Code maps an error to an exit code. Order matters: the most specific
// classification wins.
func Code(err error) int {
	switch {
	case err == nil:
		return CodeOK
	case errors.Is(err, fault.ErrInternal):
		return CodeInternal
	case errors.Is(err, fault.ErrEscape):
		return CodeEscape
	case errors.Is(err, fault.ErrUsage):
		return CodeUsage
	case errors.Is(err, fault.ErrAuth):
		return CodeAuth
	case errors.Is(err, fault.ErrDenied):
		return CodeDenied
	case errors.Is(err, fault.ErrAmbiguous):
		return CodeAmbiguous
	case errors.Is(err, fault.ErrNotFound):
		return CodeNotFound
	case errors.Is(err, fault.ErrConflict):
		return CodeConflict
	case errors.Is(err, fault.ErrParse):
		return CodeParse
	case errors.Is(err, fault.ErrIO):
		return CodeIO
	default:
		return CodeInternal
	}
}

// flags are the options every command shares.
type flags struct {
	all      bool
	sent     bool
	yes      bool
	noColor  bool
	json     bool
	noBodies bool
	width    int

	// key carries a caller-chosen key for `admin user add`, and keyGiven says
	// whether it was supplied at all — "--key ''" and no --key are different
	// mistakes. See adminAdd for why Orc needs to choose the key rather than
	// read one back.
	key      string
	keyGiven bool
}

// parseFlags splits options out of the arguments.
//
// Options are recognised anywhere, and everything after a bare "--" is a
// positional argument whatever it looks like — which is what lets a subject or
// a body legitimately begin with a dash.
func parseFlags(args []string) ([]string, flags, error) {
	var f flags
	var rest []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return append(rest, args[i+1:]...), f, nil
		case arg == "--all":
			f.all = true
		case arg == "--sent":
			f.sent = true
		case arg == "--yes":
			f.yes = true
		case arg == "--no-color", arg == "--no-colour":
			f.noColor = true
		case arg == "--json":
			// Output for another program. Colour would be corruption in it, so
			// the palette is turned off with the same flag.
			f.json, f.noColor = true, true
		case arg == "--no-bodies":
			f.noBodies = true
		case arg == "--key":
			if i+1 >= len(args) {
				return nil, f, fault.Usage{Reason: "--key needs a key, or - to read one from stdin"}
			}
			i++
			f.key, f.keyGiven = args[i], true
		case strings.HasPrefix(arg, "--key="):
			f.key, f.keyGiven = strings.TrimPrefix(arg, "--key="), true
		case arg == "--width":
			if i+1 >= len(args) {
				return nil, f, fault.Usage{Reason: "--width needs a number"}
			}
			i++
			n, err := atoi(args[i])
			if err != nil {
				return nil, f, fault.Usage{Reason: fmt.Sprintf("--width %q is not a number", args[i])}
			}
			f.width = n
		case strings.HasPrefix(arg, "--width="):
			n, err := atoi(strings.TrimPrefix(arg, "--width="))
			if err != nil {
				return nil, f, fault.Usage{Reason: fmt.Sprintf("bad %q", arg)}
			}
			f.width = n
		case strings.HasPrefix(arg, "--"):
			return nil, f, fault.Usage{Reason: fmt.Sprintf("unknown option %q", arg)}
		default:
			rest = append(rest, arg)
		}
	}
	return rest, f, nil
}

func (a App) dispatch(args []string) error {
	if len(args) == 0 {
		return fault.Usage{Reason: "no command given"}
	}
	command, raw := args[0], args[1:]

	// Help is answered before anything else: an agent with no identity should
	// still be able to find out what the identity is meant to be.
	switch command {
	case "help", "-h", "--help":
		return a.help(raw)
	}

	rest, f, err := parseFlags(raw)
	if err != nil {
		return err
	}
	if f.width > 0 {
		a.Width = f.width
	}
	a.Colour = a.Colour && !f.noColor

	if err := a.route(command, rest, f); err != nil {
		return err
	}

	// The mirror is told only after the command succeeded, and only if it
	// actually changed something. Nothing about this can fail the command: the
	// mail has already been delivered, and cq being late is cq's problem.
	if mutates(command, rest) {
		nudge.After()
	}
	return nil
}

// route runs one command. It is separate from dispatch so that "did it work"
// and "does the mirror need to know" are answered in one place rather than at
// each of a dozen returns.
func (a App) route(command string, rest []string, f flags) error {
	switch command {
	case "admin":
		return a.admin(rest, f)
	case "inbox":
		return a.inbox(rest, f)
	case "open":
		return a.open(rest, f)
	case "convo":
		return a.convo(rest, f)
	case "send":
		return a.send(rest, f)
	case "reply":
		return a.reply(rest, f)
	case "archive":
		return a.archive(rest, f)
	case "prune":
		return a.prune(rest, f)
	case "read":
		return a.read(rest, f)
	case "check":
		return a.check(rest, f)
	case "cc":
		return a.cc(rest, f)
	case "verify":
		return a.verify(rest, f)
	default:
		return unknown(command)
	}
}

// session is an authenticated command's context: who is running it, the store
// they are running it against, and how to draw the result.
type session struct {
	app     App
	store   *store.Store
	who     user.Name
	palette style.Palette
}

// begin opens the store and authenticates.
//
// Authentication happens here, before any argument is examined, so an agent
// with no identity is told that rather than told its query is malformed.
func (a App) begin() (session, error) {
	s, err := a.openStore()
	if err != nil {
		return session{}, err
	}

	cred, err := identity.New(identity.Env(a.Env)).Resolve()
	if err != nil {
		return session{}, err
	}
	if err := s.Authenticate(cred.Name(), cred.Key()); err != nil {
		return session{}, err
	}

	palette, err := a.paint()
	if err != nil {
		return session{}, err
	}
	return session{app: a, store: s, who: cred.Name(), palette: palette}, nil
}

// openStore opens the store without authenticating, which is what the
// provisioning command needs.
func (a App) openStore() (*store.Store, error) {
	root := a.Root
	if root == "" {
		home := a.Home
		if home == "" {
			if h, err := os.UserHomeDir(); err == nil {
				home = h
			}
		}
		var err error
		if root, err = store.DefaultRoot(a.Env, home); err != nil {
			return nil, err
		}
	}
	return store.Open(root, a.Clock)
}

// paint resolves the colour scheme for stdout.
//
// A misspelled ORC_THEME is reported rather than quietly falling back to the
// default: a setting that silently does nothing is one the operator concludes
// is broken. Everything else — an agent, NO_COLOR, a pipe — simply produces a
// plain palette, because none of those is a mistake.
func (a App) paint() (style.Palette, error) {
	cfg, err := theme.Resolve(theme.Look(a.Env), a.Colour && a.Terminal)
	if err != nil {
		return style.Plain(), fault.Usage{Reason: err.Error()}
	}
	return style.New(cfg.Palette), nil
}

// mailbox loads the caller's own view of the store.
func (s session) mailbox() (view.Mailbox, error) {
	box, err := view.Load(s.store, s.who)
	if err != nil {
		return view.Mailbox{}, err
	}
	// Damage is reported and stepped over. One unreadable file must not hide
	// the rest of someone's mail, but an inbox that quietly shows nine of ten
	// messages is worse than one that shows nine and says so.
	for _, d := range box.Damaged() {
		s.app.note("message %s could not be read and is not shown: %v", d.MID, d.Err)
	}
	if box.Skipped() > 0 {
		s.app.note("%d bytes at the end of your mailbox journal were left by an interrupted write and were ignored", box.Skipped())
	}
	return box, nil
}

// now is the instant relative query terms are measured from.
func (s session) now() query.Now { return query.At(s.app.Clock.Now()) }

// say writes a line to stdout, returning the error rather than dropping it: a
// closed pipe should fail the command, not be ignored.
func (a App) say(line string) error {
	_, err := io.WriteString(a.Stdout, line+"\n")
	return err
}

// write writes pre-rendered output verbatim.
func (a App) write(text string) error {
	_, err := io.WriteString(a.Stdout, text)
	return err
}

// note reports something the caller should know that is not a failure. It goes
// to stderr so stdout stays pipeable.
func (a App) note(format string, args ...any) {
	// Nothing can be done about a broken stderr, and failing a command that
	// otherwise succeeded because its footnote could not be printed would be
	// worse than the footnote being lost.
	_, _ = fmt.Fprintf(a.Stderr, "mailman: %s\n", fmt.Sprintf(format, args...))
}

func atoi(s string) (int, error) {
	if s == "" {
		return 0, fault.Usage{Reason: "empty number"}
	}
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, fault.Usage{Reason: "not a number"}
		}
		n = n*10 + int(c-'0')
		if n > 1<<20 {
			return 0, fault.Usage{Reason: "number is too large"}
		}
	}
	return n, nil
}
