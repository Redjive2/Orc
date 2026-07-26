// Package cli is Dock's command layer.
//
// Each command is a method that returns an error; Main turns that error into an
// exit code through the one shared table in common/fault, so a hook branching on
// a status means the same thing whichever Orc tool it called.
//
// Output goes to stdout and diagnostics to stderr, so a caller can pipe one
// without catching the other. Every write is checked: a command that could not
// deliver its output has failed, and reporting success would be a lie a pipeline
// then acts on.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"orc/common/fault"
	"orc/common/source"
	"orc/dock/internal/anno"
	"orc/dock/internal/doc"
	"orc/dock/internal/edit"
	"orc/dock/internal/link"
	"orc/dock/internal/render"
	"orc/dock/internal/root"
	"orc/dock/internal/scan"
	"orc/dock/internal/style"
	"orc/dock/internal/target"
	"orc/theme"
)

// App is one invocation.
type App struct {
	Stdout io.Writer
	Stderr io.Writer
	// Stdin is where content of "-" comes from.
	Stdin io.Reader
	// Out and Err are the palettes for the two streams, decided separately: a
	// piped index stays clean while the diagnostics beside it on a terminal stay
	// legible.
	Out style.Palette
	Err style.Palette
	// Stat reports whether a path exists as a regular file. It is injected so
	// the path/target split can be tested without arranging a filesystem.
	Stat func(string) bool
	// Anno resolves code targets. The zero value is unavailable, which leaves
	// every code link unchecked rather than calling it broken.
	Anno anno.Tool
	// Look reads the environment when --color has to re-resolve the scheme.
	// A nil Look reads the process environment.
	Look theme.Look
}

// escaped reports why a broken link left the doc root, or "" when it did not.
//
// A link out of the tree is still a *broken link* rather than a containment
// breach, so it keeps its Dangling state and check keeps its exit code. Dock
// never exits 11: for a documentation tool an out-of-root link is a mistake in a
// document, and reserving the escape code for Orcprobe — where a path leaving
// the probe really is the thing to alarm on — is what keeps that code worth
// alarming on.
//
// What the reason buys is the difference between "no document at
// ../../../../notes.md" and "that is outside the tree at all", which are the
// same words to a graph and different problems to a person.
func escaped(base string, a link.Arrow) string {
	tg := a.Edge.To()
	if tg.SameFile() || tg.Kind() != target.Section {
		return ""
	}
	dest := filepath.Join(filepath.Dir(a.From.Path), tg.Path())
	if _, err := root.Within(base, dest); err != nil {
		return "escapes the tree at " + base
	}
	return ""
}

// codeTarget renders an arrow's destination as a path anno can resolve.
//
// A destination is written relative to the document that declares it, so it is
// rejoined against that document's directory before anno sees it. Handing anno
// the raw text would resolve it against dock's working directory, which is a
// different file or none at all.
func codeTarget(a link.Arrow) string {
	t := a.Edge.To()
	if t.Path() == "" {
		return t.Chain()
	}
	return filepath.Join(filepath.Dir(a.From.Path), t.Path()) + t.Chain()
}

// verdict translates anno's answer into the graph's vocabulary.
//
// An ambiguous target is dangling rather than unchecked: a link that names more
// than one annotation does not address one thing, which is as broken for a
// reader following it as naming none.
func verdict(r anno.Result) (link.State, string) {
	switch r.Verdict {
	case anno.Exists:
		return link.Resolved, ""
	case anno.Missing:
		return link.Dangling, r.Why
	case anno.Ambiguous:
		why := r.Why
		if len(r.Candidates) > 0 {
			why += " (" + strings.Join(r.Candidates, ", ") + ")"
		}
		return link.Dangling, why
	default:
		return link.Unchecked, r.Why
	}
}

// Colour flags. They are global rather than per-command, and are taken off the
// line before dispatch, so `dock --no-color index x.md` and
// `dock index x.md --no-color` both work and no command has to know they exist.
//
// The environment (NO_COLOR, ORC_AGENT, ORC_THEME=none) already turns colour
// off, but an environment variable is awkward for a caller assembling one
// command — which is what Orc will be doing — so there is a flag for the same
// thing. --color forces it on for the opposite case: a caller that pipes dock
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

