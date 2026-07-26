// Package cli implements Orc's commands.
//
// Each command is a small method on an App, which carries the streams to read
// from and write to. Commands return errors; the mapping from error to exit code
// lives in one place — orc/common/fault — so a new failure mode cannot
// accidentally exit zero, and so `orc`, `muff`, `anno`, and `mailman` mean the
// same thing by the same number.
//
// Two things are different here from every other tool's CLI, and both follow from
// Orc being the fleet's authority rather than one of its users:
//
//   - **Every command authenticates against the store, not just against the
//     environment.** Macmuffin resolves $ORC_USER and believes it. Orc is the
//     thing that issued the credential, so it verifies the key. That is what
//     makes `orc check-control` worth anything to the tools that ask it.
//   - **Every command derives the whole fleet first.** Nothing effective is
//     stored (Plan.md §2.4), so there is no path on which a command acts on an
//     authority a `move` has already invalidated.
package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/identity"
	"orc/common/user"
	"orc/orc/internal/authz"
	"orc/orc/internal/model"
	"orc/orc/internal/provision"
	"orc/orc/internal/render"
	"orc/orc/internal/session"
	"orc/orc/internal/store"
	"orc/orc/internal/style"
	"orc/theme"
)

// App carries everything a command needs from the outside world. Every field is
// injected so the whole CLI is testable without a terminal, a home directory, a
// real clock, or a Mailman installation.
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
	// Terminal reports whether stdout is a real terminal. It is injected because
	// a test writes to a buffer, which never is one.
	Terminal bool
	// ErrTerminal is the same question for stderr. They are asked separately
	// because `orc status > fleet.txt` still has a terminal to be diagnosed on.
	ErrTerminal bool
	// Width is the terminal width to lay screens out for. Zero takes the
	// renderer's default.
	Width int
	// User is the operating-system user, which is what `orc bootstrap` names the
	// operator after when --as does not say otherwise.
	User string
	// Provision runs another tool's provisioning command. Nil uses the real
	// `mailman` binary; a test passes a recorder so nothing is ever executed.
	Provision provision.Run
	// Populate and Depopulate start and stop sessions. Nil uses the real
	// supervisor; a test passes recorders, so a CLI test can exercise the worklist
	// and the budget without spawning a process per employment. The supervisor
	// itself is tested in internal/session, against a real pty.
	Populate   func(*store.Store, user.Name, string, model.Model, model.Effort, bool) error
	Depopulate func(*store.Store, user.Name) error
	// Entropy mints identity ids. Nil uses crypto/rand.
	Entropy io.Reader

	// out and err are the resolved palettes. They are filled in before anything
	// else runs, so that a failure on the way to the store — a missing
	// credential, a bad argument — is painted like everything else rather than
	// being the one plain thing orc prints.
	out style.Palette
	err style.Palette
}

// Colour flags. They are global rather than per-command and are taken off the
// line before dispatch, so `orc --no-color status` and `orc status --no-color`
// both work and no command has to know they exist.
const (
	FlagNoColour = "--no-color"
	FlagColour   = "--color"
)

