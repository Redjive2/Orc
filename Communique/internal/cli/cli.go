// Package cli is cq's command line.
//
// Each command is a small function over an App carrying the streams it writes
// to, so the whole thing is testable without a terminal. The mapping from
// error to exit code lives in one place — fault.Exit — so a new failure mode
// cannot exit zero by omission.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"orc/cq/internal/agent"
	"orc/cq/internal/auth"
	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
	"orc/cq/internal/server"
	"orc/cq/internal/source"
	"orc/cq/internal/store"
	"orc/cq/internal/style"
	"orc/cq/internal/upgrade"
	"orc/theme"
)

// App carries the streams a command reads from and writes to, and how each one
// is styled. The zero palettes are plain, so a caller that says nothing about
// colour gets none — which is what a pipe, a hook, and a test all want.
type App struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Env    func(string) (string, bool)
	Out    style.Palette
	Err    style.Palette
	// Listen is how `serve` opens its port; tests replace it.
	Listen func(addr string, h http.Handler) error
}

// Main runs a command line and returns the process exit code. It recovers from
// a panic so that even a defect produces a diagnosed exit rather than a crash.
func Main(app App, args []string) (code int) {
	defer func() {
		if r := recover(); r != nil {
			app.complain(fault.Internal{Where: "cli.Main", Detail: fmt.Sprintf("panic: %v", r)})
			code = fault.ExitInternal
		}
	}()

	if app.Stdout == nil || app.Stderr == nil {
		return fault.ExitInternal
	}
	if app.Env == nil {
		app.Env = os.LookupEnv
	}

	err := app.dispatch(args)
	if err == nil {
		return fault.ExitOK
	}
	// A supervised child's exit status is the server's outcome, not a failure of
	// the supervisor, so it is passed through rather than re-classified.
	var status exitStatus
	if errors.As(err, &status) {
		return status.Status()
	}
	code = fault.Exit(err)
	app.complain(err)
	// The overview no longer follows every usage error. It is fifty lines, most of
	// it the first-run steps, and a refusal that has already said what was wrong
	// reads better without them; the refusals that need a pointer carry their own.
	//
	// `cq` on its own is the exception, and the only one: nothing was named, so
	// there is no refusal to read and the useful answer is what the commands are.
	if code == fault.ExitUsage && len(args) == 0 {
		_, _ = fmt.Fprintln(app.Stderr, "\n"+Brief(app.Err))
	}
	return code
}

// complain reports a failure on stderr. If stderr itself is broken there is
// nowhere to say so, and the exit code carries the outcome on its own.
func (a App) complain(err error) {
	_, _ = fmt.Fprintf(a.Stderr, "%s %v\n", a.Err.Paint("cq:", style.Alarm), err)
}

func (a App) dispatch(args []string) error {
	if len(args) == 0 {
		return fault.Usage{Reason: "no command given"}
	}
	// `cq sync --help` is what a hand reaches for; it means the same as
	// `cq help sync`, so it is routed there rather than left to the flag
	// package to refuse.
	if len(args) > 1 && isHelpFlag(args[len(args)-1]) {
		return a.help(args[:len(args)-1])
	}

	switch args[0] {
	case "serve":
		return a.serve(args[1:])
	case "sync":
		return a.sync(args[1:])
	case "status":
		return a.status(args[1:])
	case "queue":
		return a.queue(args[1:])
	case "admin":
		return a.admin(args[1:])
	case "upgrade":
		return a.upgrade(args[1:])
	case "help", "-h", "--help":
		return a.help(args[1:])
	default:
		return unknown(args[0])
	}
}

func (a App) say(format string, args ...any) error {
	_, err := fmt.Fprintf(a.Stdout, format+"\n", args...)
	return err
}

// tell writes commentary to standard error, so that standard output carries
// only the thing the command was run to produce.
//
// The write is unchecked because there is nowhere left to report it: if stderr
// is broken, the error message about stderr being broken goes to stderr.
func (a App) tell(format string, args ...any) {
	_, _ = fmt.Fprintf(a.Stderr, format+"\n", args...)
}