// colour applies the flags to the palettes.
//
// --no-color always wins: it is the side that can say no, and the shared scheme
// resolves every "no" before any "yes". --color re-resolves as though both
// streams were terminals, which honours the flavour in ORC_THEME and still
// leaves ORC_AGENT absolute — an agent's output is an input to another program,
// and escape sequences in it are corruption rather than decoration.
func (a App) colour(force, off bool) App {
	switch {
	case off:
		a.Out, a.Err = style.Plain(), style.Plain()
	case force:
		look := a.Look
		if look == nil {
			look = os.LookupEnv
		}
		if cfg, err := theme.Resolve(look, true); err == nil {
			a.Out, a.Err = style.New(cfg.Palette), style.New(cfg.Palette)
		}
	}
	return a
}

// New builds an app over the real streams and filesystem, with one palette for
// both streams. Tests use it; main decides the two separately.
func New(stdout, stderr io.Writer, pal style.Palette) App {
	return App{
		Stdout: stdout, Stderr: stderr, Stdin: os.Stdin,
		Out: pal, Err: pal,
		Stat: regular, Anno: anno.New(),
	}
}

// Regular reports whether a path exists as a regular file. It is the default
// Stat, exported so main can build an App without New.
func Regular(path string) bool { return regular(path) }

func regular(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func (a App) validate() error {
	const where = "cli.App"
	if a.Stdout == nil || a.Stderr == nil {
		return fault.Internal{Where: where, Detail: "output streams are not set"}
	}
	return fault.Check(a.Stat != nil, where, "no stat function")
}

// Main runs one command and returns the process exit code.
//
// A panic anywhere below becomes an Internal fault rather than a crash: a tool
// an agent runs on every read should fail with a diagnosis, not a stack trace.
func (a App) Main(args []string) (code int) {
	defer func() {
		if p := recover(); p != nil {
			a.report(fault.Internal{Where: "cli.Main", Detail: fmt.Sprint(p)})
			code = fault.CodeInternal
		}
	}()

	if err := a.validate(); err != nil {
		a.report(err)
		return fault.Code(err)
	}

	args, force, off := takeColourFlags(args)
	a = a.colour(force, off)

	if err := a.dispatch(args); err != nil {
		var q quiet
		if !errors.As(err, &q) {
			a.report(err)
		}
		code := fault.Code(err)
		// The full screen no longer follows every usage error: a refusal that has
		// already said what was wrong reads better without the target syntax under
		// it, and the refusals that need one point at `dock help` themselves.
		//
		// `dock` on its own is the exception, and the only one: nothing was named,
		// so there is no refusal to read and the useful answer is what the verbs
		// are.
		if code == fault.CodeUsage && len(args) == 0 {
			_, _ = fmt.Fprintln(a.Stderr, "\n"+brief(a.Err))
		}
		return code
	}
	return fault.CodeOK
}

func (a App) dispatch(args []string) error {
	if len(args) == 0 {
		return fault.Usage{Reason: "no command given"}
	}
	command, rest := args[0], args[1:]
	switch command {
	case "index":
		return a.index(rest)
	case "read":
		return a.read(rest)
	case "overview":
		return a.overview(rest)
	case "find":
		return a.find(rest)
	case "write":
		return a.write(rest)
	case "links":
		return a.links(rest)
	case "check":
		return a.check(rest)
	case "help", "-h", "--help":
		return a.help()
	default:
		return unknown(command)
	}
}

// report writes a diagnostic. Its own failure is discarded because stderr is
// where a stderr failure would have to be reported.
func (a App) report(err error) {
	if err == nil {
		return
	}
	_, _ = fmt.Fprintf(a.Stderr, "%s %s\n", a.Err.Paint("dock:", style.Alarm), err)
}

// say writes to stdout and returns the write's error, so a closed pipe fails
// the command rather than being dropped.
func (a App) say(s string) error {
	if _, err := io.WriteString(a.Stdout, s); err != nil {
		return fault.IO{Op: "write", Path: "stdout", Err: err}
	}
	return nil
}

func (a App) help() error {
	return a.say(usage(a.Out))
}

// loaded is a document read from disk: its bytes, its lines, and its sections.
type loaded struct {
	file source.File
	scan scan.Result
	doc  doc.Doc
}

// load reads and parses one document.
func (a App) load(path string) (loaded, error) {
	f, err := source.Load(path)
	if err != nil {
		return loaded{}, err
	}
	r := scan.Scan(string(f.Bytes()))
	d, err := doc.Build(path, r)
	if err != nil {
		return loaded{}, err
	}
	return loaded{file: f, scan: r, doc: d}, nil
}

// index prints a document's table.
func (a App) index(args []string) error {
	rest, asJSON, err := flag(args, "--json")
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fault.Usage{Reason: "usage: dock index <file> [--json]"}
	}
	args = rest
	l, err := a.load(args[0])
	if err != nil {
		return err
	}

	// A malformed destination is worth reporting when a single document was
	// asked for, where overview only notes it: the caller named this file.
	if _, err := link.Edges(l.doc, l.scan); err != nil {
		return err
	}
	ix, err := render.BuildIndex(l.doc, l.scan.Lines(), a.counts(l, args[0]))
	if err != nil {
		return err
	}
	if asJSON {
		return a.emitJSON(indexJSON(args[0], ix))
	}
	return a.say(render.Table(ix, a.Out))
}

