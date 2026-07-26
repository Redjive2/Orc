// Package target parses and resolves annotation addresses.
//
// A target is a path followed by a chain of resolver-qualified steps, as in
// example.go@code:Operate^declarations. A chain may be partial: it matches when
// its steps map onto a subsequence of a node's ancestor path. Resolution always
// collects every match and never picks a winner, because the difference between
// a graceful ambiguity error and a silently wrong answer is exactly that choice.
package target

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"orc/anno/internal/marker"
	"orc/anno/internal/tree"
	"orc/common/fault"
)

// Step is one link in a chain: a kind, named by its resolver character, and an
// annotation name.
type Step struct {
	kind marker.Kind
	name string
}

// NewStep builds a kind-qualified step.
func NewStep(kind marker.Kind, name string) (Step, error) {
	if !kind.Valid() {
		return Step{}, fault.Internal{Where: "target.NewStep", Detail: fmt.Sprintf("invalid kind %d", int(kind))}
	}
	if name == "" {
		return Step{}, fault.Usage{Reason: "annotation name is empty"}
	}
	return Step{kind: kind, name: name}, nil
}

// Kind returns the kind the step selects.
func (s Step) Kind() marker.Kind { return s.kind }

// Name returns the step's annotation name.
func (s Step) Name() string { return s.name }

// String renders the step as it appears in a target.
func (s Step) String() string { return string(s.kind.Resolver()) + s.name }

// Target is a parsed address: a file or directory path plus a chain.
type Target struct {
	path  string
	steps []Step
	raw   string
}

// Path returns the path portion.
func (t Target) Path() string { return t.path }

// Steps returns a copy of the chain.
func (t Target) Steps() []Step { return slices.Clone(t.steps) }

// Raw returns the target exactly as the user wrote it, for error messages.
func (t Target) Raw() string { return t.raw }

// IsFile reports whether the target addresses the file as a whole.
func (t Target) IsFile() bool { return len(t.steps) == 0 }

// Last returns the final step, which is the annotation actually being addressed.
func (t Target) Last() (Step, bool) {
	if len(t.steps) == 0 {
		return Step{}, false
	}
	return t.steps[len(t.steps)-1], true
}

// String renders the target in canonical form.
func (t Target) String() string {
	var b strings.Builder
	b.WriteString(t.path)
	for _, s := range t.steps {
		b.WriteString(s.String())
	}
	return b.String()
}

// WithPath returns a copy of the target rooted at a different path.
func (t Target) WithPath(path string) Target {
	return Target{path: path, steps: slices.Clone(t.steps), raw: t.raw}
}

// Parse splits a target string into every plausible (path, chain) reading,
// most-path-first.
//
// Paths may legitimately contain resolver characters — a Windows drive letter,
// a directory called "a:b" — so a single split cannot be chosen from syntax
// alone. Parse returns the candidates it considers valid and leaves the choice
// to the caller, which can test which paths exist. The first candidate is
// always the whole string read as a bare path, so a caller with no filesystem
// to consult still behaves predictably.
func Parse(s string) ([]Target, error) {
	if s == "" {
		return nil, fault.Usage{Reason: "empty target"}
	}
	if strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) {
		return nil, fault.Usage{Reason: fmt.Sprintf("target %q contains a non-printing character", s)}
	}

	out := []Target{{path: s, raw: s}}

	for i, r := range s {
		if i == 0 || !marker.IsResolver(r) {
			continue
		}
		steps, ok := parseChain(s[i:])
		if !ok {
			continue
		}
		out = append(out, Target{path: s[:i], steps: steps, raw: s})
	}

	// Longest path first: a split that keeps more of the string in the path is
	// the more conservative reading.
	slices.SortStableFunc(out, func(a, b Target) int { return len(b.path) - len(a.path) })
	return out, nil
}

// ParseOne returns the single most plausible reading of s, which is the one
// with the shortest path — the reading that treats as much of the string as
// possible as a chain. Callers that can consult a filesystem should use Parse
// and prefer a candidate whose path exists.
func ParseOne(s string) (Target, error) {
	candidates, err := Parse(s)
	if err != nil {
		return Target{}, err
	}
	// Parse always offers at least the whole string read as a bare path.
	return candidates[len(candidates)-1], nil
}

