// Package cli implements Anno's commands.
//
// Each command is a small function over an App, which carries the streams to
// write to. Commands return errors; the mapping from error to exit code lives
// in one place, Code, so a new failure mode cannot accidentally exit zero.
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"orc/anno/internal/edit"
	"orc/anno/internal/guard"
	"orc/anno/internal/render"
	"orc/anno/internal/style"
	"orc/anno/internal/target"
	"orc/anno/internal/tree"
	"orc/common/fault"
	"orc/common/source"
	"orc/theme"
)

// Exit codes. They are stable: hooks branch on them.
//
// They are the shared table in orc/common/fault rather than a copy of it. Anno
// once had its own, and it silently fell behind the moment another tool added a
// code — an out-of-scope write came back as 70, "this is a bug", which is both
// wrong and alarming. One table, one meaning per number, across every tool.
const (
	CodeOK        = fault.CodeOK
	CodeUsage     = fault.CodeUsage
	CodeNotFound  = fault.CodeNotFound
	CodeAmbiguous = fault.CodeAmbiguous
	CodeParse     = fault.CodeParse
	CodeIO        = fault.CodeIO
	CodeConflict  = fault.CodeConflict
	CodeScope     = fault.CodeScope
	CodeInternal  = fault.CodeInternal
)

// Colour flags. They are global rather than per-command, and are taken off the line
// before dispatch, so `anno --no-color index x.go` and `anno index x.go --no-color`
// both work and no command has to know they exist.
//
// The environment (NO_COLOR, ORC_AGENT, ORC_THEME=none) already turns colour off, but
// an environment variable is awkward for a caller assembling one command — which is
// what Orc does when it runs Anno inside a session — so there is a flag for the same
// thing. --color forces it on for the opposite case: a caller piping Anno somewhere
// that renders escapes.
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

// App carries the streams a command reads from and writes to, and how each one
// is styled. The zero palettes are plain, so a caller that says nothing about
// colour gets none — which is what a pipe, a hook, and a test all want.
type App struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Out    style.Palette
	Err    style.Palette

	// Look reads the environment, for the one decision Main makes itself:
	// --color has to honour ORC_THEME's flavour and must still lose to
	// ORC_AGENT. Nil reads the process environment.
	Look theme.Look

	// Scope asks whether a path may be written. Nil means ask the real
	// Macmuffin; a test supplies its own answer rather than execing anything.
	Scope guard.Check
}

// allowed reports whether a write may proceed. A caller that set no check gets
// the real one.
func (a App) allowed(path string) error {
	check := a.Scope
	if check == nil {
		check = guard.Exec
	}
	return check(path)
}

// Main runs a command line and returns the process exit code. It is the only
// function that converts an error into a status, and it recovers from panics so
// that even a defect in Anno produces a diagnosed exit rather than a crash.
func Main(app App, args []string) (code int) {
	defer func() {
		if r := recover(); r != nil {
			_, _ = fmt.Fprintf(app.Stderr, "%s %v\n", app.Err.Paint("anno:", style.Alarm), fault.Internal{
				Where:  "cli.Main",
				Detail: fmt.Sprintf("panic: %v", r),
			})
			code = CodeInternal
		}
	}()

	if err := app.validate(); err != nil {
		// Without streams there is nowhere to report; the code still says why.
		return CodeInternal
	}

	args, force, off := takeColourFlags(args)
	if off {
		app.Out, app.Err = style.Plain(), style.Plain()
	}
	if force {
		app.Out, app.Err = app.forced(), app.forced()
	}

	err := app.dispatch(args)
	if err == nil {
		return CodeOK
	}

	// Diagnostics go to stderr; if stderr itself is broken there is nowhere to
	// report that, so the exit code carries the outcome on its own.
	code = Code(err)
	_, _ = fmt.Fprintf(app.Stderr, "%s %v\n", app.Err.Paint("anno:", style.Alarm), err)
	// The full screen no longer follows every usage error: a refusal that has
	// already said what was wrong reads better without the chain syntax under it,
	// and the refusals that need one carry their own pointer to `anno help`.
	//
	// `anno` on its own is the exception, and the only one: nothing was named, so
	// there is no refusal to read and the useful answer is what the verbs are.
	if code == CodeUsage && len(args) == 0 {
		_, _ = fmt.Fprintln(app.Stderr, "\n"+brief(app.Err))
	}
	return code
}