// overview prints the sections of every document under a directory.
//
// A file that will not load — a binary, a non-UTF-8 file, one too large, one
// the process may not read — is skipped with a note on stderr, never a hard
// failure. One bad corner of a tree must not cost the whole overview, and a
// tree of source code is mostly files that are not documents.
//
// A file that loads but declares no sections is skipped silently. It is not a
// document, and saying so for every file in a repository would bury the ones
// that are.
func (a App) overview(args []string) error {
	rest, asJSON, err := flag(args, "--json")
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fault.Usage{Reason: "usage: dock overview <dir> [--json]"}
	}
	dir := rest[0]

	paths, err := root.Walk(dir)
	if err != nil {
		return err
	}

	shown := 0
	docs := []jsonDoc{}
	for _, path := range paths {
		l, err := a.load(path)
		if err != nil {
			a.note(path, err)
			continue
		}
		if l.doc.Len() == 0 {
			continue
		}
		ix, err := render.BuildIndex(l.doc, l.scan.Lines(), a.outbound(l))
		if err != nil {
			return err
		}
		if asJSON {
			docs = append(docs, indexJSON(path, ix))
			shown++
			continue
		}
		if shown > 0 {
			if err := a.say("\n"); err != nil {
				return err
			}
		}
		if err := a.say(render.Table(ix, a.Out)); err != nil {
			return err
		}
		shown++
	}

	// An empty tree is an empty array under --json, not a failure: a caller
	// mirroring a repository should get "no documents here" as data rather than
	// as an error it has to special-case.
	if asJSON {
		return a.emitJSON(docs)
	}
	if shown == 0 {
		return fault.NotFound{Target: dir, Near: []string{"no document under it carries a " + doc.Sigil + " heading"}}
	}
	return nil
}

