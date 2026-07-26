package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"orc/orcprobe/internal/clock"
	"orc/orcprobe/internal/doctor"
	"orc/orcprobe/internal/fault"
	"orc/orcprobe/internal/probe"
	"orc/orcprobe/internal/render"
	"orc/orcprobe/internal/snapshot"
	"orc/orcprobe/internal/source"
	"orc/orcprobe/internal/style"
)

// save checkpoints a probe as it stands.
func (a App) save(args []string, f flags) error {
	if len(args) != 1 {
		return fault.Usage{Reason: "save takes one label: orcprobe save <label>"}
	}
	store, err := a.store()
	if err != nil {
		return err
	}
	p, err := store.Resolve(f.probe)
	if err != nil {
		return err
	}

	point, err := store.Save(p, args[0])
	if err != nil {
		return err
	}
	return a.say(fmt.Sprintf("saved %s — %d files, %s\n  rewind with: orcprobe restore %s --probe %s",
		point.Label, point.Files, bytesText(point.Bytes), point.Label, p.Name))
}

// restore rewinds a probe to a checkpoint.
//
// It overwrites everything since, so it is confirmed the same way destroy is:
// an agent, whose stdin is never a terminal, must pass --yes.
func (a App) restore(args []string, f flags) error {
	if len(args) != 1 {
		return fault.Usage{Reason: "restore takes one label: orcprobe restore <label> --yes"}
	}
	store, err := a.store()
	if err != nil {
		return err
	}
	p, err := store.Resolve(f.probe)
	if err != nil {
		return err
	}

	if err := a.say("will rewind probe " + p.Name + " to " + args[0] + ", discarding everything since"); err != nil {
		return err
	}
	if !f.yes {
		if !a.Terminal {
			return fault.Usage{Reason: "restore discards work; pass --yes to confirm"}
		}
		ok, err := a.confirm("rewind it? [y/N] ")
		if err != nil {
			return err
		}
		if !ok {
			return a.say("left alone")
		}
	}

	if err := store.Restore(p, args[0]); err != nil {
		return err
	}
	return a.say("probe " + p.Name + " is back at " + args[0])
}

// diff compares two probes, or a probe against the world it was taken from.
func (a App) diff(args []string, f flags) error {
	store, err := a.store()
	if err != nil {
		return err
	}

	if f.source {
		if len(args) > 1 {
			return fault.Usage{Reason: "diff --source takes at most one probe"}
		}
		name := f.probe
		if len(args) == 1 {
			name = args[0]
		}
		p, err := store.Resolve(name)
		if err != nil {
			return err
		}
		return a.driftAgainstSource(p)
	}

	if len(args) != 2 {
		return fault.Usage{Reason: "diff takes two probes, or --source to compare one against the world it came from"}
	}
	left, err := store.Get(args[0])
	if err != nil {
		return err
	}
	right, err := store.Get(args[1])
	if err != nil {
		return err
	}

	rows := make([][]render.Cell, 0, 8)
	for _, part := range []string{probe.StateDir, probe.RepoDir, probe.ClaudeDir} {
		d, err := snapshot.Compare(left.Path(part), right.Path(part))
		if err != nil {
			return err
		}
		rows = append(rows, diffRow(part, d))
	}

	table, err := render.Draw(render.Table{
		Title: "diff · " + left.Name + " → " + right.Name,
		Columns: []render.Column{
			{Header: "part", Align: render.Left, Min: 6},
			{Header: "added", Align: render.Right, Min: 5},
			{Header: "removed", Align: render.Right, Min: 7},
			{Header: "changed", Align: render.Right, Min: 7},
			{Header: "same", Align: render.Right, Min: 4},
			{Header: "verdict", Align: render.Left, Weight: 1, Min: 8},
		},
		Rows: rows,
	}, a.out, a.Width)
	if err != nil {
		return err
	}
	return a.write(table)
}

func diffRow(part string, d snapshot.Diff) []render.Cell {
	var added, removed, changed int
	for _, c := range d.Changes {
		switch c.Kind {
		case snapshot.Added:
			added++
		case snapshot.Removed:
			removed++
		case snapshot.Changed:
			changed++
		}
	}

	verdict, paint := "identical", style.Palette.Good
	if d.Count() > 0 {
		verdict, paint = fmt.Sprintf("%d difference(s)", d.Count()), style.Palette.Warn
	}
	if d.Truncated > 0 {
		verdict += fmt.Sprintf(", %d not listed", d.Truncated)
	}

	return []render.Cell{
		render.Painted(part, style.Palette.Probe),
		render.Plain(fmt.Sprintf("%d", added)),
		render.Plain(fmt.Sprintf("%d", removed)),
		render.Plain(fmt.Sprintf("%d", changed)),
		render.Painted(fmt.Sprintf("%d", d.Same), style.Palette.Muted),
		render.Painted(verdict, paint),
	}
}