// Main runs a command line and returns the process exit code.
//
// It is the only function that turns an error into a status, and it recovers from
// panics so that even a defect produces a diagnosed exit rather than a crash with
// an identity half-written.
func Main(app App, args []string) (code int) {
	defer func() {
		if r := recover(); r != nil {
			_, _ = fmt.Fprintf(app.Stderr, "%s %v\n", app.err.Alarm("orc:"), fault.Internal{
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

	code = fault.Code(err)
	_, _ = fmt.Fprintf(app.Stderr, "%s %v\n", app.err.Alarm("orc:"), err)
	// The full screen no longer follows a usage error. It is ninety lines, and a
	// refusal that has already said what was wrong is easier to read without them;
	// the errors themselves carry the pointer to `orc help` where one helps.
	//
	// `orc` on its own is the exception, and the only one: nothing was named, so
	// there is no refusal to read and the useful answer is what the verbs are.
	if code == fault.CodeUsage && len(args) == 0 {
		_, _ = fmt.Fprintln(app.Stderr, "\n"+brief(app.err))
	}
	return code
}

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
// before any command does, and the command itself still reports the bad setting as
// a usage error. Refusing to draw is the right answer; refusing to *say* why would
// leave the caller with a silent tool.
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
	case "bootstrap":
		// The one command that runs without a fleet, because it is what makes one.
		return a.bootstrap(rest)
	}

	switch command {
	case "new":
		return a.newThing(rest)
	case "assign":
		return a.assign(rest)
	case "remove":
		return a.remove(rest)
	case "grant":
		return a.grant(rest)
	case "revoke":
		return a.revoke(rest)
	case "move":
		return a.move(rest)
	case "status":
		return a.status(rest)
	case "list":
		return a.list(rest)
	case "budget":
		return a.budget(rest)
	case "introspect":
		return a.introspect(rest)
	case "check-control":
		return a.checkControl(rest)
	case "env":
		return a.env(rest)
	case "verify":
		return a.verify(rest)
	case "owner":
		return a.owner(rest)

	case "employ":
		return a.employ(rest)
	case "fire":
		return a.fire(rest)
	case "tend":
		return a.tend(rest)
	case "attach":
		return a.attach(rest)
	case "poke":
		return a.poke(rest)
	case "refresh":
		return a.refresh(rest)

	// Documented in Docs/Orc/Reference.md, deliberately not built yet. It says what
	// it is waiting on rather than failing as an unknown command, because a verb in
	// the specification that answers "unknown command" reads as a broken build
	// rather than as an unfinished one. See doctor.go.
	case "doctor":
		return a.doctor(rest)

	default:
		return unknown(command)
	}
}

// caller is an authenticated command's context: who is running it, the store
// they are running it against, and the fleet as derived a moment ago.
type caller struct {
	app App
	// fromKeyring records that the credential was not presented but found: the
	// owner fallback below. `orc owner` shows it, because "why does orc believe I
	// am the operator" should have a visible answer.
	fromKeyring bool
	store       *store.Store
	who         user.Name
	fleet       authz.Fleet
	paint       style.Palette
}

// begin opens the store, authenticates, and derives the fleet.
//
// The order matters and is the reverse of what looks natural. Authentication
// happens before any argument is examined, so an agent with no identity is told
// that rather than told its arguments are malformed. The derivation happens
// before any command runs, so a structurally broken fleet — no operator, a
// missing boss, a cycle — refuses every command with the same true message
// instead of letting some of them succeed against a store nobody can reason
// about.
func (a App) begin() (caller, error) {
	root, err := a.root()
	if err != nil {
		return caller{}, err
	}
	s, err := store.Open(root, a.Clock)
	if err != nil {
		return caller{}, err
	}

	who, key, fromKeyring, err := a.credential(s)
	if err != nil {
		return caller{}, err
	}

	// Orc issued this credential, so Orc verifies it — including the one it just
	// read out of its own keyring. Verifying a credential this process supplied to
	// itself sounds circular and is not: it catches a keyring whose digest and
	// plaintext have drifted apart, which is a damaged store rather than a caller
	// mistake, and one that would otherwise only surface as an agent that cannot
	// authenticate.
	if err := s.Authenticate(who, key); err != nil {
		return caller{}, err
	}

	fleet, err := s.Fleet()
	if err != nil {
		return caller{}, err
	}
	if !fleet.Has(who) {
		// A credential that verifies against an identity the fleet does not
		// contain means the store changed under the process, or a directory was
		// copied in. Either way the caller is not somebody this fleet knows.
		return caller{}, fault.Auth{Reason: "authentication failed",
			Detail: who.String() + " has a credential but is not in the fleet"}
	}

	return caller{app: a, store: s, who: who, fleet: fleet, paint: a.out, fromKeyring: fromKeyring}, nil
}

// credential resolves who is running this command.
//
// The environment first, exactly as Common/identity defines it — that is the
// contract every tool in this tree shares, and an agent's session always has it.
//
// The fallback is the owner's convenience, and it is narrow on purpose:
//
//   - it applies only when **neither** $ORC_USER nor $ORC_KEY is set. A half-set
//     environment stays an error, because it is a mistake rather than an absence,
//     and a typo in one of the two must never silently promote somebody to
//     operator.
//   - it yields the **operator** and nobody else. An agent presents its
//     credential; there is no path here that looks up another identity's key.
//   - it requires the store to be **private to this unix user**. That is the whole
//     argument for doing it at all: the keyring is plaintext at 0600 inside a 0700
//     directory, so a process that can read the directory can already read every
//     key in it, and making the owner export one adds friction rather than
//     security. The moment that stops being true — a group-readable store, another
//     user's fleet — the argument fails and so does this.
func (a App) credential(s *store.Store) (who user.Name, key string, fromKeyring bool, err error) {
	_, hasUser := a.Env(identity.EnvUser)
	_, hasKey := a.Env(identity.EnvKey)

	if hasUser || hasKey {
		cred, err := identity.New(identity.Env(a.Env)).Resolve()
		if err != nil {
			return user.Name{}, "", false, err
		}
		return cred.Name(), cred.Key(), false, nil
	}

	who, key, err = s.OperatorCredential()
	if err != nil {
		return user.Name{}, "", false, err
	}
	return who, key, true, nil
}

// root resolves where the store lives.
func (a App) root() (string, error) {
	if a.Root != "" {
		return a.Root, nil
	}
	home := a.Home
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = h
		}
	}
	return store.DefaultRoot(store.Env(a.Env), home)
}