// find resolves a section across a tree, printing every match.
//
// Unlike read it does not narrow: a name may legitimately appear in several
// documents, and reporting them all is what makes find worth having. Each match
// is headed by a target that can be pasted straight into read.
func (a App) find(args []string) error {
	rest, tree, err := flag(args, "--tree")
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fault.Usage{Reason: "usage: dock find <dir>" + doc.Sigil + "<ref> [--tree]"}
	}

	readings, ok, err := target.Parse(rest[0])
	if err != nil {
		return err
	}
	if !ok || readings[0].Kind() != target.Section {
		return fault.Usage{Reason: fmt.Sprintf(
			"%q addresses no section; try a directory and an address, as in docs%s1.2", rest[0], doc.Sigil)}
	}

	// The directory is whichever reading names one, longest first — the same
	// most-path-first rule read uses, decided by what exists.
	var chosen target.Target
	for _, tg := range readings {
		if tg.Path() != "" && a.isDir(tg.Path()) {
			chosen = tg
			break
		}
	}
	if chosen.Path() == "" {
		return fault.NotFound{Target: rest[0], Near: []string{"no directory at " + readings[0].Path()}}
	}

	paths, err := root.Walk(chosen.Path())
	if err != nil {
		return err
	}

	matches := 0
	for _, path := range paths {
		l, err := a.load(path)
		if err != nil {
			a.note(path, err)
			continue
		}
		s, found := lookup(l.doc, chosen)
		if !found {
			continue
		}
		matches++

		span := s.Own()
		if tree {
			span = s.Tree()
		}
		header := fmt.Sprintf("%s%s%s   %s\n", path, doc.Sigil, s.Number(), s.Name())
		if err := a.say(a.Out.Paint(header, style.Number)); err != nil {
			return err
		}
		if span.Empty() {
			continue
		}
		text, err := l.file.Slice(span.Start(), span.End())
		if err != nil {
			return err
		}
		if err := a.say(string(text)); err != nil {
			return err
		}
	}

	if matches == 0 {
		return fault.NotFound{Target: rest[0], Near: []string{
			fmt.Sprintf("no document under %s declares it", chosen.Path())}}
	}
	return nil
}

// write replaces a section's content.
//
// It takes the same --tree flag as read and means the same span by it, so
// whatever read returned is what write replaces. Content of "-" is read from
// stdin, which is the practical path for anything multi-line and the one the
// hooks use.
func (a App) write(args []string) error {
	rest, tree, err := flag(args, "--tree")
	if err != nil {
		return err
	}
	if len(rest) != 2 {
		return fault.Usage{Reason: "usage: dock write <target> <content> [--tree]   (content of - reads stdin)"}
	}

	readings, ok, err := target.Parse(rest[0])
	if err != nil {
		return err
	}
	if !ok {
		return fault.Usage{Reason: fmt.Sprintf(
			"%q addresses no section; a target is a path and an address, as in guide.md%s1.2", rest[0], doc.Sigil)}
	}
	if readings[0].Kind() == target.Anno {
		return fault.Usage{Reason: fmt.Sprintf(
			"%s addresses code; write it with `anno write`", rest[0])}
	}

	content := rest[1]
	if content == "-" {
		if a.Stdin == nil {
			return fault.Usage{Reason: "content of - was given but there is no standard input"}
		}
		read, err := io.ReadAll(a.Stdin)
		if err != nil {
			return fault.IO{Op: "read", Path: "stdin", Err: err}
		}
		content = string(read)
	}

	tg, err := a.pick(rest[0], readings)
	if err != nil {
		return err
	}
	l, err := a.load(tg.Path())
	if err != nil {
		return err
	}
	s, found := lookup(l.doc, tg)
	if !found {
		return notFound(l.doc, tg)
	}

	plan, err := edit.Prepare(l.file, l.doc, s, tree, content)
	if err != nil {
		return err
	}
	if err := edit.Commit(plan); err != nil {
		return err
	}
	// The summary goes to stderr: stdout is for what a command was asked to
	// produce, and write was asked to change a file, not to say so.
	_, _ = fmt.Fprintf(a.Stderr, "%s %s\n",
		a.Err.Paint("dock:", style.Good), a.Err.Paint(plan.Summary(), style.Quiet))
	return nil
}

// corpus loads every document under a root and resolves their links.
//
// Documents that will not load are returned as faults rather than thrown away:
// overview skips them with a note because it is showing what it can, while
// check reports them because being unreadable is exactly the kind of thing it
// exists to find.
func (a App) corpus(dir string) (link.Graph, []error, error) {
	paths, err := root.Walk(dir)
	if err != nil {
		return link.Graph{}, nil, err
	}

	var (
		docs   []link.Document
		faults []error
	)
	for _, path := range paths {
		l, err := a.load(path)
		if err != nil {
			faults = append(faults, err)
			continue
		}
		edges, err := link.Edges(l.doc, l.scan)
		if err != nil {
			faults = append(faults, err)
			continue
		}
		docs = append(docs, link.Document{Path: path, Doc: l.doc, Edges: edges})
	}
	return link.Build(docs), faults, nil
}