// driftAgainstSource answers "has the world moved since this probe was taken?"
//
// It compares each source's recorded digest against the real root as it is now.
// This is the one command that reads a real store after creation, and it reads
// only: the digest walk opens every file O_RDONLY and writes nothing.
func (a App) driftAgainstSource(p *probe.Probe) error {
	rows := make([][]render.Cell, 0, len(p.Sources))

	for _, src := range p.Sources {
		if !src.Present {
			rows = append(rows, []render.Cell{
				render.Painted(src.Tool, style.Palette.Probe),
				render.Painted(elide(src.From, 40), style.Palette.Path),
				render.Painted("nothing was there", style.Palette.Muted),
			})
			continue
		}

		now, err := snapshot.Digest(src.From)
		verdict, paint := "unchanged", style.Palette.Good
		switch {
		case err != nil:
			verdict, paint = "cannot be read now", style.Palette.Warn
		case src.Digest == "":
			verdict, paint = "no digest was recorded", style.Palette.Muted
		case now != src.Digest:
			verdict, paint = "the world has moved on", style.Palette.Warn
		}
		rows = append(rows, []render.Cell{
			render.Painted(src.Tool, style.Palette.Probe),
			render.Painted(elide(src.From, 40), style.Palette.Path),
			render.Painted(verdict, paint),
		})
	}

	age := "unknown"
	if at, err := p.CreatedAt(); err == nil {
		age = clock.Since(a.Clock.Now(), at)
	}

	table, err := render.Draw(render.Table{
		Title: "drift · " + p.Name,
		Note:  "taken " + age,
		Columns: []render.Column{
			{Header: "source", Align: render.Left, Min: 8},
			{Header: "from", Align: render.Left, Weight: 2, Min: 12},
			{Header: "since the probe was taken", Align: render.Left, Weight: 1, Min: 10},
		},
		Rows:  rows,
		Empty: "this probe copied nothing",
	}, a.out, a.Width)
	if err != nil {
		return err
	}
	return a.write(table)
}

// doctor checks every guard and says which are in force.
func (a App) doctor(args []string, f flags) error {
	if len(args) != 0 {
		return fault.Usage{Reason: "doctor takes no arguments"}
	}
	store, err := a.store()
	if err != nil {
		return err
	}
	p, err := store.Resolve(f.probe)
	if err != nil {
		return err
	}

	tools := source.Tools()
	stateDirs := map[string]string{}
	for _, tool := range tools {
		dir := p.Path(filepath.FromSlash(tool.Dir))
		for _, src := range p.Sources {
			if src.Tool == tool.Name && src.Present {
				stateDirs[tool.Command] = dir
			}
		}
	}
	repoDir := ""
	if p.Repo != nil && p.Repo.Present {
		repoDir = p.Path(probe.RepoDir)
	}

	report, err := doctor.Run(doctor.Spec{
		ProbeID:    p.ID,
		ProbeDir:   p.Dir(),
		StateDirs:  stateDirs,
		RepoDir:    repoDir,
		BinDir:     p.Path(probe.BinDir),
		EnvFile:    p.Path(probe.EnvFile),
		Identities: p.Path(probe.IdentitiesFile),
		Environ:    a.Environ,
		Path:       a.Path,
	})
	if err != nil {
		return err
	}

	rows := make([][]render.Cell, 0, len(report.Checks))
	for _, c := range report.Checks {
		paint := style.Palette.Good
		switch c.State {
		case doctor.Absent:
			paint = style.Palette.Bad
		case doctor.Skipped:
			paint = style.Palette.Muted
		case doctor.Warn:
			paint = style.Palette.Warn
		}
		rows = append(rows, []render.Cell{
			render.Painted(c.Guard, style.Palette.ID),
			render.Plain(c.What),
			render.Painted(string(c.State), paint),
			render.Painted(c.Detail, style.Palette.Muted),
		})
	}

	inForce, absent, skipped, partial := report.Counts()
	note := fmt.Sprintf("%d in force", inForce)
	if partial > 0 {
		note += fmt.Sprintf(" · %d partial", partial)
	}
	if absent > 0 {
		note += fmt.Sprintf(" · %d absent", absent)
	}
	if skipped > 0 {
		note += fmt.Sprintf(" · %d not checked", skipped)
	}

	table, err := render.Draw(render.Table{
		Title: "doctor · " + p.Name,
		Note:  note,
		Columns: []render.Column{
			{Header: "guard", Align: render.Left, Min: 6},
			{Header: "what", Align: render.Left, Min: 8},
			{Header: "state", Align: render.Left, Min: 9},
			{Header: "detail", Align: render.Left, Weight: 3, Min: 12},
		},
		Rows: rows,
	}, a.out, a.Width)
	if err != nil {
		return err
	}
	if err := a.write(table); err != nil {
		return err
	}

	// The summary says what the table means, because the one thing an operator
	// must not do is skim a doctor report and take silence for safety.
	var verdict string
	switch {
	case absent > 0:
		verdict = a.out.Bad("✗") + "  this probe is not fully contained; the guards above marked absent are not protecting you"
	case !report.Measured():
		verdict = a.out.Warn("!") + "  every guard that could be checked is in force; the ones marked not checked were not measured, not confirmed"
	default:
		verdict = a.out.Good("✓") + "  every guard is in force"
	}
	if err := a.say("\n  " + verdict); err != nil {
		return err
	}

	if f.strict && (absent > 0 || !report.Measured()) {
		return exitStatus(CodeEscape)
	}
	return nil
}

// checkpointNote renders a probe's checkpoints for `list`.
func checkpointNote(points []probe.Checkpoint) string {
	if len(points) == 0 {
		return "—"
	}
	labels := make([]string, 0, len(points))
	for _, c := range points {
		labels = append(labels, c.Label)
	}
	if len(labels) > 2 {
		return fmt.Sprintf("%d: %s…", len(labels), strings.Join(labels[:2], " "))
	}
	return strings.Join(labels, " ")
}
