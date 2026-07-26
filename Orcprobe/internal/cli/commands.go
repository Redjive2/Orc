package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"orc/orcprobe/internal/clock"
	"orc/orcprobe/internal/env"
	"orc/orcprobe/internal/fault"
	"orc/orcprobe/internal/mint"
	"orc/orcprobe/internal/neuter"
	"orc/orcprobe/internal/probe"
	"orc/orcprobe/internal/render"
	"orc/orcprobe/internal/shim"
	"orc/orcprobe/internal/spawn"
	"orc/orcprobe/internal/style"
)

// defaultIdentity is who a probe shell is when nobody says otherwise.
const defaultIdentity = mint.God

// create takes a probe of the current world.
func (a App) create(args []string, f flags) error {
	if len(args) != 1 {
		return fault.Usage{Reason: "create takes one name: orcprobe create <name>"}
	}
	store, err := a.store()
	if err != nil {
		return err
	}

	shimPath, found := shim.Find(a.Exe, a.Path)
	if !found {
		// Reported, not fatal. The environment layer still redirects everything;
		// what is missing is the layer that catches a subshell fighting it.
		a.note("orcprobe-shim was not found beside orcprobe or on the PATH; the probe will have no shims")
	}

	claude := ""
	if a.Home != "" {
		claude = filepath.Join(a.Home, ".claude")
	}

	report, err := store.Create(probe.Spec{
		Name:      args[0],
		Env:       a.Env,
		Home:      a.Home,
		Repo:      f.repo,
		NoRepo:    f.noRepo,
		Cwd:       a.Cwd,
		ClaudeDir: claude,
		FakeHome:  f.fakeHome,
		LiveState: f.liveState,
		ShimPath:  shimPath,
		BasePath:  a.Path,
	})
	if err != nil {
		return err
	}

	// A first probe becomes the default, because the alternative is every new
	// user's second command failing with "no default probe".
	current, err := store.Current()
	if err != nil {
		return err
	}
	if current == "" {
		if err := store.SetCurrent(report.Probe.Name); err != nil {
			return err
		}
	}

	return a.showCreation(report)
}

func (a App) showCreation(report *probe.Creation) error {
	p := report.Probe

	rows := make([][]render.Cell, 0, len(p.Sources)+2)
	add := func(src *probe.Source) {
		if src == nil {
			return
		}
		note := "copied"
		paint := style.Palette.Good
		if !src.Present {
			note, paint = "nothing to copy", style.Palette.Muted
		}
		rows = append(rows, []render.Cell{
			render.Painted(src.Tool, style.Palette.Probe),
			render.Plain(elide(src.From, 44)),
			render.Plain(fmt.Sprintf("%d", src.Files)),
			render.Painted(bytesText(src.Bytes), style.Palette.Muted),
			render.Painted(note, paint),
		})
	}
	for i := range p.Sources {
		add(&p.Sources[i])
	}
	add(p.Repo)
	add(p.Claude)

	table, err := render.Draw(render.Table{
		Title: "probe " + p.Name,
		Note:  p.ID,
		Columns: []render.Column{
			{Header: "source", Align: render.Left, Min: 8},
			{Header: "from", Align: render.Left, Weight: 3, Min: 12},
			{Header: "files", Align: render.Right, Min: 5},
			{Header: "size", Align: render.Right, Min: 6},
			{Header: "state", Align: render.Left, Min: 8},
		},
		Rows:  rows,
		Empty: "nothing was copied",
	}, a.out, a.Width)
	if err != nil {
		return err
	}
	if err := a.write(table); err != nil {
		return err
	}

	lines := []string{
		fmt.Sprintf("%s  %d mailboxes have probe keys; no real credential was copied",
			a.out.Good("✓"), report.Identities),
	}
	if p.Neutered {
		lines = append(lines, fmt.Sprintf("%s  %s", a.out.Good("✓"), scrubbed(report.Scrub)))
	}
	for _, held := range report.Scrub.Unreleased {
		lines = append(lines, fmt.Sprintf("%s  task %s still shows %s as its owner: macmuffin has no `release` op,",
			a.out.Warn("!"), held.Task, held.Owner))
		lines = append(lines, "     so there is no valid event to append. Nothing is running — but it does not look unclaimed.")
	}
	if len(report.Remotes) > 0 {
		lines = append(lines, fmt.Sprintf("%s  git remotes removed: %s",
			a.out.Good("✓"), strings.Join(report.Remotes, ", ")))
	}
	if report.Worktrees > 0 {
		lines = append(lines, fmt.Sprintf("%s  %d worktree registration(s) removed", a.out.Good("✓"), report.Worktrees))
	}
	if report.Drops > 0 {
		lines = append(lines, fmt.Sprintf("%s  %d thing(s) deliberately left behind — see `orcprobe manifest`",
			a.out.Note("·"), report.Drops))
	}
	for _, skip := range report.Skipped {
		lines = append(lines, fmt.Sprintf("%s  %s could not be reminted: %s", a.out.Warn("!"), skip.Name, skip.Why))
	}
	if report.Shims {
		lines = append(lines, fmt.Sprintf("%s  shims installed: %s", a.out.Good("✓"), strings.Join(shim.Commands(), " ")))
	}
	for _, note := range report.Deferred {
		lines = append(lines, fmt.Sprintf("%s  %s", a.out.Bad("✗"), note))
	}
	lines = append(lines, "", "  enter it with: orcprobe shell --probe "+p.Name)

	return a.say(strings.Join(lines, "\n"))
}

