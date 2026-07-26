// Package cli implements Orcprobe's commands.
//
// Each command is a small method on an App, which carries the streams to read
// from and write to and the few facts about the outside world orcprobe needs.
// Commands return errors; the mapping from error to exit code lives in one
// place, Code, so a new failure mode cannot accidentally exit zero. The shape
// is Mailman's, and the shared exit codes mean the same things in both tools.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"orc/orcprobe/internal/clock"
	"orc/orcprobe/internal/fault"
	"orc/orcprobe/internal/probe"
	"orc/orcprobe/internal/render"
	"orc/orcprobe/internal/source"
	"orc/orcprobe/internal/style"
	"orc/theme"
)

// Exit codes. They are stable: hooks branch on them, and they agree with
// Mailman's and Anno's for every code all three can produce. Escape is
// Orcprobe's own, and it is deliberately its own number: "something tried to
// reach the real world" is not a usage error and must never be read as one.
const (
	CodeOK        = 0
	CodeUsage     = 1
	CodeNotFound  = 2
	CodeAmbiguous = 3
	CodeParse     = 4
	CodeIO        = 5
	CodeConflict  = 6
	CodeAuth      = 7
	// CodeEscape is 11, matching the shared table in Claude/Docs/ExitCodes.md,
	// where 9 means "out of scope" and 11 means "a path resolved outside the
	// root it was measured against". Orcprobe used 9 before that table existed,
	// which would have made a hook read a containment failure as a scope
	// violation — the two things a probe most needs to tell apart.
	CodeEscape   = 11
	CodeInternal = 70
)

// The colour flags, named so the help can print them and the parser can take
// them from anywhere in a command line.
const (
	FlagNoColour = "--no-color"
	FlagColour   = "--color"
)

// takeColourFlags removes the colour flags and reports what they asked for.
//
// Both spellings of "colour" are accepted, because half this tree's prose uses
// one and every other CLI in the world uses the other.
func takeColourFlags(args []string) (rest []string, force, off bool) {
	rest = make([]string, 0, len(args))
	for _, arg := range args {
		switch arg {
		case FlagNoColour, "--no-colour":
			off = true
		case FlagColour, "--colour":
			force = true
		default:
			rest = append(rest, arg)
		}
	}
	return rest, force, off
}

// App carries everything a command needs from the outside world. Every field is
// injected so the whole CLI is testable without a terminal, a home directory, a
// real clock, or a real store.
type App struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// Env supplies environment variables: where the real stores are, and where
	// probes live.
	Env source.Env
	// Home is the user's home directory, used to locate stores and the Claude
	// configuration.
	Home string
	// Cwd is where the command was run, used to find the repo to copy.
	Cwd string
	// Exe is the running orcprobe binary, used to find orcprobe-shim beside it.
	Exe string
	// Path is the PATH a probe's own bin directory is prepended to.
	Path string
	// Shell is the shell `orcprobe shell` starts.
	Shell string
	// Environ is the environment a probe's own is layered onto, in os.Environ
	// form. It is a field rather than a call so a test can hand a command a
	// world of exactly three variables and know what it got.
	Environ []string
	// Root overrides where probes live. Empty means resolve it from Env.
	Root string

	Clock clock.Clock
	Width int
	// Colour asks for colour; it is still refused unless the stream is a
	// terminal and the scheme allows it.
	Colour bool
	// Terminal reports whether stdout is a real terminal. Injected because a
	// test writes to a buffer, which never is one.
	Terminal bool
	// ErrTerminal is the same question about stderr. The two are separate
	// because the streams are: see resolve.
	ErrTerminal bool

	// out and err are the palettes for the two streams, resolved once in
	// defaults so no command has to think about it.
	out, err style.Palette
}

