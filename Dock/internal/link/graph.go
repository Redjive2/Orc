package link

import (
	"path"
	"path/filepath"
	"sort"
	"strings"

	"orc/dock/internal/doc"
	"orc/dock/internal/target"
)

// Document is one loaded document offered to a graph: its path, its sections,
// and the edges its links declare.
//
// The graph does no I/O. The command layer walks a tree and loads it; this
// package only resolves what it is handed, which is what keeps the resolution
// rules — most-path-first, same-file, by number or by name — testable without a
// filesystem.
type Document struct {
	Path  string
	Doc   doc.Doc
	Edges []Edge
}

// State is what became of an edge when the graph tried to resolve it.
type State int

const (
	// Resolved means the edge points at a section that exists.
	Resolved State = iota
	// Dangling means it points at a document or section that does not.
	Dangling
	// Unchecked means resolving it needs a tool the graph does not have —
	// today, an Anno chain, which only anno can answer. Reporting one as
	// dangling would send someone to fix a document that is correct.
	Unchecked
)

// String implements fmt.Stringer.
func (s State) String() string {
	switch s {
	case Resolved:
		return "resolved"
	case Dangling:
		return "dangling"
	case Unchecked:
		return "unchecked"
	default:
		return "unknown"
	}
}

// Node is a resolved endpoint: one section of one document. A Number of Root
// means the document itself.
type Node struct {
	Path   string
	Number string
}

// String renders the node as a target, so a line of output can be pasted into
// a command.
func (n Node) String() string {
	if n.Number == Root {
		return n.Path
	}
	return n.Path + doc.Sigil + n.Number
}

// Arrow is one edge after resolution.
type Arrow struct {
	// From is the section the link sits in.
	From Node
	// To is what it resolves to, meaningful only when State is Resolved.
	To Node
	// State is what became of it.
	State State
	// Edge is the link itself, carrying its label and position.
	Edge Edge
	// Why explains a Dangling or Unchecked arrow in one phrase.
	Why string
}

// Graph is a frozen view of a doc set's links.
type Graph struct {
	out    map[Node][]Arrow
	in     map[Node][]Arrow
	all    []Arrow
	docs   map[string]doc.Doc
	faults []Arrow
}

// Build resolves every edge in a doc set.
//
// Paths are resolved relative to the linking document, as markdown links
// already are, and cleaned so that ./a.md and a.md are one node rather than
// two. Where a destination has several readings the first whose *document* is
// in the corpus wins — the same most-path-first rule read uses, decided by what
// exists rather than by syntax.
func Build(docs []Document) Graph {
	g := Graph{
		out:  map[Node][]Arrow{},
		in:   map[Node][]Arrow{},
		docs: map[string]doc.Doc{},
	}
	for _, d := range docs {
		g.docs[clean(d.Path)] = d.Doc
	}

	for _, d := range docs {
		from := clean(d.Path)
		for _, e := range d.Edges {
			a := g.resolve(from, e)
			g.all = append(g.all, a)
			g.out[a.From] = append(g.out[a.From], a)
			if a.State == Resolved {
				g.in[a.To] = append(g.in[a.To], a)
			} else {
				g.faults = append(g.faults, a)
			}
		}
	}
	return g
}

// resolve turns one edge into an arrow.
func (g Graph) resolve(from string, e Edge) Arrow {
	a := Arrow{From: Node{Path: from, Number: e.From()}, Edge: e}

	// Every reading is tried, longest path first. A reading whose document is
	// not in the corpus is not wrong yet — a later one may name a document that
	// is — so the last failure is what gets reported.
	var why string
	for _, tg := range e.Readings() {
		if tg.Kind() == target.Anno {
			a.State, a.Why = Unchecked, "anno resolves this"
			return a
		}
		docPath := clean(from)
		if !tg.SameFile() {
			docPath = clean(path.Join(path.Dir(from), tg.Path()))
		}
		d, ok := g.docs[docPath]
		if !ok {
			why = "no document at " + docPath
			continue
		}
		s, ok := lookup(d, tg)
		if !ok {
			why = tg.String() + " names no section in " + docPath
			continue
		}
		a.To = Node{Path: docPath, Number: s.Number()}
		a.State = Resolved
		return a
	}

	a.State, a.Why = Dangling, why
	if why == "" {
		a.Why = "addresses nothing"
	}
	return a
}

func lookup(d doc.Doc, tg target.Target) (doc.Section, bool) {
	if n := tg.Number(); n != "" {
		return d.ByNumber(n)
	}
	return d.ByName(tg.Name())
}