// parseChain reads a complete chain, which must consume all of s. Step names
// exclude resolver characters, path separators, brackets and whitespace, so a
// chain, once it starts, has exactly one reading.
func parseChain(s string) ([]Step, bool) {
	var steps []Step
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		kind, ok := marker.KindForResolver(r)
		if !ok {
			return nil, false
		}
		s = s[size:]
		end := strings.IndexFunc(s, func(r rune) bool { return marker.IsResolver(r) })
		name := s
		if end >= 0 {
			name, s = s[:end], s[end:]
		} else {
			s = ""
		}
		if !validName(name) {
			return nil, false
		}
		steps = append(steps, Step{kind: kind, name: name})
	}
	return steps, len(steps) > 0
}

func validName(name string) bool {
	if name == "" {
		return false
	}
	return !strings.ContainsFunc(name, func(r rune) bool {
		return unicode.IsSpace(r) || !unicode.IsPrint(r) ||
			r == '/' || r == '\\' || r == '[' || r == ']' || marker.IsResolver(r)
	})
}

// Match is one node found by a chain, together with the ancestor path that
// reached it, which is what lets an error message quote a fully qualified
// address.
type Match struct {
	path []tree.Node
	file string
}

// Node returns the matched annotation.
func (m Match) Node() (tree.Node, error) {
	if len(m.path) == 0 {
		return tree.Node{}, fault.Internal{Where: "target.Match.Node", Detail: "match has an empty ancestor path"}
	}
	return m.path[len(m.path)-1], nil
}

// File returns the path of the file the match was found in.
func (m Match) File() string { return m.file }

// Path returns a copy of the ancestor chain that reached the match, outermost
// annotation first and the matched node last.
func (m Match) Path() []tree.Node { return slices.Clone(m.path) }

// Qualified renders the match as a fully qualified target: every ancestor named
// with its own resolver, so the result is unambiguous by construction.
func (m Match) Qualified() string {
	var b strings.Builder
	b.WriteString(m.file)
	for _, n := range m.path {
		b.WriteRune(n.Kind().Resolver())
		b.WriteString(n.Name())
	}
	return b.String()
}

// Depth returns how many levels deep the matched node sits.
func (m Match) Depth() int { return len(m.path) }

// Resolve collects every node in t that the chain addresses. The result is
// ordered by position in the file, and is empty when nothing matches; deciding
// what to do about zero or many matches belongs to the caller.
func Resolve(t tree.Tree, steps []Step) ([]Match, error) {
	if len(steps) == 0 {
		return nil, fault.Internal{Where: "target.Resolve", Detail: "empty chain; the caller should handle whole-file targets"}
	}
	for i, s := range steps {
		if s.name == "" {
			return nil, fault.Internal{Where: "target.Resolve", Detail: fmt.Sprintf("step %d has an empty name", i)}
		}
	}

	var matches []Match
	var walk func(nodes []tree.Node, ancestry []tree.Node)
	walk = func(nodes []tree.Node, ancestry []tree.Node) {
		for _, n := range nodes {
			here := append(slices.Clone(ancestry), n)
			if chainMatches(steps, here) {
				matches = append(matches, Match{path: here, file: t.Path()})
			}
			walk(n.Children(), here)
		}
	}
	walk(t.Children(), nil)
	return matches, nil
}

// chainMatches reports whether steps map onto a subsequence of path, with the
// final step matching path's final node. Scanning right to left is what makes
// the subsequence test greedy and correct: the last step is pinned, and each
// earlier step takes the latest ancestor that satisfies it.
func chainMatches(steps []Step, path []tree.Node) bool {
	if len(steps) == 0 || len(path) == 0 || len(steps) > len(path) {
		return false
	}
	if !stepMatches(steps[len(steps)-1], path[len(path)-1]) {
		return false
	}
	i := len(steps) - 2
	j := len(path) - 2
	for i >= 0 && j >= 0 {
		if stepMatches(steps[i], path[j]) {
			i--
		}
		j--
	}
	return i < 0
}

func stepMatches(s Step, n tree.Node) bool {
	return s.name == n.Name() && s.kind == n.Kind()
}

// Near lists annotations that share the chain's final name but were not matched,
// which is what turns "not found" into a useful message: the usual cause is a
// resolver character that names the wrong kind.
func Near(t tree.Tree, steps []Step) []string {
	if len(steps) == 0 {
		return nil
	}
	want := steps[len(steps)-1].name

	var out []string
	var walk func(nodes []tree.Node, ancestry []tree.Node)
	walk = func(nodes []tree.Node, ancestry []tree.Node) {
		for _, n := range nodes {
			here := append(slices.Clone(ancestry), n)
			if n.Name() == want && !chainMatches(steps, here) {
				out = append(out, Match{path: here, file: t.Path()}.Qualified())
			}
			walk(n.Children(), here)
		}
	}
	walk(t.Children(), nil)
	return out
}