// forced is the palette --color asks for.
//
// It resolves the scheme rather than assuming one, so `--color` in a Frappé terminal
// is Frappé. ORC_AGENT still wins: turning colour off for every tool at once must not
// be defeatable by one command, and Resolve is where that rule lives.
//
// A misspelled ORC_THEME leaves it plain here. The command that follows still reports
// the bad setting, and refusing to *draw* is a better failure than refusing to say
// why.
func (a App) forced() style.Palette {
	look := a.Look
	if look == nil {
		look = theme.OSLook
	}
	cfg, err := theme.Resolve(look, true)
	if err != nil {
		return style.Plain()
	}
	return style.New(cfg.Palette)
}

func (a App) validate() error {
	if a.Stdout == nil || a.Stderr == nil {
		return fault.Internal{Where: "cli.App", Detail: "output streams are not set"}
	}
	return nil
}

// Code maps an error to an exit code.
//
// It is the shared classifier: a tool that mapped errors its own way would make
// the same fault mean different things depending on which binary produced it,
// and hooks branch on these numbers.
func Code(err error) int { return fault.Code(err) }

func (a App) dispatch(args []string) error {
	if len(args) == 0 {
		return fault.Usage{Reason: "no command given"}
	}
	command, rest := args[0], args[1:]

	switch command {
	case "index":
		return a.index(rest)
	case "overview":
		return a.overview(rest)
	case "read":
		return a.read(rest)
	case "find":
		return a.find(rest)
	case "write":
		return a.write(rest)
	case "help", "-h", "--help":
		return a.help(rest)
	default:
		return unknown(command)
	}
}

// help answers `anno help`, and `anno help <command>` for one of them.
func (a App) help(args []string) error {
	switch len(args) {
	case 0:
		return a.say(usage(a.Out))
	case 1:
		got, ok := commandHelp(a.Out, args[0])
		if !ok {
			return noSuchTopic(args[0])
		}
		return a.say(got)
	default:
		return fault.Usage{Reason: "help takes one command, or nothing"}
	}
}

// index renders one file's annotation tree.
func (a App) index(args []string) error {
	args, asJSON, err := takeFlag(args, "--json")
	if err != nil {
		return err
	}
	if len(args) != 1 {
		return fault.Usage{Reason: fmt.Sprintf("index takes one file path, got %d arguments", len(args))}
	}
	t, err := load(args[0])
	if err != nil {
		return err
	}
	if asJSON {
		return a.emitJSON(treeJSON(t))
	}
	out, err := render.Index(t, a.Out)
	if err != nil {
		return err
	}
	_, err = io.WriteString(a.Stdout, out)
	return err
}