// clean normalises a path so that ./a.md, a.md, and b/../a.md are one node.
func clean(p string) string {
	return filepath.ToSlash(filepath.Clean(p))
}

// Out returns the arrows leaving a node, in document order.
func (g Graph) Out(n Node) []Arrow { return append([]Arrow(nil), g.out[n]...) }

// In returns the arrows arriving at a node.
//
// Backlinks are computed by scanning, never stored: a stored backlink is a
// second source of truth that goes stale the moment someone edits the other
// document in an ordinary editor.
func (g Graph) In(n Node) []Arrow { return append([]Arrow(nil), g.in[n]...) }

// Counts reports how many links leave and arrive at a node.
func (g Graph) Counts(n Node) (out, in int) { return len(g.out[n]), len(g.in[n]) }

// Arrows returns every arrow in the graph, in document order.
func (g Graph) Arrows() []Arrow { return append([]Arrow(nil), g.all...) }

// Faults returns the arrows that did not resolve — dangling and unchecked both,
// since a report that hid the unchecked ones would overstate what was verified.
func (g Graph) Faults() []Arrow { return append([]Arrow(nil), g.faults...) }

// Dangling returns only the arrows that are genuinely broken.
func (g Graph) Dangling() []Arrow {
	var out []Arrow
	for _, a := range g.faults {
		if a.State == Dangling {
			out = append(out, a)
		}
	}
	return out
}

// Recheck asks a resolver about every arrow the graph could not settle itself,
// and returns a new graph carrying the answers.
//
// It exists so the graph stays a pure function while still benefiting from a
// tool it cannot call. Dock's only such arrows today are Anno chains, and ask
// is the anno boundary; the graph neither knows nor cares which process
// answered. An ask that returns Unchecked leaves the arrow exactly as it was,
// which is what keeps "anno is missing" from turning into "this link is
// broken".
//
// The resolver is handed the whole arrow rather than just its target, because
// a destination is written relative to the document that declares it. Asking a
// tool about "../code/example.go" without saying which document said so
// resolves it against the caller's working directory instead, which is a
// different file or none at all.
func (g Graph) Recheck(ask func(a Arrow) (State, string)) Graph {
	if ask == nil {
		return g
	}
	out := Graph{
		out:  map[Node][]Arrow{},
		in:   map[Node][]Arrow{},
		docs: g.docs,
	}
	for _, a := range g.all {
		if a.State == Unchecked {
			if state, why := ask(a); state != Unchecked {
				a.State = state
				a.Why = why
			}
		}
		out.all = append(out.all, a)
		out.out[a.From] = append(out.out[a.From], a)
		switch a.State {
		case Resolved:
			// A code target resolves to something outside the doc graph, so it
			// gains no node: nothing in the corpus can be linked *to* by it.
			if a.To != (Node{}) {
				out.in[a.To] = append(out.in[a.To], a)
			}
		default:
			out.faults = append(out.faults, a)
		}
	}
	return out
}

// Documents returns the paths in the graph, sorted, so a report over it is
// deterministic.
func (g Graph) Documents() []string {
	out := make([]string, 0, len(g.docs))
	for p := range g.docs {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Rel renders a node's path relative to a base directory, for output that is
// readable without being wrong: the result still parses as a target.
func (n Node) Rel(base string) Node {
	if base == "" {
		return n
	}
	rel, err := filepath.Rel(base, n.Path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return n
	}
	return Node{Path: filepath.ToSlash(rel), Number: n.Number}
}

// Explain replaces the reason on arrows that are already known to be broken,
// without changing what they are.
//
// It is Recheck's counterpart and deliberately narrower: Recheck asks a tool to
// *settle* an arrow the graph could not, while this only improves the wording of
// one it settled itself. The caller has knowledge the graph does not — where the
// doc root is, and therefore whether a destination left it — and a reason is the
// right place to put knowledge that changes nothing about the verdict.
func (g Graph) Explain(why func(a Arrow) string) Graph {
	if why == nil {
		return g
	}
	out := Graph{out: map[Node][]Arrow{}, in: g.in, docs: g.docs}
	for _, a := range g.all {
		if a.State == Dangling {
			if reason := why(a); reason != "" {
				a.Why = reason
			}
		}
		out.all = append(out.all, a)
		out.out[a.From] = append(out.out[a.From], a)
		if a.State != Resolved {
			out.faults = append(out.faults, a)
		}
	}
	return out
}