// list shows every probe.
func (a App) list(args []string, f flags) error {
	if len(args) != 0 {
		return fault.Usage{Reason: "list takes no arguments"}
	}
	store, err := a.store()
	if err != nil {
		return err
	}

	probes, unfinished, err := store.List()
	if err != nil {
		return err
	}
	current, err := store.Current()
	if err != nil {
		return err
	}
	now := a.Clock.Now()

	rows := make([][]render.Cell, 0, len(probes))
	for _, p := range probes {
		var (
			bytesTotal int64
			present    int
		)
		for _, src := range p.Sources {
			bytesTotal += src.Bytes
			if src.Present {
				present++
			}
		}
		if p.Repo != nil {
			bytesTotal += p.Repo.Bytes
		}

		marker := " "
		if p.Name == current {
			marker = "●"
		}
		age := "unknown"
		if at, err := p.CreatedAt(); err == nil {
			age = clock.Since(now, at)
		}
		state := p.Liveness()
		paint := style.Palette.Warn
		if state == "neutered" {
			paint = style.Palette.Good
		}

		points, err := store.Checkpoints(p)
		if err != nil {
			return err
		}

		rows = append(rows, []render.Cell{
			render.Painted(marker+" "+p.Name, style.Palette.Probe),
			render.Painted(age, style.Palette.Muted),
			render.Plain(fmt.Sprintf("%d/%d", present, len(p.Sources))),
			render.Plain(fmt.Sprintf("%d", p.Identities)),
			render.Painted(bytesText(bytesTotal), style.Palette.Muted),
			render.Painted(state, paint),
			render.Painted(checkpointNote(points), style.Palette.Muted),
		})
	}

	table, err := render.Draw(render.Table{
		Title: "probes",
		Note:  fmt.Sprintf("%d", len(probes)),
		Columns: []render.Column{
			{Header: "name", Align: render.Left, Weight: 1, Min: 6},
			{Header: "taken", Align: render.Left, Min: 5},
			{Header: "state", Align: render.Right, Min: 5},
			{Header: "ids", Align: render.Right, Min: 3},
			{Header: "size", Align: render.Right, Min: 6},
			{Header: "liveness", Align: render.Left, Min: 8},
			{Header: "saved", Align: render.Left, Weight: 1, Min: 5},
		},
		Rows:  rows,
		Empty: "no probes yet — orcprobe create <name>",
	}, a.out, a.Width)
	if err != nil {
		return err
	}
	if err := a.write(table); err != nil {
		return err
	}
	for _, name := range unfinished {
		a.note("%s was never finished; remove it with `orcprobe destroy %s`", name, name)
	}
	return nil
}