// overview renders every readable file in a directory. Files Anno cannot parse
// are reported and skipped: an overview of a directory should not fail because
// one file in it is a binary.
func (a App) overview(args []string) error {
	args, asJSON, err := takeFlag(args, "--json")
	if err != nil {
		return err
	}
	if len(args) != 1 {
		return fault.Usage{Reason: fmt.Sprintf("overview takes one directory path, got %d arguments", len(args))}
	}
	dir := args[0]
	files, err := listDir(dir)
	if err != nil {
		return err
	}

	shown := 0
	trees := []jsonTree{}
	for _, path := range files {
		t, err := load(path)
		if err != nil {
			a.skip(path, err)
			continue
		}
		if asJSON {
			trees = append(trees, treeJSON(t))
			shown++
			continue
		}
		out, err := render.Index(t, a.Out)
		if err != nil {
			a.skip(path, err)
			continue
		}
		if shown > 0 {
			if err := a.say(""); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(a.Stdout, out); err != nil {
			return err
		}
		shown++
	}
	// An empty directory is an empty array under --json, not a failure: a caller
	// mirroring a repository should get "nothing annotated here" as data rather
	// than as an error it has to special-case.
	if asJSON {
		return a.emitJSON(trees)
	}
	if shown == 0 {
		return fault.NotFound{Target: dir}
	}
	return nil
}

// read emits an annotation's span.
func (a App) read(args []string) error {
	if len(args) != 1 {
		return fault.Usage{Reason: fmt.Sprintf("read takes one target, got %d arguments", len(args))}
	}
	tgt, err := locate(args[0], false)
	if err != nil {
		return err
	}
	f, err := source.Load(tgt.Path())
	if err != nil {
		return err
	}
	t, err := tree.Build(f)
	if err != nil {
		return err
	}

	if tgt.IsFile() {
		return a.emit(f, tree.Range{}, true)
	}

	m, err := unique(t, tgt)
	if err != nil {
		return err
	}
	node, err := m.Node()
	if err != nil {
		return err
	}
	return a.emit(f, node.Span(), false)
}

// find resolves a chain across a directory, reporting every match.
func (a App) find(args []string) error {
	if len(args) != 1 {
		return fault.Usage{Reason: fmt.Sprintf("find takes one target, got %d arguments", len(args))}
	}
	tgt, err := locate(args[0], true)
	if err != nil {
		return err
	}
	if tgt.IsFile() {
		return fault.Usage{Reason: fmt.Sprintf("find needs an annotation chain, as in %s^name", tgt.Path())}
	}

	files, err := listDir(tgt.Path())
	if err != nil {
		return err
	}

	found := 0
	var near []string
	for _, path := range files {
		f, err := source.Load(path)
		if err != nil {
			a.skip(path, err)
			continue
		}
		t, err := tree.Build(f)
		if err != nil {
			a.skip(path, err)
			continue
		}
		matches, err := target.Resolve(t, tgt.Steps())
		if err != nil {
			return err
		}
		near = append(near, target.Near(t, tgt.Steps())...)
		for _, m := range matches {
			node, err := m.Node()
			if err != nil {
				return err
			}
			row, err := render.Row(t, m.Path(), a.Out)
			if err != nil {
				return err
			}
			if found > 0 {
				if err := a.say(""); err != nil {
					return err
				}
			}
			if err := a.say(a.Out.Paint(m.Qualified(), style.Name)); err != nil {
				return err
			}
			if err := a.say(row); err != nil {
				return err
			}
			if err := a.emit(f, node.Span(), false); err != nil {
				return err
			}
			found++
		}
	}
	if found == 0 {
		return fault.NotFound{Target: tgt.Raw(), Near: trim(near)}
	}
	return nil
}

// write replaces an annotation's content.
func (a App) write(args []string) error {
	if len(args) != 2 {
		return fault.Usage{Reason: fmt.Sprintf("write takes a target and content, got %d arguments", len(args))}
	}
	tgt, err := locate(args[0], false)
	if err != nil {
		return err
	}
	if tgt.IsFile() {
		return fault.Usage{Reason: fmt.Sprintf("write needs an annotation chain, as in %s^name; anno does not overwrite whole files", tgt.Path())}
	}

	content := args[1]
	if content == "-" {
		if a.Stdin == nil {
			return fault.Usage{Reason: `content is "-" but no standard input is available`}
		}
		data, err := io.ReadAll(a.Stdin)
		if err != nil {
			return fault.IO{Op: "read standard input for", Path: tgt.Path(), Err: err}
		}
		content = string(data)
	}

	// Asked before the file is read, let alone written: a refusal should cost
	// nothing and change nothing. This is the mechanism behind Macmuffin's
	// "enforces editing even via Anno" — see internal/guard.
	if err := a.allowed(tgt.Path()); err != nil {
		return err
	}

	f, err := source.Load(tgt.Path())
	if err != nil {
		return err
	}
	t, err := tree.Build(f)
	if err != nil {
		return err
	}
	m, err := unique(t, tgt)
	if err != nil {
		return err
	}

	plan, err := edit.Prepare(f, m, tgt.Steps(), content)
	if err != nil {
		return err
	}
	if err := edit.Commit(plan); err != nil {
		return err
	}
	return a.say(a.Out.Paint(plan.Summary(), style.Good))
}

// say writes one line to stdout. A failed write is reported rather than
// dropped: a hook reading anno's output must not mistake a truncated stream for
// a complete one.
func (a App) say(line string) error {
	_, err := io.WriteString(a.Stdout, line+"\n")
	return err
}

// emit writes a span verbatim: no dedent, no trimming, original line endings.
//
// Emitting the span exactly makes `read` and `write` inverses of each other,
// which is what lets a caller read a region, transform it, and write it back
// without the tool silently reshaping anything in between. The one addition is
// a final newline where the file lacks one, so output composes with other tools.
func (a App) emit(f source.File, span tree.Range, whole bool) error {
	start, end := span.Start(), span.End()
	if whole {
		start, end = 1, f.Count()
	}
	if end > f.Count() {
		return fault.Internal{Where: "cli.emit", Detail: fmt.Sprintf("span ends at %d in a %d line file", end, f.Count())}
	}
	if end < start {
		return nil
	}
	data, err := f.Slice(start, end)
	if err != nil {
		return err
	}
	if _, err := a.Stdout.Write(data); err != nil {
		return err
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		_, err = io.WriteString(a.Stdout, "\n")
	}
	return err
}

// skip reports a file that could not be included, without failing the command.
// A diagnostic that cannot be written has nowhere left to be reported, so the
// write's error is deliberately dropped here and only here.
func (a App) skip(path string, err error) {
	_, _ = fmt.Fprintf(a.Stderr, "%s %s %s: %v\n",
		a.Err.Paint("anno:", style.Alarm), a.Err.Paint("skipping", style.Quiet), path, err)
}

// load reads and parses one file.
func load(path string) (tree.Tree, error) {
	f, err := source.Load(path)
	if err != nil {
		return tree.Tree{}, err
	}
	return tree.Build(f)
}

// listDir returns the regular files directly inside dir, in a stable order.
func listDir(dir string) ([]string, error) {
	if dir == "" {
		return nil, fault.Usage{Reason: "empty directory path"}
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fault.IO{Op: "stat", Path: dir, Err: err}
	}
	if !info.IsDir() {
		return nil, fault.IO{Op: "list", Path: dir, Err: fmt.Errorf("not a directory")}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fault.IO{Op: "list", Path: dir, Err: err}
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	slices.Sort(out)
	return out, nil
}

// locate chooses which reading of a target string to use.
//
// A path may legitimately contain resolver characters, so the split cannot be
// decided by syntax alone. The candidates are ordered most-path-first and the
// first one whose path exists with the right kind wins; if none exists, the
// reading with the shortest path gives the clearest error, since that is the
// one the user almost certainly meant.
func locate(raw string, wantDir bool) (target.Target, error) {
	candidates, err := target.Parse(raw)
	if err != nil {
		return target.Target{}, err
	}
	for _, c := range candidates {
		info, err := os.Stat(c.Path())
		if err != nil || info.IsDir() != wantDir {
			continue
		}
		return c, nil
	}

	fallback, err := target.ParseOne(raw)
	if err != nil {
		return target.Target{}, err
	}
	want, found := "file", "directory"
	if wantDir {
		want, found = "directory", "file"
	}
	if _, statErr := os.Stat(fallback.Path()); statErr == nil {
		return target.Target{}, fault.IO{
			Op:   "read",
			Path: fallback.Path(),
			Err:  fmt.Errorf("is a %s, but this command needs a %s", found, want),
		}
	}
	return target.Target{}, fault.IO{Op: "open", Path: fallback.Path(), Err: fmt.Errorf("no such %s", want)}
}

// unique resolves a chain to exactly one annotation, or explains why it could
// not. It never picks a winner among several matches.
func unique(t tree.Tree, tgt target.Target) (target.Match, error) {
	matches, err := target.Resolve(t, tgt.Steps())
	if err != nil {
		return target.Match{}, err
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return target.Match{}, fault.NotFound{Target: tgt.Raw(), Near: trim(target.Near(t, tgt.Steps()))}
	default:
		candidates := make([]string, 0, len(matches))
		for _, m := range matches {
			candidates = append(candidates, m.Qualified())
		}
		return target.Match{}, fault.Ambiguous{Target: tgt.Raw(), Candidates: candidates}
	}
}

// trim caps a suggestion list so an error stays readable.
func trim(items []string) []string {
	const limit = 10
	items = slices.Clone(items)
	if len(items) > limit {
		return append(items[:limit:limit], fmt.Sprintf("… and %d more", len(items)-limit))
	}
	return items
}