// help prints the overview, or the detail for one command.
func (a App) help(args []string) error {
	if len(args) == 0 {
		return a.say("%s", Overview(a.Out))
	}
	name := strings.Join(args, " ")
	detail, ok := Detail(a.Out, name)
	if !ok {
		return fault.Usage{Reason: fmt.Sprintf(
			"no command called %q; try one of %s", name, strings.Join(Names(), ", "))}
	}
	return a.say("%s", detail)
}

func isHelpFlag(arg string) bool {
	return arg == "--help" || arg == "-h" || arg == "help"
}

// ink is shorthand for painting one piece of output.
func (a App) ink(text string, i style.Ink) string { return a.Out.Paint(text, i) }

// look reads a setting from the environment.
func (a App) look(key, fallback string) string {
	if v, ok := a.Env(key); ok && v != "" {
		return v
	}
	return fallback
}

// flavour resolves the shared colour scheme.
func (a App) flavour() (theme.Flavour, error) {
	name := a.look("ORC_THEME", "")
	if name == "" {
		return theme.Default, nil
	}
	f, err := theme.ParseFlavour(name)
	if err != nil {
		return theme.Default, fault.Usage{Reason: fmt.Sprintf("ORC_THEME: %v", err)}
	}
	return f, nil
}

func (a App) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(a.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// --- serve ---------------------------------------------------------------

func (a App) serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addr := fs.String("addr", ":8080", "address to listen on")
	stateDir := fs.String("state", a.look("CQ_STATE", defaultStateDir()), "state directory")
	certFile := fs.String("tls-cert", "", "TLS certificate")
	keyFile := fs.String("tls-key", "", "TLS key")
	noAdmin := fs.Bool("no-admin", false, "do not serve the admin panel")
	metaOnly := fs.Bool("admin-metadata-only", false, "withhold other users' bodies")
	supervise := fs.Bool("supervise", true, "run under a supervisor so the server can restart itself")
	source := fs.String("source", a.look("CQ_SOURCE", ""), "checkout to build from on upgrade")
	binDir := fs.String("bin", a.look("CQ_BIN", ""), "where upgrade installs binaries")
	if err := parse(fs, args); err != nil {
		return err
	}
	_ = metaOnly // the agent decides what to send; this is recorded for symmetry

	// Become the supervisor, unless this process already is one's child or the
	// caller asked for a plain process. A supervisor that forked a supervisor
	// would go on doing it, which is the classic way this design goes wrong; the
	// marker in the environment is what stops it.
	//
	// `a.Listen != nil` is a test serving in-process. There is no exec there and
	// nothing to restart, so supervising would fork the test binary.
	if *supervise && !a.supervised() && a.Listen == nil {
		return a.supervise(append([]string{"serve"}, args...))
	}

	state, err := store.Open(*stateDir)
	if err != nil {
		return err
	}
	creds, err := auth.Open(*stateDir)
	if err != nil {
		return err
	}
	flavour, err := a.flavour()
	if err != nil {
		return err
	}

	// restart is what the upgrade endpoint calls when the new binaries are on
	// disk. Nil when nothing is supervising this process, and the endpoint then
	// says so rather than exiting into nothing.
	restart := make(chan struct{}, 1)
	var askRestart func()
	if a.supervised() {
		askRestart = func() {
			select {
			case restart <- struct{}{}:
			default: // already asked; one restart is enough
			}
		}
	}

	srv, err := server.New(server.Options{
		State: state, Creds: creds, Admin: !*noAdmin,
		Logger: a.logger(), Flavour: flavour,
		Secure:  *certFile != "",
		Upgrade: upgrade.Options{Source: *source, Target: *binDir},
		Restart: askRestart,
	})
	if err != nil {
		return err
	}

	if err := a.say("%s serving %s on %s %s",
		a.ink("cq", style.Tool),
		a.ink(*stateDir, style.Setting),
		a.ink(*addr, style.Value),
		a.ink("("+flavour.String()+")", style.Quiet)); err != nil {
		return err
	}
	if !*noAdmin {
		if err := a.say("%s the admin panel is served; %s turns it off",
			a.ink("   ", style.None), a.ink("--no-admin", style.Flag)); err != nil {
			return err
		}
	}
	if *certFile == "" && !strings.HasPrefix(*addr, "127.0.0.1") && !strings.HasPrefix(*addr, "localhost") {
		if err := a.say("%s no TLS on a public address — put a proxy in front, or pass %s",
			a.ink("   warning", style.Warn), a.ink("--tls-cert", style.Flag)); err != nil {
			return err
		}
	}

	if a.Listen != nil {
		return a.Listen(*addr, srv)
	}
	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           srv,
		ReadHeaderTimeout: server.ReadHeaderTimeout,
	}

	serving := make(chan error, 1)
	go func() {
		if *certFile != "" {
			serving <- httpServer.ListenAndServeTLS(*certFile, *keyFile)
			return
		}
		serving <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-serving:
		return fault.IO{Op: "serve", Subject: *addr, Err: err}

	case <-restart:
		// Drained, not dropped. A request in flight when the upgrade landed is a
		// request somebody is waiting on, and finishing it costs a few seconds
		// against a restart that is already going to take longer than that.
		//
		// Nothing else needs saving: the queue and the snapshots are on disk and
		// fsynced before each reply, so what comes back reads exactly the state
		// that went down. That is the property that makes restarting mid-flight
		// safe — not this shutdown, which only spares the requests in the air.
		ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			// A connection that would not close in time is not worth staying up
			// for. Said out loud, then on with the restart.
			a.tell("cq: %d s was not enough to drain: %v", int(shutdownGrace.Seconds()), err)
		}
		// The supervisor reads this and starts the new binary.
		return exitStatus(ExitRestart)
	}
}