// Main runs a command line and returns the process exit code.
//
// It is the only function that converts an error into a status, and it recovers
// from panics so that even a defect in Orcprobe produces a diagnosed exit rather
// than a crash halfway through creating a probe.
func Main(app App, args []string) (code int) {
	defer func() {
		if r := recover(); r != nil {
			_, _ = fmt.Fprintf(app.Stderr, "orcprobe: %v\n", fault.Internal{
				Where:  "cli.Main",
				Detail: fmt.Sprintf("panic: %v", r),
			})
			code = CodeInternal
		}
	}()

	if app.Stdout == nil || app.Stderr == nil {
		return CodeInternal
	}
	app = app.defaults()

	err := app.dispatch(args)
	if err == nil {
		return CodeOK
	}

	code = Code(err)
	// A child's exit status is the command's outcome, not a failure of
	// orcprobe, so it is passed through silently.
	var status exitStatus
	if errors.As(err, &status) {
		return int(status)
	}

	_, _ = fmt.Fprintf(app.Stderr, "orcprobe: %v\n", err)
	// The full screen no longer follows every usage error: a refusal that has
	// already said what was wrong reads better without the query language under
	// it, and the refusals that need one point at `orcprobe help` themselves.
	//
	// `orcprobe` on its own is the exception, and the only one: nothing was named,
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
	a.out = a.resolve(a.Terminal)
	a.err = a.resolve(a.ErrTerminal)
	return a
}

// usageText renders the help for a stream, so a usage error on stderr is
// painted for stderr rather than for stdout.
func usageText(p style.Palette) string { return usage(p) }

// exitStatus carries a child process's exit code back to Main.
type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("command exited with status %d", int(e)) }

