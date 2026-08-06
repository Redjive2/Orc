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

	"orc/common/watch"
	"orc/cq/internal/agent"
	"orc/cq/internal/auth"
	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
	"orc/cq/internal/server"
	stored "orc/cq/internal/settings"
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
	case "pace":
		return a.pace(args[1:])
	case "status":
		return a.status(args[1:])
	case "queue":
		return a.queue(args[1:])
	case "workspace":
		return a.workspace(args[1:])
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
	install := fs.Bool("install-service", false, "write a startup service for this machine and exit")
	force := fs.Bool("force", false, "with --install-service, replace one that is already there")
	if err := parse(fs, args); err != nil {
		return err
	}
	_ = metaOnly // the agent decides what to send; this is recorded for symmetry

	// Writing the service is a use of `serve`'s own flags rather than a command of
	// its own, and deliberately: the unit has to record the exact address, store,
	// and certificates this server runs with, and those are these. `cq serve --addr
	// :443 --state /srv/cq --install-service` writes a unit for precisely the
	// server that command line describes, which is the only way to be sure the
	// service and the thing somebody tested are the same server.
	if *install {
		return a.installService(*addr, *stateDir, *certFile, *keyFile, *noAdmin, *source, *binDir, *force)
	}

	// Become the supervisor, unless this process already is one's child or the
	// caller asked for a plain process. A supervisor that forked a supervisor
	// would go on doing it, which is the classic way this design goes wrong; the
	// marker in the environment is what stops it.
	//
	// `a.Listen != nil` is a test serving in-process. There is no exec there and
	// nothing to restart, so supervising would fork the test binary.
	if *supervise && !a.supervised() && a.Listen == nil {
		// Serving is what was asked for; restarting in place is a convenience on
		// top of it. So a binary this process cannot start a copy of costs the
		// supervisor and nothing else — said out loud, because `cq upgrade` will
		// later have to ask for a restart by hand and this is the reason.
		switch exe, err := restartable(); {
		case err == nil:
			return a.supervise(exe, append([]string{"serve"}, args...))
		default:
			a.tell("%s %v", a.ink("warning", style.Warn), err)
			a.tell("%s serving without a supervisor; %s will ask you to restart by hand",
				a.ink("       ", style.None), a.ink("cq upgrade", style.Flag))
		}
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

	// Said at start, not discovered at upgrade time.
	//
	// The supervisor execs the path this binary is at. A `--bin` pointing anywhere
	// else means an upgrade installs where nothing runs from, restarts into the old
	// build, and reports success — and the moment to hear about that is now, while
	// somebody is reading the startup lines, rather than in a log after a rebuild
	// that appeared to work. The server still starts: the setting may be deliberate
	// on a machine that installs for others.
	a.warnInstallElsewhere(*binDir)

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
	interval := fs.Duration("watch", 0, "repeat at this interval")
	ttl := fs.Duration("for", 0, "stop watching after this long; default is until stopped")
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
	// Where this machine mirrors from, which the website can move.
	typed := explicitlySet(fs, "library")
	libraryRoot, err := libraryFor(typed, *library, *home)
	if err != nil {
		return err
	}

	src := a.sourceFor(who)
	// The directory the library is collected from is the only one its edits may
	// write. One setting rather than two that could disagree.
	src.LibraryRoot = libraryRoot
	// And the home, so an action that moves the root has somewhere to record it.
	src.Home = *home

	if *dryRun {
		snap, err := src.Snapshot(context.Background(), source.Options{
			Machine: protocol.MachineID(*machine), Admin: *admin, AdminBodies: *bodies,
			Library: libraryRoot,
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

	// Set here rather than in sourceFor because only this command knows the agent
	// home and the flags a watch would run with. An upgrade arriving down the queue
	// applies inside this adapter, and this is what it calls afterwards.
	src.EnsureWatch = a.ensureWatch(watchPlan{
		Home: *home, Server: *serverURL, Machine: *machine, User: *user,
		// The library too. watchPlan has carried the field and args() has forwarded
		// it all along; nothing filled it in, so a watcher started after an upgrade
		// mirrored no repository even when the sync that spawned it did.
		Library: libraryRoot,
	})

	ag, err := agent.New(agent.Options{
		Source: src, Server: *serverURL, Token: a.look("CQ_TOKEN", ""),
		Machine: protocol.MachineID(*machine), State: *home,
		Admin: *admin, AdminBodies: *bodies, Library: libraryRoot, Logger: a.logger(),
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

	if *interval <= 0 {
		report, err := ag.Sync(context.Background())
		if err != nil {
			return err
		}
		// A one-shot cannot change its own interval — it is about to exit — but it
		// is often the only thing that ever runs on a machine driven by cron, and
		// it is where the next watcher's starting interval comes from.
		a.rememberPace(*home, report.Pace)
		return a.report(report)
	}
	return a.watchSync(ag, src, *home, *interval, *ttl, typed)
}

// watchSync is the mirror's heartbeat: sync, wait, sync again, until stopped.
//
// The loop is here rather than inside the agent, so the process stays restartable
// and one failed round does not end the watch.
//
// Three things happen between rounds and never during one. The watcher checks
// whether its own binary has been replaced — an upgrade this very loop applied
// will have done exactly that — and restarts into the new build if so. It checks
// whether it has outlived the time it was given. And it re-reads which directory
// it is meant to be mirroring, because that is a thing the website can move and a
// watcher whose settings were fixed at launch could never hear about it.
//
// All three are between rounds because a round that is half-applied when the
// process image — or the directory it is walking — changes underneath it is the
// one outcome worse than running an old build for another five minutes.
//
// The re-read is skipped when `--library` was typed on this command line. A flag
// is an instruction about this run, and a run whose directory changed halfway
// through because of something somebody did in a browser is not what was asked
// for.
func (a App) watchSync(ag *agent.Agent, src *source.CLI, home string,
	every, ttl time.Duration, typed bool) error {
	// Asked for once, at the top, so that what is compared against is the build
	// this watcher started with rather than whatever was on disk a moment ago.
	exe, stamp, err := watch.Own()
	if err != nil {
		return err
	}

	// What the server last asked for, rather than what this command line says. See
	// startingPace: the flag deciding the first round made it the one round that
	// did not follow the rule.
	if next := a.startingPace(home, every); next != every {
		_ = a.say("cq: syncing every %s, as the server last asked", round(next))
		every = next
	}

	started := time.Now()
	stopAt := watch.Until(started, ttl)
	// A watcher that cannot say it is running is still a watcher, so a registry
	// that will not write is complained about and carried past: refusing to sync
	// because a bookkeeping file failed would take the mirror down over the very
	// thing meant to keep it up. The cost is that an upgrade may start a second
	// watcher beside this one, which is noise rather than damage.
	announce := func() func() {
		release, err := watch.Registry{Dir: watchers(home)}.Register(watch.Record{
			Kind: watch.Sync, Exe: exe, Args: selfArgs(),
			Period: watch.Duration(every), Started: started, Expires: stopAt,
		})
		if err != nil {
			a.complain(err)
			return func() {}
		}
		return release
	}

	release := announce()
	defer func() { release() }()

	ticker := time.NewTicker(every)
	defer ticker.Stop()

	// The narrow rounds, for a session somebody has open in a browser.
	//
	// A second ticker rather than a faster first one. The full round mirrors the
	// mail, the tasks, the repository and every agent's session; running *that*
	// every three seconds to keep one transcript current would multiply the cost of
	// everything to make one pane feel live. This one carries a single session and
	// drains the queue, so answering an agent takes seconds while the mirror keeps
	// its own pace.
	//
	// Stopped whenever nobody is watching, which is almost always. A stopped
	// ticker's channel never fires, so the ordinary case pays nothing at all.
	watching := watchLoop{}
	defer watching.stop()

	// The lifetime is a timer rather than a comparison at the top of each round,
	// so `--for` means what it says. Checked only between rounds, a watch given an
	// hour and a seven-minute period would run sixty-three minutes — right for the
	// default, where the period divides the hour exactly, and wrong for every
	// setting that does not. A nil channel blocks for ever, which is exactly the
	// behaviour wanted when no lifetime was asked for.
	var expired <-chan time.Time
	if stopAt != nil {
		timer := time.NewTimer(time.Until(*stopAt))
		defer timer.Stop()
		expired = timer.C
	}

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

		// How often to come back, as the server last asked.
		//
		// It arrives on the response because that is the only moment the two
		// machines are in contact: the browser sets it at the server, and a
		// watcher here is something the server can never call. A round that
		// failed says nothing about the pace, so the interval stands — a mirror
		// that sped up or slowed down because it could not reach the server would
		// be reacting to the wrong fact.
		// Who is being read, and how often to send them. A lease that has lapsed
		// arrives as nothing, which stops the narrow rounds — see store.Watching.
		watching.follow(report.Watching, a)

		a.rememberPace(home, report.Pace)
		if next := syncPace(report.Pace, every); next != every {
			_ = a.say("cq: syncing every %s from now on", round(next))
			every = next
			ticker.Reset(every)
		}

		// Asked here too, because a round can take longer than the lifetime left:
		// finishing one and then waiting for a timer that has already fired would
		// be a watch that outlives its own expiry by a whole period.
		if stopAt != nil && !time.Now().Before(*stopAt) {
			return a.say("cq: the watch has run its %s and is stopping", round(ttl))
		}
		if !typed {
			a.reLibrary(ag, src, home)
		}
		if watch.Replaced(exe, stamp) {
			// Removed first: after exec there is no `defer` left to run, and a
			// record naming this pid would then describe the new image with the old
			// one's claim. The new watcher writes its own on the way in.
			release()
			a.tell("cq: restarting into the new build")
			handedOff, err := watch.Restart(exe, selfArgs())
			if handedOff {
				// Windows: a replacement is already running. Standing down is the
				// restart — carrying on would be two watchers on one machine.
				return a.say("cq: the new build is watching now")
			}
			if err != nil {
				// Not fatal, and not the end of the watch. Carrying on means the
				// mirror keeps updating on the build it has, which is the whole
				// point of this loop; the next round tries again, and a build that
				// was mid-write when this looked will have finished by then.
				a.complain(err)
				release = announce()
				// And the stamp moves on, so a build this process cannot exec into
				// is not re-tried every round for ever. What is wanted is one
				// complaint per new build, not one per cycle.
				if now, err := watch.Look(exe); err == nil {
					stamp = now
				}
			}
		}

		// Wait for the next full round, serving narrow ones in between.
		//
		// An inner loop rather than three arms of one select, and the difference is
		// the whole feature: `continue` from a select arm goes back to the top of
		// the outer loop, which is where the *full* round is. A narrow round would
		// then drag a full mirror along behind it every three seconds — the exact
		// cost this exists to avoid, arrived at by the shape of the code rather
		// than by any decision.
		for next := false; !next; {
			select {
			case <-ticker.C:
				next = true
			case <-watching.tick():
				// A narrow round. Its failures are complained about and nothing
				// more: the pane goes stale, which the screen says out loud, and
				// the mirror is untouched either way.
				report, err := ag.Watch(context.Background(), watching.who)
				if err != nil {
					a.complain(err)
					continue
				}
				watching.follow(report.Watching, a)
			case <-expired:
				return a.say("cq: the watch has run its %s and is stopping", round(ttl))
			}
		}
	}
}

// watchLoop is the narrow cadence: who is being read, and how often.
//
// It holds a ticker rather than a deadline because the decision is not this
// machine's to make. The server owns the lease — a browser renews it, and it
// lapses on its own when nothing does — and every response says whether it is
// still live. So there is nothing to expire here, only something to follow.
type watchLoop struct {
	who    string
	every  time.Duration
	ticker *time.Ticker
}

// tick fires when the next narrow round is due, and never when nobody is
// watching: a nil channel blocks for ever, which is exactly right for a select
// arm that should not exist yet.
func (w *watchLoop) tick() <-chan time.Time {
	if w.ticker == nil {
		return nil
	}
	return w.ticker.C
}

func (w *watchLoop) stop() {
	if w.ticker != nil {
		w.ticker.Stop()
		w.ticker = nil
	}
	w.who, w.every = "", 0
}

// follow takes up what the last response said, and says so once per change.
//
// Once per change rather than once per round: a watched machine syncs every three
// seconds, and a line each time would bury everything else the watcher prints
// under a log of somebody reading a screen.
func (w *watchLoop) follow(want *protocol.Watching, a App) {
	if want == nil {
		if w.who != "" {
			_ = a.say("cq: nobody is reading %s now; back to the ordinary pace", w.who)
			w.stop()
		}
		return
	}
	every, err := time.ParseDuration(want.Every)
	if err != nil || every <= 0 {
		// A server asking for something unreadable is not a reason to spin. The
		// pane goes as stale as the ordinary mirror, which is what it looked like
		// before any of this existed.
		a.complain(fmt.Errorf("cq: the server asked to send %s every %q, which is not a duration",
			want.Identity, want.Every))
		w.stop()
		return
	}
	if w.who == want.Identity && w.every == every {
		return
	}
	w.stop()
	w.who, w.every = want.Identity, every
	w.ticker = time.NewTicker(every)
	_ = a.say("cq: sending %s every %s while it is being read", want.Identity, round(every))
}

// reLibrary picks up a library root the website has moved.
//
// Both halves are set together and from one value, because they are the same
// setting seen twice: what the collector walks, and the only directory the
// library verbs may write. A watcher that moved one and not the other would
// mirror one checkout while editing another, which no screen would show.
//
// A settings file that will not parse is complained about and stepped over
// rather than ending the watch — the mirror carrying on where it was is better
// than a machine that stops syncing over a file it can reread next round. That
// it is *said* is the part that matters: the alternative is a directory somebody
// chose that quietly never takes effect.
func (a App) reLibrary(ag *agent.Agent, src *source.CLI, home string) {
	chosen, err := stored.Read(home)
	if err != nil {
		a.complain(err)
		return
	}
	if chosen.Library == "" || chosen.Library == src.LibraryRoot {
		return
	}
	src.LibraryRoot = chosen.Library
	ag.UseLibrary(chosen.Library)
	a.tell("cq: now mirroring %s", chosen.Library)
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
	// Said out loud, never silently. A pace put back is a change somebody may have
	// made on this machine by hand being undone, and finding that out from the
	// behaviour of a fleet days later is the worst way to learn it.
	for _, put := range r.Paced {
		parts = append(parts, a.ink("pace put back: "+put, style.Warn))
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

// explicitlySet reports whether a flag was given on the command line, as opposed
// to holding its default.
//
// The distinction matters wherever a default comes from the environment: `*flag`
// cannot tell "the operator asked for this" from "$CQ_LIBRARY happened to be
// set", and those deserve different treatment when a third source — what the
// website chose — is in the running too.
func explicitlySet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// libraryFor decides which repository this run mirrors.
//
// Three answers, in this order:
//
//  1. a `--library` somebody typed for this run;
//  2. the choice recorded from the website, in the agent's home;
//  3. `$CQ_LIBRARY`, which is what `fallback` already holds.
//
// The typed flag wins because it is the more specific instruction, and because a
// flag that silently did nothing would be worse than either answer. It also stops
// the watch re-reading, so one run means one directory throughout — see
// watchSync.
//
// The recorded choice beats the environment rather than the other way round, and
// that ordering is the whole feature. A watcher is handed `os.Environ()` when it
// launches, so `$CQ_LIBRARY` is whatever it was in the shell that started it,
// possibly weeks ago; the recorded choice is what somebody decided since. An
// environment that outranked it would mean the website appeared to work and
// changed nothing.
func libraryFor(typed bool, fallback, home string) (string, error) {
	if typed || home == "" {
		return fallback, nil
	}
	chosen, err := stored.Read(home)
	if err != nil {
		return "", err
	}
	if chosen.Library != "" {
		return chosen.Library, nil
	}
	return fallback, nil
}

// syncPace reads what the server asked for, falling back to what the watcher has.
//
// Anything unparseable, absent, or tighter than the floor leaves the interval where
// it is. A server that asked for a busy-wait would be asking one machine to spend
// its time telling another what it has not done, and the floor is checked here as
// well as at the protocol so an older server cannot ask for one either.
func syncPace(asked string, now time.Duration) time.Duration {
	got, err := time.ParseDuration(strings.TrimSpace(asked))
	if err != nil || got < protocol.MinSyncPace {
		return now
	}
	return got
}

// rememberPace records what the server last asked for, so the next process starts
// where this one ended rather than where its command line did.
//
// Failing to write it is worth a word and not worth a failed sync: the mirror ran,
// and what is lost is the *starting* interval of some future watcher, which the
// server will correct on that watcher's first round anyway.
func (a App) rememberPace(home, pace string) {
	if strings.TrimSpace(pace) == "" {
		return
	}
	got, err := stored.Read(home)
	if err != nil {
		a.complain(err)
		return
	}
	if got.Pace == pace {
		return
	}
	got.Pace = pace
	if err := stored.Write(home, got); err != nil {
		a.complain(err)
	}
}

// startingPace is the interval a watcher opens with: what the server last asked
// for, or the flag when it has never asked.
//
// The flag used to decide every start, which made the first round of every watcher
// an exception to the rule every later round follows. On a machine whose watcher is
// respawned by a service manager that was not a brief exception — the command line
// is fixed at install time, so a pace chosen in the browser was overridden at every
// restart by a number nobody had looked at since.
func (a App) startingPace(home string, flag time.Duration) time.Duration {
	got, err := stored.Read(home)
	if err != nil {
		a.complain(err)
		return flag
	}
	return syncPace(got.Pace, flag)
}

// warnInstallElsewhere says when an upgrade would install where this server does
// not run from. Empty means "beside the running binary", which is always right.
func (a App) warnInstallElsewhere(binDir string) {
	if strings.TrimSpace(binDir) == "" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	here, there := filepath.Dir(exe), binDir
	if got, err := filepath.Abs(there); err == nil {
		there = got
	}
	if here == there {
		return
	}
	a.tell("%s %s", a.ink("warning", style.Warn), a.ink(fmt.Sprintf(
		"upgrades install into %s, and this server runs from %s — a rebuild would restart "+
			"on the same build", there, here), style.Quiet))
}