// shutdownGrace bounds draining. Long enough for a page load and a sync round to
// finish, short enough that a wedged connection cannot hold a restart open.
const shutdownGrace = 15 * time.Second

// --- sync ----------------------------------------------------------------

// sourceFor builds the adapter for one mirrored account.
//
// It hands over cq's own environment lookup, because the adapter has to check
// that the ambient Orc identity is the account being mirrored — and it should
// read the same environment the rest of cq does, not a second one.
func (a App) sourceFor(m mirror) *source.CLI {
	src := source.NewCLI(m.User)
	src.Look = a.Env
	src.Key = m.Key
	// Where this machine builds from, for a queued upgrade. Its zero value has no
	// source, and the action then refuses with that reason — which is right for a
	// machine that installs binaries rather than building them.
	src.Upgrade = upgrade.Options{
		Source: a.look("CQ_SOURCE", ""),
		Target: a.look("CQ_BIN", ""),
	}
	src.Warn = func(format string, args ...any) { a.logger().Warn(fmt.Sprintf(format, args...)) }
	return src
}

// source builds the adapter for the configured account, for the commands that
// need one built but do not read a mailbox with it. It does not run the ladder:
// `cq status` must answer on a machine where nothing is configured yet, since
// that is the machine whose operator most needs to read it.
func (a App) source() *source.CLI {
	return a.sourceFor(mirror{User: a.look("CQ_USER", ""), Key: a.look("CQ_KEY", "")})
}