// use sets the default probe.
func (a App) use(args []string, f flags) error {
	if len(args) != 1 {
		return fault.Usage{Reason: "use takes one name: orcprobe use <name>"}
	}
	store, err := a.store()
	if err != nil {
		return err
	}
	if err := store.SetCurrent(args[0]); err != nil {
		return err
	}
	return a.say(args[0] + " is now the default probe")
}

// manifest shows how a probe was made.
func (a App) manifest(args []string, f flags) error {
	if len(args) != 0 {
		return fault.Usage{Reason: "manifest takes no arguments"}
	}
	store, err := a.store()
	if err != nil {
		return err
	}
	p, err := store.Resolve(f.probe)
	if err != nil {
		return err
	}

	entries, skipped, err := probe.ReadManifest(p.Path(probe.ManifestFile))
	if err != nil {
		return err
	}

	rows := make([][]render.Cell, 0, len(entries))
	for _, e := range entries {
		paint := style.Palette.Muted
		switch e.Act {
		case probe.ActCopy, probe.ActMint, probe.ActStamp:
			paint = style.Palette.Good
		case probe.ActDrop:
			paint = style.Palette.Note
		case probe.ActDefer:
			paint = style.Palette.Bad
		}
		at := e.At
		if t, err := clock.Parse(e.At); err == nil {
			at = t.Format(clock.Display)
		}
		rows = append(rows, []render.Cell{
			render.Painted(at, style.Palette.Muted),
			render.Painted(e.Act, paint),
			render.Painted(e.What, style.Palette.Probe),
			render.Plain(e.Detail),
		})
	}

	table, err := render.Draw(render.Table{
		Title: "manifest — " + p.Name,
		Note:  fmt.Sprintf("%d entries", len(entries)),
		Columns: []render.Column{
			{Header: "at", Align: render.Left, Min: 16},
			{Header: "act", Align: render.Left, Min: 5},
			{Header: "what", Align: render.Left, Weight: 1, Min: 8},
			{Header: "detail", Align: render.Left, Weight: 3, Min: 12},
		},
		Rows:  rows,
		Empty: "nothing recorded",
	}, a.out, a.Width)
	if err != nil {
		return err
	}
	if err := a.write(table); err != nil {
		return err
	}
	if skipped > 0 {
		a.note("%d bytes at the end of the manifest were left by an interrupted write and were ignored", skipped)
	}
	return nil
}

// destroy removes a probe whole. It is the only irreversible command here.
func (a App) destroy(args []string, f flags) error {
	if len(args) != 1 {
		return fault.Usage{Reason: "destroy takes one name: orcprobe destroy <name> --yes"}
	}
	store, err := a.store()
	if err != nil {
		return err
	}
	p, err := store.Get(args[0])
	if err != nil {
		// An unfinished probe cannot be opened but must still be removable, or
		// a crashed creation would be permanent litter.
		if _, isConflict := err.(fault.Conflict); !isConflict {
			return err
		}
	}

	target := args[0]
	detail := ""
	if p != nil {
		target = p.Name
		detail = fmt.Sprintf(" (%s, %d mailboxes)", p.ID, p.Identities)
	}
	if err := a.say("will remove probe " + target + detail); err != nil {
		return err
	}

	if !f.yes {
		if !a.Terminal {
			return fault.Usage{Reason: "destroy is irreversible; pass --yes to confirm"}
		}
		ok, err := a.confirm("remove it? [y/N] ")
		if err != nil {
			return err
		}
		if !ok {
			return a.say("left alone")
		}
	}
	if err := store.Destroy(target); err != nil {
		return err
	}
	return a.say("probe " + target + " is gone")
}

