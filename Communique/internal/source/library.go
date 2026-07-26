package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
)

// Collecting the repository, so it can be read from a browser.
//
// The structure comes from Dock and Anno rather than from a markdown parser or a
// Go parser written here: those two tools own what a section and an annotation
// are, and a second implementation would be a second opinion. The text comes
// from the filesystem, because neither tool exists to be a `cat`.
//
// Everything is carried, not indexed for later fetching. The two sides never
// meet — the server cannot ask the agent for a file — so a browser can only show
// what a sync already brought. That is affordable at this repository's size and
// bounded so it stays honest when it is not.

// Extensions worth carrying. Everything else in a repository is a binary, a
// build artefact, or something nobody reads in a browser, and walking past it
// silently is the difference between a library and a disk dump.
var readable = map[string]bool{
	".go": true, ".md": true, ".js": true, ".css": true, ".html": true,
	".json": true, ".sh": true, ".txt": true, ".toml": true, ".yaml": true, ".yml": true,
}

// Directories never descended into. Their contents are either not source or not
// interesting, and `.git` alone would dwarf everything else.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, ".claude": true,
}

// Library collects the repository under root.
//
// A file that cannot be read is carried with its reason rather than dropped: a
// reader who cannot find something must be able to tell "unreadable" from "not
// there", and an omission says neither.
func (c *CLI) Library(ctx context.Context, root string) (*protocol.Library, error) {
	if root == "" {
		return nil, nil
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fault.IO{Op: "resolve", Subject: root, Err: err}
	}

	paths, err := walk(abs)
	if err != nil {
		return nil, err
	}

	lib := &protocol.Library{Root: filepath.Base(abs), Files: []protocol.File{}}
	sections, note := c.sectionsUnder(ctx, abs)
	if note != "" {
		lib.Notes = append(lib.Notes, note)
	}
	annotations, note := c.annotationsUnder(ctx, abs, paths)
	if note != "" {
		lib.Notes = append(lib.Notes, note)
	}

	var carried int
	var dropped []string
	for _, path := range paths {
		if len(lib.Files) >= protocol.MaxLibraryFiles {
			dropped = append(dropped, path)
			continue
		}
		file := readFile(abs, path)
		file.Sections = sections[file.Path]
		file.Annotations = annotations[file.Path]

		// The budget is spent in path order, so what a reader sees is stable
		// between syncs rather than depending on which files were biggest.
		if carried+len(file.Text) > protocol.MaxLibraryBytes {
			file.Text, file.Skipped = "", "the snapshot ran out of room for it"
		}
		carried += len(file.Text)
		lib.Files = append(lib.Files, file)
	}

	if len(dropped) > 0 {
		lib.Truncated = plural(len(dropped), "file") + " past the limit of " +
			itoa(protocol.MaxLibraryFiles) + " were left out entirely"
	}
	return lib, nil
}

// walk lists the readable files under root, in a stable order.
func walk(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// One unreadable directory is not a reason to abandon the tree.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] || (strings.HasPrefix(d.Name(), ".") && path != root) {
				return fs.SkipDir
			}
			return nil
		}
		if readable[strings.ToLower(filepath.Ext(path))] {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, fault.IO{Op: "walk", Subject: root, Err: err}
	}
	sort.Strings(out)
	return out, nil
}

// readFile reads one file, reporting why it could not rather than omitting it.
func readFile(root, path string) protocol.File {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = filepath.Base(path)
	}
	rel = filepath.ToSlash(rel)

	info, err := os.Stat(path)
	if err != nil {
		return protocol.File{Path: rel, Skipped: "it could not be read"}
	}
	file := protocol.File{Path: rel, Bytes: int(info.Size())}
	if info.Size() > protocol.MaxFileBytes {
		file.Skipped = "it is larger than the " + itoa(protocol.MaxFileBytes/1024) + "K limit for one file"
		return file
	}

	data, err := os.ReadFile(path)
	if err != nil {
		file.Skipped = "it could not be read"
		return file
	}
	if !utf8.Valid(data) {
		// Not text, whatever its extension says. Carrying it would put invalid
		// UTF-8 on a wire that refuses it, failing the whole sync over one file.
		file.Skipped = "it is not text"
		return file
	}
	if i := protocol.ControlRune(string(data)); i >= 0 {
		// The same reasoning, one step further. A file may be valid UTF-8 and
		// still hold a NUL — a fixture, a test corpus, something generated — and
		// the wire refuses control characters. Skipping it costs that one file;
		// carrying it cost the entire mirror, which is how this was found.
		r, _ := utf8.DecodeRune(data[i:])
		file.Skipped = fmt.Sprintf("it holds a control character (%#U at byte %d), so it is not text cq can carry", r, i)
		return file
	}
	file.Text = string(data)
	file.Lines = strings.Count(file.Text, "\n")
	if len(file.Text) > 0 && !strings.HasSuffix(file.Text, "\n") {
		file.Lines++
	}
	return file
}