func (a App) sync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	serverURL := fs.String("server", a.look("CQ_SERVER", ""), "server to sync against")
	machine := fs.String("machine", a.look("CQ_MACHINE", hostname()), "this machine's name")
	user := fs.String("user", a.look("CQ_USER", ""), "the mailbox to mirror")
	home := fs.String("home", a.look("CQ_HOME", defaultAgentDir()), "agent state directory")
	watch := fs.Duration("watch", 0, "repeat at this interval")
	nudge := fs.Bool("nudge", false, "coalescing form, called after every mailman action")
	dryRun := fs.Bool("dry-run", false, "collect and print, but send nothing")
	admin := fs.Bool("admin", true, "include the whole-Mailman view")
	bodies := fs.Bool("admin-bodies", true, "include other users' message bodies")
	library := fs.String("library", a.look("CQ_LIBRARY", ""), "repository to mirror for reading")
	if err := parse(fs, args); err != nil {
		return err
	}

	// The plain settings first. Working out whose mailbox this is means running
	// Orc, and a machine that has not been pointed at a server should hear about
	// the server rather than about a tool it was not asked to think about.
	if !*dryRun {
		if err := agent.CheckSettings(*serverURL, a.look("CQ_TOKEN", ""),
			protocol.MachineID(*machine)); err != nil {
			return err
		}
	}

	// Resolved once, before anything is collected, so a machine that cannot say
	// whose mailbox this is fails with that — and not with three Mailman commands
	// run as the wrong account first.
	who, err := a.mirrored(context.Background(), *user)
	if err != nil {
		return err
	}
	src := a.sourceFor(who)
	// The directory the library is collected from is the only one its edits may
	// write. One setting rather than two that could disagree.
	src.LibraryRoot = *library

	if *dryRun {
		snap, err := src.Snapshot(context.Background(), source.Options{
			Machine: protocol.MachineID(*machine), Admin: *admin, AdminBodies: *bodies,
			Library: *library,
		})
		if err != nil {
			return err
		}
		if err := a.say("%s %d in the inbox, %d archived, %d tasks — %s",
			a.ink(string(snap.Machine), style.Value),
			len(snap.Inbox), len(snap.Archive), len(snap.Tasks),
			a.ink("nothing was sent", style.Quiet)); err != nil {
			return err
		}
		// The library is what a dry run is most useful for: it is the one part
		// of a snapshot whose size depends on a directory the operator chose,
		// and seeing that before the first real sync is the point.
		if snap.Library != nil {
			body, err := json.Marshal(snap.Library)
			if err != nil {
				return fault.Internal{Where: "cli.sync", Detail: err.Error()}
			}
			var docs, annotated, skipped int
			for _, f := range snap.Library.Files {
				if len(f.Sections) > 0 {
					docs++
				}
				if len(f.Annotations) > 0 {
					annotated++
				}
				if f.Skipped != "" {
					skipped++
				}
			}
			if err := a.say("  %s", a.ink(fmt.Sprintf(
				"library: %d files (%d documents, %d annotated, %d not carried) — %dK on the wire",
				len(snap.Library.Files), docs, annotated, skipped, len(body)/1024), style.Quiet)); err != nil {
				return err
			}
			if snap.Library.Truncated != "" {
				return a.say("  %s", a.ink(snap.Library.Truncated, style.Warn))
			}
		}
		return nil
	}

	ag, err := agent.New(agent.Options{
		Source: src, Server: *serverURL, Token: a.look("CQ_TOKEN", ""),
		Machine: protocol.MachineID(*machine), State: *home,
		Admin: *admin, AdminBodies: *bodies, Library: *library, Logger: a.logger(),
	})
	if err != nil {
		return err
	}

	if *nudge {
		report, ran, err := ag.Nudge(context.Background())
		if err != nil {
			return err
		}
		if !ran {
			return nil // another sync is in flight and will pick this up
		}
		return a.report(report)
	}

	if *watch <= 0 {
		report, err := ag.Sync(context.Background())
		if err != nil {
			return err
		}
		return a.report(report)
	}

	// The loop is here rather than inside the agent, so the process stays
	// restartable and one failed round does not end the watch.
	ticker := time.NewTicker(*watch)
	defer ticker.Stop()
	for {
		report, err := ag.Sync(context.Background())
		if err != nil {
			// A failed round does not end the watch: the next one may find the
			// server back, and stopping would mean the mirror silently dies
			// the first time a network hiccups.
			a.complain(err)
		} else if err := a.report(report); err != nil {
			return err
		}
		<-ticker.C
	}
}

// report renders one sync's outcome, colouring only what carries state.
func (a App) report(r agent.Report) error {
	parts := []string{
		a.ink(string(r.Machine), style.Value),
		fmt.Sprintf("%s up", a.ink(fmt.Sprint(r.Sent), style.Good)),
		fmt.Sprintf("%s down", a.ink(fmt.Sprint(r.Received), style.Good)),
	}
	if r.Applied > 0 {
		parts = append(parts, a.ink(fmt.Sprintf("%d applied", r.Applied), style.Good))
	}
	if r.Skipped > 0 {
		parts = append(parts, a.ink(fmt.Sprintf("%d already done", r.Skipped), style.Quiet))
	}
	if r.Failed > 0 {
		parts = append(parts, a.ink(fmt.Sprintf("%d failed", r.Failed), style.Alarm))
	}
	if r.Truncated {
		parts = append(parts, a.ink("journal tail was incomplete", style.Warn))
	}
	return a.say("%s", strings.Join(parts, a.ink(" · ", style.Frame)))
}

// --- status --------------------------------------------------------------