// Code maps an error to an exit code. Order matters: the most specific
// classification wins.
func Code(err error) int {
	var status exitStatus
	switch {
	case err == nil:
		return CodeOK
	case errors.As(err, &status):
		return int(status)
	case errors.Is(err, fault.ErrInternal):
		return CodeInternal
	case errors.Is(err, fault.ErrEscape):
		return CodeEscape
	case errors.Is(err, fault.ErrUsage):
		return CodeUsage
	case errors.Is(err, fault.ErrAuth):
		return CodeAuth
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
	probe     string
	as        string
	repo      string
	noRepo    bool
	fakeHome  bool
	liveState bool
	source    bool
	strict    bool
	since     string
	tool      string
	yes       bool
	width     int
}

// parseFlags splits options out of the arguments. Everything after a bare "--"
// is positional whatever it looks like, which is what lets `orcprobe as bob --
// mailman inbox --all` pass flags through to the command being run.
func parseFlags(args []string) ([]string, flags, error) {
	var f flags
	var rest []string

	value := func(i *int, name string) (string, error) {
		if *i+1 >= len(args) {
			return "", fault.Usage{Reason: name + " needs a value"}
		}
		*i++
		return args[*i], nil
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return append(rest, args[i+1:]...), f, nil
		case arg == "--no-repo":
			f.noRepo = true
		case arg == "--fake-home":
			f.fakeHome = true
		case arg == "--live-state":
			f.liveState = true
		case arg == "--source":
			f.source = true
		case arg == "--strict":
			f.strict = true
		case arg == "--yes":
			f.yes = true
		case arg == "--probe":
			v, err := value(&i, arg)
			if err != nil {
				return nil, f, err
			}
			f.probe = v
		case arg == "--as":
			v, err := value(&i, arg)
			if err != nil {
				return nil, f, err
			}
			f.as = v
		case arg == "--repo":
			v, err := value(&i, arg)
			if err != nil {
				return nil, f, err
			}
			f.repo = v
		case arg == "--since":
			v, err := value(&i, arg)
			if err != nil {
				return nil, f, err
			}
			f.since = v
		case arg == "--tool":
			v, err := value(&i, arg)
			if err != nil {
				return nil, f, err
			}
			f.tool = v
		case arg == "--width":
			v, err := value(&i, arg)
			if err != nil {
				return nil, f, err
			}
			n, err := atoi(v)
			if err != nil {
				return nil, f, fault.Usage{Reason: fmt.Sprintf("--width %q is not a number", v)}
			}
			f.width = n
		case strings.HasPrefix(arg, "--probe="):
			f.probe = strings.TrimPrefix(arg, "--probe=")
		case strings.HasPrefix(arg, "--as="):
			f.as = strings.TrimPrefix(arg, "--as=")
		case strings.HasPrefix(arg, "--repo="):
			f.repo = strings.TrimPrefix(arg, "--repo=")
		case strings.HasPrefix(arg, "--since="):
			f.since = strings.TrimPrefix(arg, "--since=")
		case strings.HasPrefix(arg, "--tool="):
			f.tool = strings.TrimPrefix(arg, "--tool=")
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
	// The colour flags are taken before anything else, from anywhere in the
	// line, for one reason: `help` answers before the rest of the parsing runs,
	// and would otherwise be the single screen a caller could not colour or
	// decolour. They are also the only flags that mean the same thing for every
	// command, so parsing them once is honest rather than convenient.
	args, force, off := takeColourFlags(args)
	if err := a.checkTheme(); err != nil {
		return err
	}
	a.Colour = a.Colour && !off
	// --color forces colour onto a stream that is not a terminal, which is how
	// a person pipes a coloured screen into a pager. It cannot defeat ORC_AGENT
	// or NO_COLOR: turning colour off for every tool at once must not be
	// overridable per command, and theme.Resolve checks both before it looks at
	// the terminal at all.
	a.out = a.resolve(a.Terminal || force)
	a.err = a.resolve(a.ErrTerminal || force)

	if len(args) == 0 {
		return fault.Usage{Reason: "no command given"}
	}
	command, raw := args[0], args[1:]

	switch command {
	case "help", "-h", "--help":
		return a.say(usageText(a.out))
	}

	// `as` takes a command line of its own, and that command line may contain
	// anything. Splitting it out before flag parsing is what keeps `orcprobe as
	// bob -- cq serve --addr 0.0.0.0` from being read as orcprobe's own flags.
	if command == "as" {
		return a.runAs(raw)
	}

	rest, f, err := parseFlags(raw)
	if err != nil {
		return err
	}
	if f.width > 0 {
		a.Width = f.width
	}

	switch command {
	case "create":
		return a.create(rest, f)
	case "list", "ls":
		return a.list(rest, f)
	case "use":
		return a.use(rest, f)
	case "shell":
		return a.shell(rest, f)
	case "world":
		return a.world(rest, f)
	case "mail":
		return a.mail(rest, f)
	case "tasks":
		return a.tasks(rest, f)
	case "journal":
		return a.journal(rest, f)
	case "timeline":
		return a.timeline(rest, f)
	case "save":
		return a.save(rest, f)
	case "restore":
		return a.restore(rest, f)
	case "diff":
		return a.diff(rest, f)
	case "doctor":
		return a.doctor(rest, f)
	case "manifest":
		return a.manifest(rest, f)
	case "destroy":
		return a.destroy(rest, f)
	default:
		return unknown(command)
	}
}

// store opens the probe store.
func (a App) store() (*probe.Store, error) {
	root := a.Root
	if root == "" {
		home := a.Home
		if home == "" {
			if h, err := os.UserHomeDir(); err == nil {
				home = h
			}
		}
		var err error
		if root, err = probe.DefaultRoot(a.Env, home); err != nil {
			return nil, err
		}
	}
	return probe.Open(root, a.Clock)
}

// resolve picks the palette for one stream.
//
// Each stream is asked about separately, because they are not the same stream.
// `orcprobe shell > log` writes its banner to a terminal while stdout is a
// file; `orcprobe world 2> log` is the reverse. Deciding both from stdout's
// terminal-ness would either drop the colour where a person is reading or
// write escape sequences into a file where nobody is.
//
// A misspelled ORC_THEME leaves this plain rather than failing: it runs before
// any command does, and checkTheme reports the bad setting as a usage error. A
// tool that refuses to draw is right; one that refuses to *say why* is not.
func (a App) resolve(terminal bool) style.Palette {
	cfg, err := theme.Resolve(theme.Look(a.Env), a.Colour && terminal)
	if err != nil {
		return style.Plain()
	}
	return style.New(cfg.Palette)
}

// checkTheme reports a colour setting that cannot be honoured.
//
// A setting that silently does nothing is one the operator concludes is broken,
// so a misspelled ORC_THEME is a usage error rather than a quiet fallback —
// even on the paths where the answer would not have changed what was drawn.
func (a App) checkTheme() error {
	if _, err := theme.Resolve(theme.Look(a.Env), false); err != nil {
		return fault.Usage{Reason: err.Error()}
	}
	return nil
}

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
	_, _ = fmt.Fprintf(a.Stderr, "orcprobe: %s\n", fmt.Sprintf(format, args...))
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

// bytes renders a size the way a person reads one.
func bytesText(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f K", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1f M", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%.1f G", float64(n)/(1<<30))
	}
}