// mayRunVerb reports whether the caller's role permits an Orc verb.
//
// The rule, which is not in Auth_Perm_Role.md and is this build's reading of it:
// `orc(<verb>)` clauses **narrow** an identity rather than enabling it. An
// identity whose effective permissions contain no orc-kind clause at all is
// governed by the structural rules alone — authority for handing out authority,
// ancestry for acting on subordinates — and one that has any is additionally held
// to them.
//
// The alternative reading, where every verb needs an explicit orc() clause, makes
// a freshly bootstrapped fleet unable to create the permission that would let it
// create anything. A rule that requires bootstrapping itself is not a rule.
func (s caller) mayRunVerb(verb string) error {
	if s.who.String() == s.fleet.Operator().String() {
		return nil
	}
	var gated bool
	for _, c := range s.fleet.Clauses(s.who) {
		if c.Pattern.Kind() == model.KindOrc {
			gated = true
			if c.Pattern.Matches(verb) {
				return nil
			}
		}
	}
	if !gated {
		return nil
	}
	return fault.Denied{Actor: s.who.String(), Action: "run", Target: "orc " + verb,
		Reason: "its role names the orc verbs it may run, and this is not one of them"}
}

// controls refuses a command aimed at somebody who is not the caller's
// subordinate.
//
// The refusal is a *not found* rather than a denial, following Macmuffin's privacy
// rule: saying "you may not" would confirm the identity exists, and the roster of
// a fleet is exactly the kind of thing an agent should not be able to enumerate
// from outside its own branch.
func (s caller) controls(target user.Name, action string) error {
	if s.fleet.Controls(s.who, target) {
		return nil
	}
	if s.who.String() == target.String() {
		return fault.Denied{Actor: s.who.String(), Action: action, Target: "itself",
			Reason: "an identity is not its own subordinate"}
	}
	return fault.NotFound{Target: target.String()}
}

// atLeast refuses handing out an authority the caller does not have.
func (s caller) atLeast(level model.Authority, what string) error {
	mine, _ := s.fleet.Authority(s.who)
	if mine.AtLeast(level) {
		return nil
	}
	return fault.Denied{Actor: s.who.String(), Action: "set", Target: what,
		Reason: fmt.Sprintf("that is authority %s, and %s has %s", level, s.who, mine)}
}

// Output helpers, matching the rest of the tree.

// width is the width to lay screens out for.
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

// note reports something the caller should know that is not a failure. It goes to
// stderr so stdout stays pipeable.
func (a App) note(format string, args ...any) {
	// Nothing can be done about a broken stderr, and failing a command that
	// otherwise succeeded because its footnote could not be printed would be
	// worse than the footnote being lost.
	_, _ = fmt.Fprintf(a.Stderr, "%s %s\n", a.err.Alarm("orc:"), fmt.Sprintf(format, args...))
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
// Everything after a bare "--" is positional whatever it looks like, which is what
// lets a description legitimately begin with a dash. An unknown option is a usage
// error rather than a positional argument: `orc remove identity atlas --yse`
// silently treating the typo as a name is exactly the mistake a fleet tool must
// not make.
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

// populate and depopulate resolve the injected hooks, falling back to the real
// supervisor. They exist so that every caller reads the same way whether or not a
// test replaced them.
func (a App) populate(s *store.Store, name user.Name, id string, m model.Model, e model.Effort, resume bool) error {
	if a.Populate != nil {
		return a.Populate(s, name, id, m, e, resume)
	}
	return session.Populate(s, name, id, m, e, resume)
}

func (a App) depopulate(s *store.Store, name user.Name) error {
	if a.Depopulate != nil {
		return a.Depopulate(s, name)
	}
	return session.Depopulate(s, name)
}