// links shows what a section points at and what points at it.
func (a App) links(args []string) error {
	if len(args) != 1 {
		return fault.Usage{Reason: "usage: dock links <target>"}
	}

	readings, ok, err := target.Parse(args[0])
	if err != nil {
		return err
	}
	if !ok {
		return fault.Usage{Reason: fmt.Sprintf(
			"%q addresses no section; a target is a path and an address, as in guide.md%s1.2", args[0], doc.Sigil)}
	}
	if readings[0].Kind() == target.Anno {
		return fault.Usage{Reason: fmt.Sprintf("%s addresses code; dock does not graph it", args[0])}
	}

	tg, err := a.pick(args[0], readings)
	if err != nil {
		return err
	}
	l, err := a.load(tg.Path())
	if err != nil {
		return err
	}
	s, found := lookup(l.doc, tg)
	if !found {
		return notFound(l.doc, tg)
	}

	// Backlinks need the whole corpus, so the root is found from the document
	// rather than asked for: an agent naming one section should not also have
	// to know where the tree begins.
	base, err := root.Find(tg.Path())
	if err != nil {
		return err
	}
	g, faults, err := a.corpus(base)
	if err != nil {
		return err
	}
	for _, f := range faults {
		a.note(base, f)
	}

	at := link.Node{Path: filepath.ToSlash(filepath.Clean(tg.Path())), Number: s.Number()}
	return a.say(render.Links(base, at, s.Name(), g.Out(at), g.In(at), a.Out))
}

// check reports every link in a tree that does not resolve.
func (a App) check(args []string) error {
	dir := "."
	switch len(args) {
	case 0:
	case 1:
		dir = args[0]
	default:
		return fault.Usage{Reason: "usage: dock check [<dir>]"}
	}

	g, faults, err := a.corpus(dir)
	if err != nil {
		return err
	}

	// Say which broken links left the tree, which the graph cannot know: it has
	// no filesystem and no notion of where the corpus begins.
	if base, err := root.Find(dir); err == nil {
		g = g.Explain(func(ar link.Arrow) string { return escaped(base, ar) })
	}

	// Ask anno about the code targets. Without it they stay unchecked, and the
	// summary says so rather than counting them as verified.
	tool := a.Anno
	if tool.Available() {
		g = g.Recheck(func(ar link.Arrow) (link.State, string) {
			return verdict(tool.Check(codeTarget(ar)))
		})
	}

	if err := a.say(render.Check(dir, g, faults, a.Out)); err != nil {
		return err
	}

	// Exit 2 when anything is broken, so a hook or a CI step can branch on the
	// status without parsing the report — but say nothing more. The report is
	// on stdout and has already named every fault with its position; a
	// diagnostic after it would only repeat itself less usefully.
	if len(g.Dangling()) > 0 || len(faults) > 0 {
		return quiet{fault.NotFound{Target: dir}}
	}
	return nil
}

// quiet carries an exit code without printing a diagnostic, for a command that
// has already reported everything it has to say.
type quiet struct{ err error }

func (q quiet) Error() string { return q.err.Error() }
func (q quiet) Unwrap() error { return q.err }

// MaxBacklinkScan bounds how large a tree index will walk to count backlinks.
//
// Inbound counts need the whole corpus read, and an index of one file in a
// large repository should not pay for that. Past the bound the count is left
// unknown and the table prints "?" — which is honest, where a zero nobody
// measured would not be.
const MaxBacklinkScan = 250

