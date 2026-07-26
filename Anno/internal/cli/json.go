package cli

import (
	"encoding/json"

	"orc/anno/internal/tree"
	"orc/common/fault"
)

// The `--json` projection of a tree.
//
// It exists so another tool can read a file's annotations without parsing the
// box-drawn index: a presentation format is a bad contract, and Communiqué needs
// a good one to mirror this repository to the web.
//
// Two rules keep it usable as a contract:
//
//   - It is a projection of the same tree.Tree the index draws, so JSON and the
//     table can never disagree about what a file contains.
//   - Fields are added, never repurposed or removed. A reader that ignores what
//     it does not recognise keeps working across a version of Anno it has not
//     seen.

// jsonTree is one file's annotations.
type jsonTree struct {
	Path  string     `json:"path"`
	Lines int        `json:"lines"`
	Nodes []jsonNode `json:"nodes"`
}

// jsonNode is one annotation, with the ones nested inside it.
//
// The nesting is kept as nesting rather than flattened with a depth, because
// that is what the annotations *are* — a section holds symbols, a symbol holds
// parts — and a reader folding the tree would otherwise have to rebuild the
// shape from ranks.
type jsonNode struct {
	Kind string   `json:"kind"`
	Name string   `json:"name"`
	Meta []string `json:"meta,omitempty"`
	// Start and End are the annotation's whole span, marker included: it is what
	// the index reports and what a reader needs to show the annotation in place.
	Start int `json:"start"`
	End   int `json:"end"`
	Lines int `json:"lines"`
	// ContentStart and ContentEnd are the narrower span `read` returns. The two
	// are different questions and both are worth having: one says where the
	// annotation is, the other says what is inside it.
	ContentStart int        `json:"content_start"`
	ContentEnd   int        `json:"content_end"`
	Children     []jsonNode `json:"children,omitempty"`
}

// treeJSON projects one file's tree.
func treeJSON(t tree.Tree) jsonTree {
	return jsonTree{
		Path:  t.Path(),
		Lines: t.Count(),
		Nodes: nodesJSON(t.Children()),
	}
}

// takeFlag removes one flag from the arguments and reports whether it was there.
//
// Anno parses arguments by counting them rather than with a flag set, because a
// chain like `file@section` must not be mistaken for one. This keeps that, and
// refuses an unknown flag rather than treating it as a path.
func takeFlag(args []string, name string) (rest []string, found bool, err error) {
	for _, arg := range args {
		switch {
		case arg == name:
			if found {
				return nil, false, fault.Usage{Reason: "repeated " + name}
			}
			found = true
		case len(arg) > 1 && arg[0] == '-' && arg != "-":
			return nil, false, fault.Usage{Reason: "unknown flag " + arg}
		default:
			rest = append(rest, arg)
		}
	}
	return rest, found, nil
}

func nodesJSON(nodes []tree.Node) []jsonNode {
	out := make([]jsonNode, 0, len(nodes))
	for _, n := range nodes {
		display, content := n.Display(), n.Content()
		out = append(out, jsonNode{
			Kind:         n.Kind().String(),
			Name:         n.Name(),
			Meta:         n.Meta(),
			Start:        display.Start(),
			End:          display.End(),
			Lines:        n.Lines(),
			ContentStart: content.Start(),
			ContentEnd:   content.End(),
			Children:     nodesJSON(n.Children()),
		})
	}
	return out
}

// emitJSON writes a value as indented JSON.
func (a App) emitJSON(v any) error {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fault.Internal{Where: "cli.emitJSON", Detail: err.Error()}
	}
	return a.say(string(body))
}