func (a App) confirm(prompt string) (bool, error) {
	if _, err := fmt.Fprint(a.Stderr, prompt); err != nil {
		return false, fault.IO{Op: "prompt on", Path: "stderr", Err: err}
	}
	if a.Stdin == nil {
		return false, nil
	}
	reader := bufio.NewReader(a.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return false, nil
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

// shell opens a subshell inside a probe.
func (a App) shell(args []string, f flags) error {
	if len(args) != 0 {
		return fault.Usage{Reason: "shell takes no arguments; use `orcprobe as <user> -- <cmd>` to run one command"}
	}
	session, err := a.enter(f.probe, f.as)
	if err != nil {
		return err
	}

	shellPath := a.Shell
	if shellPath == "" {
		if s, ok := a.Env("SHELL"); ok {
			shellPath = s
		}
	}
	if shellPath == "" {
		shellPath = "/bin/sh"
	}

	if err := a.banner(session); err != nil {
		return err
	}
	status, err := spawn.Run(spawn.Request{
		Path:   shellPath,
		Env:    session.env,
		Dir:    session.dir,
		Stdin:  a.Stdin,
		Stdout: a.Stdout,
		Stderr: a.Stderr,
	})
	if err != nil {
		return err
	}
	if status != 0 {
		return exitStatus(status)
	}
	return nil
}

// runAs runs one command inside a probe, as one identity.
//
// Its arguments are parsed by hand rather than by parseFlags, because
// everything after the identity belongs to the command being run — including
// things that look exactly like orcprobe's own flags.
func (a App) runAs(args []string) error {
	var (
		f    flags
		who  string
		rest []string
	)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			rest = args[i+1:]
			i = len(args)
		case arg == "--probe" && i+1 < len(args):
			i++
			f.probe = args[i]
		case strings.HasPrefix(arg, "--probe="):
			f.probe = strings.TrimPrefix(arg, "--probe=")
		case who == "" && !strings.HasPrefix(arg, "-"):
			who = arg
		default:
			rest = args[i:]
			i = len(args)
		}
	}
	if who == "" {
		return fault.Usage{Reason: "as takes an identity: orcprobe as <user> -- <cmd...>"}
	}
	if len(rest) == 0 {
		return fault.Usage{Reason: "as needs a command: orcprobe as " + who + " -- <cmd...>"}
	}

	session, err := a.enter(f.probe, who)
	if err != nil {
		return err
	}

	command, rest := rest[0], rest[1:]
	// The same refusals the shim applies, applied here too. Orcprobe must not be
	// a way around its own guards when the shims are missing.
	if err := shim.Check(command, rest); err != nil {
		return err
	}

	path, err := shim.Real(command, session.path, "")
	if err != nil {
		return err
	}
	status, err := spawn.Run(spawn.Request{
		Path:   path,
		Args:   rest,
		Env:    session.env,
		Dir:    session.dir,
		Stdin:  a.Stdin,
		Stdout: a.Stdout,
		Stderr: a.Stderr,
	})
	if err != nil {
		return err
	}
	if status != 0 {
		return exitStatus(status)
	}
	return nil
}

// session is a resolved probe, an identity, and the environment they imply.
type session struct {
	probe *probe.Probe
	who   string
	env   []string
	path  string
	dir   string
}