func (a App) status(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	home := fs.String("home", a.look("CQ_HOME", defaultAgentDir()), "agent state directory")
	if err := parse(fs, args); err != nil {
		return err
	}

	// What is set, before what has happened: a mirror that has never synced is
	// almost always a mirror that was never told where to sync to, and saying
	// which setting is missing answers that in one line rather than none.
	if err := a.settingsReport(); err != nil {
		return err
	}

	ag, err := agent.New(agent.Options{
		Source: a.source(),
		Server: "http://unused.invalid", Token: "unused",
		Machine: protocol.MachineID(a.look("CQ_MACHINE", hostname())),
		State:   *home, Logger: a.logger(),
	})
	if err != nil {
		return err
	}
	cursor, state, err := ag.Status()
	if err != nil {
		return err
	}

	if err := a.say(""); err != nil {
		return err
	}
	switch {
	case cursor.LastSync.IsZero():
		if err := a.field("last sync", a.ink("never", style.Warn)); err != nil {
			return err
		}
	default:
		age := time.Since(cursor.LastSync).Round(time.Second)
		ink := style.Good
		if age > 10*time.Minute {
			ink = style.Alarm
		} else if age > 2*time.Minute {
			ink = style.Warn
		}
		if err := a.field("last sync", a.ink(fmt.Sprintf("%s ago", age), ink)+
			a.ink("  "+cursor.LastSync.Format(time.RFC3339), style.Quiet)); err != nil {
			return err
		}
	}

	if cursor.LastError != "" {
		if err := a.field("last error", a.ink(cursor.LastError, style.Alarm)); err != nil {
			return err
		}
	}

	waiting := len(state.Unreported())
	ink := style.Quiet
	if waiting > 0 {
		ink = style.Warn
	}
	return a.field("waiting", a.ink(fmt.Sprintf("%d result(s) the server has not taken", waiting), ink))
}

// settingsReport lists the settings this command depends on, marking each set
// or missing. It is the difference between "it does not work" and "$CQ_TOKEN
// is not set".
func (a App) settingsReport() error {
	type row struct {
		name, value string
		needed      bool
	}
	rows := []row{
		{"CQ_SERVER", a.look("CQ_SERVER", ""), true},
		{"CQ_TOKEN", redact(a.look("CQ_TOKEN", "")), true},
		{"CQ_MACHINE", a.look("CQ_MACHINE", hostname()), false},
		{"CQ_USER", a.look("CQ_USER", ""), false},
		{"CQ_KEY", redact(a.look("CQ_KEY", "")), false},
		{"CQ_HOME", a.look("CQ_HOME", defaultAgentDir()), false},
		// Listed even though it is optional, because "is a repository being
		// mirrored at all" is the first question when the docs and code tabs are
		// empty, and this is the machine where the answer lives.
		{"CQ_LIBRARY", a.look("CQ_LIBRARY", ""), false},
	}
	for _, r := range rows {
		value := a.ink(r.value, style.Setting)
		if r.value == "" {
			mark := style.Quiet
			text := "not set"
			if r.needed {
				mark, text = style.Warn, "not set — sync needs this"
			}
			value = a.ink(text, mark)
		}
		if err := a.field(r.name, value); err != nil {
			return err
		}
	}
	return a.mirrorReport()
}

// mirrorReport says whose mailbox this machine would mirror, and how cq worked it
// out.
//
// It runs the same ladder `sync` does, because a report of what cq *would* decide
// is worth having and a report of one setting is not: `$CQ_USER` being unset stopped
// meaning "sync will fail" the moment Orc could answer for it.
func (a App) mirrorReport() error {
	who, err := a.mirrored(context.Background(), a.look("CQ_USER", ""))
	if err != nil {
		// The full diagnosis belongs to the command that was refused. Here it is a
		// line in a report, and the report has other lines to print.
		return a.field("mirroring", a.ink("unresolved — run `cq sync` to see why", style.Warn))
	}
	return a.field("mirroring", a.ink(who.User, style.Setting)+
		a.ink("  from "+who.How, style.Quiet))
}

// field prints one aligned label and value. Padding is computed from the label
// itself, never from the painted text, so a coloured column still lines up.
func (a App) field(label, value string) error {
	return a.say("%s%s%s", a.ink(label, style.Heading), pad(label, 12), value)
}

// redact shows enough of a secret to recognise it and not enough to use it.
func redact(secret string) string {
	if secret == "" {
		return ""
	}
	if id, _, ok := strings.Cut(secret, "."); ok && len(id) >= 8 {
		return id + ".…"
	}
	if len(secret) <= 8 {
		return "…"
	}
	return secret[:4] + "…"
}