// sectionsUnder asks Dock for every document's sections, keyed by path.
//
// A failure is not fatal. Dock is one of two lenses on the same tree, and losing
// it should cost the § numbers on the documentation, not the whole library.
func (c *CLI) sectionsUnder(ctx context.Context, root string) (map[string][]protocol.Section, string) {
	out := map[string][]protocol.Section{}

	raw, err := c.run(ctx, c.dock(), "overview", root, "--json")
	if err != nil {
		// Carried into the snapshot as well as logged. The log is on the agent
		// machine and the reader is at a browser somewhere else, so a note only
		// written here is a note they will never see — and the tab would tell
		// them their documents have no sections, which is not what happened.
		c.warn("dock could not read %s, so the documentation has no sections: %v", root, err)
		return out, "`" + c.dock() + "` could not be run on the agent machine, so no document has sections; install it and sync again"
	}
	var docs []wireDoc
	if err := decodeJSON(raw, &docs, c.dock()+" overview"); err != nil {
		c.warn("dock's output could not be read: %v", err)
		return out, "`" + c.dock() + "` produced something this build could not read, so no document has sections"
	}
	for _, d := range docs {
		rel := relative(root, d.Path)
		for _, s := range d.Sections {
			out[rel] = append(out[rel], protocol.Section{
				Number: s.Number, Name: s.Name, Depth: s.Depth,
				Start: s.Start, End: s.End, Lines: s.Lines, Out: s.Out, In: s.In,
			})
		}
	}
	return out, ""
}

// annotationsUnder asks Anno for every annotated file's tree, keyed by path.
//
// Anno's overview reads one directory rather than a tree, so this asks per
// directory. A directory it refuses is skipped: a file that merely *mentions*
// the marker syntax — Anno's own sources do — is not a file with annotations,
// and it must not cost the rest of the tree.
//
// Anno not being installed is a different fact, and it is the one worth saying.
// Skipping it like a refusal would tell a reader that nothing in the tree is
// annotated — and would fork one doomed process per directory to say so.
func (c *CLI) annotationsUnder(ctx context.Context, root string, paths []string) (map[string][]protocol.Annotation, string) {
	out := map[string][]protocol.Annotation{}

	seen := map[string]bool{}
	for _, path := range paths {
		dir := filepath.Dir(path)
		if seen[dir] {
			continue
		}
		seen[dir] = true

		raw, err := c.run(ctx, c.anno(), "overview", dir, "--json")
		if errors.Is(err, exec.ErrNotFound) {
			c.warn("anno is not installed, so nothing carries annotations")
			return out, "`" + c.anno() + "` could not be run on the agent machine, so no file carries annotations; install it and sync again"
		}
		if err != nil {
			continue // nothing annotated here, or Anno would not read it
		}
		var trees []wireTree
		if err := decodeJSON(raw, &trees, c.anno()+" overview"); err != nil {
			continue
		}
		for _, t := range trees {
			if len(t.Nodes) == 0 {
				continue
			}
			out[relative(root, t.Path)] = annotations(t.Nodes)
		}
	}
	return out, ""
}

func annotations(nodes []wireNode) []protocol.Annotation {
	out := make([]protocol.Annotation, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, protocol.Annotation{
			Kind: n.Kind, Name: n.Name, Meta: n.Meta,
			Start: n.Start, End: n.End, Lines: n.Lines,
			ContentStart: n.ContentStart, ContentEnd: n.ContentEnd,
			Children: annotations(n.Children),
		})
	}
	return out
}

// relative turns a path either tool reported into one relative to the root, so
// both lenses key the same file the same way.
func relative(root, path string) string {
	abs := path
	if !filepath.IsAbs(abs) {
		if resolved, err := filepath.Abs(path); err == nil {
			abs = resolved
		}
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

// The shapes Dock and Anno emit under `--json`.

type wireDoc struct {
	Path     string        `json:"path"`
	Lines    int           `json:"lines"`
	Sections []wireSection `json:"sections"`
}

type wireSection struct {
	Number string `json:"number"`
	Name   string `json:"name"`
	Depth  int    `json:"depth"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Lines  int    `json:"lines"`
	Out    int    `json:"out"`
	In     *int   `json:"in"`
}

type wireTree struct {
	Path  string     `json:"path"`
	Lines int        `json:"lines"`
	Nodes []wireNode `json:"nodes"`
}

type wireNode struct {
	Kind         string     `json:"kind"`
	Name         string     `json:"name"`
	Meta         []string   `json:"meta"`
	Start        int        `json:"start"`
	End          int        `json:"end"`
	Lines        int        `json:"lines"`
	ContentStart int        `json:"content_start"`
	ContentEnd   int        `json:"content_end"`
	Children     []wireNode `json:"children"`
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func plural(n int, word string) string {
	if n == 1 {
		return itoa(n) + " " + word
	}
	return itoa(n) + " " + word + "s"
}