// enter resolves a probe and an identity and composes the environment to run
// in. It is the one place identity is chosen, so `shell` and `as` cannot drift
// apart in what they mean by "as".
func (a App) enter(name, who string) (session, error) {
	store, err := a.store()
	if err != nil {
		return session{}, err
	}
	p, err := store.Resolve(name)
	if err != nil {
		return session{}, err
	}

	vars, err := env.Load(p.Path(probe.EnvFile))
	if err != nil {
		return session{}, err
	}
	keys, err := mint.Load(p.Path(probe.IdentitiesFile))
	if err != nil {
		return session{}, err
	}
	if strings.TrimSpace(who) == "" {
		who = keys.Default
	}
	identity, err := keys.Find(who)
	if err != nil {
		return session{}, err
	}

	// The probe's stamp is checked before anything is run in it. A probe
	// directory that lost its stamp is not one orcprobe will treat as isolated.
	if _, err := probe.ReadStamp(p.Dir()); err != nil {
		return session{}, err
	}

	full := env.Apply(a.Environ, vars,
		env.Identity(identity.Name, identity.Key),
		[]env.Var{env.PromptVar(p.Name, identity.Name)})

	path, _ := env.Lookup(vars, env.Path)
	dir := p.Path(probe.RepoDir)
	if _, err := os.Stat(dir); err != nil {
		dir = p.Dir()
	}
	return session{probe: p, who: identity.Name, env: full, path: path, dir: dir}, nil
}

// banner says, once, what a probe shell is and what it does not stop. It goes
// to stderr, so it never lands in a pipe, and it is not optional: the whole
// design rests on the operator knowing which guards are real.
//
// It paints from a.err rather than a.out, and that is the whole reason the two
// palettes exist. `orcprobe shell > log` writes this to a terminal while stdout
// is a file; `orcprobe shell 2> log` is the reverse. One palette for both would
// either drop the colour where a person is reading it or write escape codes
// into a file where nobody is.
func (a App) banner(s session) error {
	lines := []string{
		"",
		fmt.Sprintf("  %s  %s  as %s",
			a.err.Title("probe"), a.err.Probe(s.probe.Name), a.err.User(s.who)),
		fmt.Sprintf("  %s  mail, tasks, and cq state are copies; the repo has no remotes", a.err.Good("✓")),
		fmt.Sprintf("  %s  cq sync, git push, and orc are refused in here", a.err.Good("✓")),
		fmt.Sprintf("  %s  an absolute path to a real store is refused by the tool itself,", a.err.Good("✓")),
		"     as long as its build has the stamp guard",
		fmt.Sprintf("  %s  exit to leave", a.err.Note("·")),
		"",
	}
	_, err := fmt.Fprintln(a.Stderr, strings.Join(lines, "\n"))
	return err
}

// scrubbed says what neutering took out, in one line.
//
// A probe where nothing was live says so rather than staying silent: "no claims
// to release" and "the scrub did not run" look identical in an empty summary,
// and only one of them means the probe is inert.
func scrubbed(rep neuter.Report) string {
	parts := []string{}
	if n := len(rep.Released); n > 0 {
		parts = append(parts, fmt.Sprintf("%d task(s) released", n))
	}
	if n := len(rep.Unreleased); n > 0 {
		parts = append(parts, fmt.Sprintf("%d task(s) still owned", n))
	}
	if rep.Collaborators > 0 {
		parts = append(parts, fmt.Sprintf("%d collaborator(s) removed", rep.Collaborators))
	}
	if rep.Worktrees > 0 {
		parts = append(parts, fmt.Sprintf("%d worktree binding(s) dropped", rep.Worktrees))
	}
	if rep.Outbox > 0 {
		parts = append(parts, fmt.Sprintf("%d undelivered notification(s) dropped", rep.Outbox))
	}
	if rep.Sessions > 0 {
		parts = append(parts, fmt.Sprintf("%d live-session claim(s) cut", rep.Sessions))
	}
	if n := len(rep.Hooks); n > 0 {
		parts = append(parts, fmt.Sprintf("%d hook(s) disabled", n))
	}
	if len(parts) == 0 {
		return "neutered: nothing in this world was live"
	}
	return "neutered: " + strings.Join(parts, ", ")
}

// elide shortens a path from the left, keeping the end that identifies it.
func elide(path string, max int) string {
	out, err := style.Elide(path, max)
	if err != nil {
		return path
	}
	return out
}