// --- admin ---------------------------------------------------------------

func (a App) admin(args []string) error {
	if len(args) == 0 {
		return fault.Usage{Reason: "admin needs a subcommand: operator or token"}
	}
	stateDir := a.look("CQ_STATE", defaultStateDir())
	creds, err := auth.Open(stateDir)
	if err != nil {
		return err
	}

	switch args[0] {
	case "operator":
		password := a.look("CQ_PASSWORD", "")
		if password == "" {
			read, err := a.readSecret("password: ")
			if err != nil {
				return err
			}
			password = read
		}
		if err := creds.SetPassword(password, time.Now().UTC()); err != nil {
			return err
		}
		if err := a.say("%s the operator password is set", a.ink("✓", style.Good)); err != nil {
			return err
		}
		return a.say("%s", a.ink("  next: cq admin token <machine>, then cq serve", style.Quiet))

	case "token":
		label := ""
		if len(args) > 1 {
			label = args[1]
		}
		secret, rec, err := creds.NewToken(label, time.Now().UTC())
		if err != nil {
			return err
		}
		// The token goes to standard output, alone; everything around it goes
		// to standard error. This command exists to hand a secret to another
		// machine, and `CQ_TOKEN=$(cq admin token studio)` is how that is done
		// — a secret the operator has to grep out of prose is a secret they
		// will grep wrongly.
		a.tell("%s token %s minted for %s",
			a.ink("✓", style.Good),
			a.ink(rec.ID, style.Setting),
			a.ink(orDash(label), style.Value))
		a.tell("%s", a.ink("  shown once, stored only as a digest — copy it now", style.Warn))
		a.tell("")
		if err := a.say("%s", secret); err != nil {
			return err
		}
		a.tell("")
		a.tell("%s", a.ink("  on the agent machine:", style.Quiet))
		a.tell("%s", a.ink("    export CQ_TOKEN=$(cq admin token "+orDash(label)+")", style.Quiet))
		return nil

	default:
		return fault.Usage{Reason: fmt.Sprintf("unknown admin subcommand %q", args[0])}
	}
}

// readSecret reads one line from standard input.
//
// One *line*, ending at the newline — not everything up to end of input. At a
// terminal there is no end of input until the operator presses Ctrl-D, so
// reading to EOF means the prompt appears to hang after they press Enter,
// which is exactly what it looked like.
//
// Echo is not disabled: doing that portably means either a dependency or raw
// terminal handling, so cq says so and lets the password be piped in instead.
func (a App) readSecret(prompt string) (string, error) {
	if a.Stdin == nil {
		return "", fault.Usage{Reason: "no standard input; set $CQ_PASSWORD instead"}
	}
	if _, err := fmt.Fprint(a.Stderr, prompt); err != nil {
		return "", err
	}

	reader := bufio.NewReader(io.LimitReader(a.Stdin, auth.MaxPasswordBytes+1))
	line, err := reader.ReadString('\n')
	// A final line with no newline is what a pipe gives, and is not an error.
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fault.IO{Op: "read", Subject: "standard input", Err: err}
	}
	if _, err := fmt.Fprintln(a.Stderr); err != nil {
		return "", err
	}
	if strings.TrimRight(line, "\r\n") == "" {
		return "", fault.Usage{Reason: "no password given"}
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// --- helpers -------------------------------------------------------------

func parse(fs *flag.FlagSet, args []string) error {
	if err := parseWithArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fault.Usage{Reason: fmt.Sprintf("unexpected argument %q", fs.Arg(0))}
	}
	return nil
}

// parseWithArgs is parse for a command that takes positional arguments as well
// as flags — `queue retry <id>`. It is separate so that every *other* command
// keeps refusing a stray argument rather than ignoring it.
func parseWithArgs(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return fault.Usage{Reason: err.Error()}
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "this machine"
	}
	return s
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return "agent"
	}
	name = strings.ToLower(strings.Split(name, ".")[0])
	if protocol.MachineID(name).Validate() != nil {
		return "agent"
	}
	return name
}

func defaultStateDir() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "cq-server")
	}
	return ".cq-server"
}

func defaultAgentDir() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "cq")
	}
	return ".cq"
}