// counts assembles a document's link counts: outbound from the document itself,
// inbound from the corpus when the corpus is small enough to read.
func (a App) counts(l loaded, path string) map[string]render.Counts {
	counts := a.outbound(l)

	base, err := root.Find(path)
	if err != nil {
		return counts
	}
	paths, err := root.Walk(base)
	if err != nil || len(paths) > MaxBacklinkScan {
		return counts
	}
	g, _, err := a.corpus(base)
	if err != nil {
		return counts
	}

	self := filepath.ToSlash(filepath.Clean(path))
	for number, c := range counts {
		_, in := g.Counts(link.Node{Path: self, Number: number})
		c.In = in
		counts[number] = c
	}
	return counts
}

// outbound counts the links each document's sections declare. A document's
// own links are all it can know; inbound needs the corpus walked.
func (a App) outbound(l loaded) map[string]render.Counts {
	counts := map[string]render.Counts{}
	for _, s := range l.doc.Sections() {
		counts[s.Number()] = render.Counts{In: render.Unknown}
	}
	edges, err := link.Edges(l.doc, l.scan)
	if err != nil {
		// A malformed destination is a fault of the document, not of the
		// index; the counts are simply unknown for it.
		return counts
	}
	for _, e := range edges {
		if e.From() == link.Root {
			continue
		}
		c := counts[e.From()]
		c.Out++
		counts[e.From()] = c
	}
	return counts
}

// note reports a skipped file without failing the command.
func (a App) note(path string, err error) {
	_, _ = fmt.Fprintf(a.Stderr, "%s %s %s: %s\n",
		a.Err.Paint("dock:", style.Alarm), a.Err.Paint("skipped", style.Quiet), path, err)
}

func (a App) isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// read prints one section's content, verbatim.
//
// With --follow it also prints the sections that one links to, to the given
// depth, each under a header naming it. The starting section stays bare: it is
// what the caller asked for, and prefixing it would spend a line telling them
// the name they just typed.
func (a App) read(args []string) error {
	o, err := parseOpts(args)
	if err != nil {
		return err
	}
	if len(o.rest) != 1 {
		return fault.Usage{Reason: "usage: dock read <target> [--tree] [--follow[=n]] [--budget=lines]"}
	}
	if o.budget > 0 && o.follow == 0 {
		return fault.Usage{Reason: "--budget bounds what --follow adds; on its own it would only truncate the section you asked for"}
	}

	readings, ok, err := target.Parse(o.rest[0])
	if err != nil {
		return err
	}
	if !ok {
		return fault.Usage{Reason: fmt.Sprintf(
			"%q addresses no section; a target is a path and an address, as in guide.md%s1.2", o.rest[0], doc.Sigil)}
	}
	// An Anno chain is refused before any path is resolved. Whether the file
	// happens to exist is beside the point: dock read is the wrong command for
	// it either way, and saying "no such file" would send the caller to fix the
	// wrong thing.
	if readings[0].Kind() == target.Anno {
		return fault.Usage{Reason: fmt.Sprintf(
			"%s addresses code, not a document; read it with `anno read %s`", o.rest[0], o.rest[0])}
	}

	tg, err := a.pick(o.rest[0], readings)
	if err != nil {
		return err
	}
	l, err := a.load(tg.Path())
	if err != nil {
		return err
	}
	s, found := lookup(l.doc, tg)
	if !found {
		return notFound(l.doc, tg)
	}

	text, err := span(l, s, o.tree)
	if err != nil {
		return err
	}
	if err := a.say(text); err != nil {
		return err
	}
	if o.follow == 0 {
		return nil
	}
	return a.follow(tg, s, o, countLines(text))
}

// span returns a section's content, verbatim.
func span(l loaded, s doc.Section, tree bool) (string, error) {
	r := s.Own()
	if tree {
		r = s.Tree()
	}
	if r.Empty() {
		return "", nil
	}
	raw, err := l.file.Slice(r.Start(), r.End())
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func countLines(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

// follow walks outward from a section, printing what it links to.
//
// Breadth first, so the nearest citations come first and a depth limit means
// what it says. Two rules keep it from becoming "print the corpus":
//
//   - a section is emitted at most once, however many paths reach it, and
//   - nothing is emitted that would take the output past --budget.
//
// Both are announced rather than silent. A reader who cannot tell the difference
// between "there was nothing more" and "there was more and you did not get it"
// has been misled by the tool.
func (a App) follow(from target.Target, start doc.Section, o opts, spent int) error {
	base, err := root.Find(from.Path())
	if err != nil {
		return err
	}
	g, faults, err := a.corpus(base)
	if err != nil {
		return err
	}
	for _, f := range faults {
		a.note(base, f)
	}

	type step struct {
		node  link.Node
		depth int
	}
	origin := link.Node{Path: filepath.ToSlash(filepath.Clean(from.Path())), Number: start.Number()}
	seen := map[link.Node]bool{origin: true}
	code := map[string]bool{}
	queue := []step{{node: origin, depth: 0}}

	cache := map[string]loaded{}
	load := func(path string) (loaded, error) {
		if got, ok := cache[path]; ok {
			return got, nil
		}
		got, err := a.load(path)
		if err == nil {
			cache[path] = got
		}
		return got, err
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= o.follow {
			continue
		}

		for _, arrow := range g.Out(cur.node) {
			switch {
			case arrow.State == link.Resolved && arrow.To != (link.Node{}):
				if seen[arrow.To] {
					if err := a.say(render.FollowNote(
						arrow.To.Rel(base).String()+" is shown above", a.Out)); err != nil {
						return err
					}
					continue
				}

				l, err := load(arrow.To.Path)
				if err != nil {
					a.note(arrow.To.Path, err)
					seen[arrow.To] = true
					continue
				}
				s, ok := l.doc.ByNumber(arrow.To.Number)
				if !ok {
					seen[arrow.To] = true
					continue
				}
				text, err := span(l, s, o.tree)
				if err != nil {
					return err
				}

				n := countLines(text)
				if o.budget > 0 && spent+n > o.budget {
					if err := a.say(render.FollowNote(fmt.Sprintf(
						"omitted %s (%d lines, %d over budget); read it with `dock read %s`",
						arrow.To.Rel(base), n, spent+n-o.budget, arrow.To.Rel(base)), a.Out)); err != nil {
						return err
					}
					seen[arrow.To] = true
					continue
				}

				seen[arrow.To] = true
				spent += n
				if err := a.say(render.FollowHeader(base, arrow.To, s.Name(), a.Out)); err != nil {
					return err
				}
				if err := a.say(text); err != nil {
					return err
				}
				queue = append(queue, step{node: arrow.To, depth: cur.depth + 1})

			case arrow.Edge.To().Kind() == target.Anno && a.Anno.Available():
				// A code target's content comes from anno, which is the only
				// thing that can read it. Depth still applies, and a chain seen
				// once is not read twice.
				chain := codeTarget(arrow)
				if code[chain] {
					continue
				}
				code[chain] = true
				got := a.Anno.Read(chain)
				if got.Verdict != anno.Exists {
					continue
				}
				n := countLines(got.Content)
				if o.budget > 0 && spent+n > o.budget {
					if err := a.say(render.FollowNote(fmt.Sprintf(
						"omitted %s (%d lines); read it with `anno read %s`",
						arrow.Edge.To(), n, chain), a.Out)); err != nil {
						return err
					}
					continue
				}
				spent += n
				if err := a.say(render.FollowCodeHeader(arrow.Edge.To().String(), a.Out)); err != nil {
					return err
				}
				if err := a.say(got.Content); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// pick chooses the reading whose path exists.
//
// The readings are ordered most-path-first, so a file genuinely named like a
// target wins over reading it as one — the conservative choice, and Anno's.
func (a App) pick(raw string, readings []target.Target) (target.Target, error) {
	for _, tg := range readings {
		if tg.Path() == "" {
			continue // a same-file reference has no document to open
		}
		if a.Stat(tg.Path()) {
			return tg, nil
		}
	}
	for _, tg := range readings {
		if tg.Path() == "" {
			return target.Target{}, fault.Usage{Reason: fmt.Sprintf(
				"%s names a section but no document; say which file, as in guide.md%s", raw, raw)}
		}
	}
	// Quote the most target-like reading: with nothing on disk to decide it,
	// that is almost certainly what was meant.
	last := readings[len(readings)-1]
	return target.Target{}, fault.NotFound{Target: raw, Near: []string{
		fmt.Sprintf("no file at %s", last.Path()),
	}}
}

func lookup(d doc.Doc, tg target.Target) (doc.Section, bool) {
	if n := tg.Number(); n != "" {
		return d.ByNumber(n)
	}
	return d.ByName(tg.Name())
}

// notFound explains a target that named a document Dock could read but a
// section it does not have, listing what it does have. Every listed line is a
// valid target, so the fix is a copy-paste.
func notFound(d doc.Doc, tg target.Target) error {
	near := make([]string, 0, d.Len())
	for _, s := range d.Sections() {
		near = append(near, fmt.Sprintf("%s%s%s   %s", tg.Path(), doc.Sigil, s.Number(), s.Name()))
	}
	if len(near) == 0 {
		near = append(near, d.Path()+" declares no sections")
	}
	return fault.NotFound{Target: tg.String(), Near: near}
}

// DefaultFollow is the depth --follow uses when given no number. One hop is the
// common case — "this section and what it cites" — and the transitive closure of
// a doc graph is the whole doc set, so the default is deliberately shallow.
const DefaultFollow = 1

// MaxFollow bounds --follow. Past this the answer is "read the documents".
const MaxFollow = 8

// opts are read's and write's flags.
type opts struct {
	rest   []string
	tree   bool
	follow int // 0 means links are not followed
	budget int // 0 means no budget
}

// parseOpts reads the flags read and write share.
//
// An unknown flag is an error rather than a filename: silently treating
// "--tre" as a path would produce "no such file" and send the caller looking
// in the wrong place.
func parseOpts(args []string) (opts, error) {
	var o opts
	for _, arg := range args {
		switch {
		case arg == "--tree":
			o.tree = true
		case arg == "--follow":
			o.follow = DefaultFollow
		case strings.HasPrefix(arg, "--follow="):
			n, err := number(arg, "--follow=")
			if err != nil {
				return opts{}, err
			}
			if n < 1 || n > MaxFollow {
				return opts{}, fault.Usage{Reason: fmt.Sprintf(
					"--follow takes a depth from 1 to %d; past that, read the documents", MaxFollow)}
			}
			o.follow = n
		case strings.HasPrefix(arg, "--budget="):
			n, err := number(arg, "--budget=")
			if err != nil {
				return opts{}, err
			}
			if n < 1 {
				return opts{}, fault.Usage{Reason: "--budget takes a line count of 1 or more"}
			}
			o.budget = n
		case strings.HasPrefix(arg, "-") && arg != "-":
			return opts{}, fault.Usage{Reason: fmt.Sprintf("unknown flag %q", arg)}
		default:
			o.rest = append(o.rest, arg)
		}
	}
	return o, nil
}

func number(arg, prefix string) (int, error) {
	n, err := strconv.Atoi(strings.TrimPrefix(arg, prefix))
	if err != nil {
		return 0, fault.Usage{Reason: fmt.Sprintf("%q is not a number", arg)}
	}
	return n, nil
}

// flag pulls one boolean flag out of an argument list.
func flag(args []string, name string) (rest []string, found bool, err error) {
	for _, a := range args {
		switch {
		case a == name:
			if found {
				return nil, false, fault.Usage{Reason: "repeated " + name}
			}
			found = true
		case strings.HasPrefix(a, "-") && a != "-":
			return nil, false, fault.Usage{Reason: fmt.Sprintf("unknown flag %q", a)}
		default:
			rest = append(rest, a)
		}
	}
	return rest, found, nil
}

// ErrUsage is re-exported so cmd/dock can tell a usage failure from the rest
// without importing the fault vocabulary itself.
var ErrUsage = fault.ErrUsage

// IsUsage reports whether an error is a usage failure.
func IsUsage(err error) bool { return errors.Is(err, ErrUsage) }
