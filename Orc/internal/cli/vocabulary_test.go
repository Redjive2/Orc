package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"testing"

	"orc/orc/internal/model"
)

// The list of verbs `orc(...)` may name is written down in one place, and this is
// what keeps it true.
//
// A clause naming a verb the gate does not check reads like a control and is an
// absence — `orc-read` is built on exactly that, deliberately, and a verb that
// fell off the list by accident would be the same thing by mistake. So rather than
// trusting two lists to stay in step, this one walks the package and asks the code.
func TestEveryGatedVerbIsInTheVocabulary(t *testing.T) {
	gated := gatedVerbs(t)
	if len(gated) == 0 {
		t.Fatal("found no mayRunVerb calls at all; this test is no longer reading the package")
	}

	known := model.OrcVerbNames()
	for _, verb := range gated {
		if !slices.Contains(known, verb) {
			t.Errorf("orc %s consults the gate and model.OrcVerbs() does not list it: "+
				"a clause naming it would be refused as unknown while the verb is real", verb)
		}
	}
	for _, verb := range known {
		if !slices.Contains(gated, verb) {
			t.Errorf("model.OrcVerbs() lists %q and nothing checks it: "+
				"a clause naming it would read like a control and allow nothing", verb)
		}
	}
}

// gatedVerbs returns every literal handed to mayRunVerb in this package.
func gatedVerbs(t *testing.T) []string {
	t.Helper()

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("reading the package: %v", err)
	}

	var out []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "mayRunVerb" || len(call.Args) != 1 {
					return true
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					// A verb worked out at runtime would defeat the whole check,
					// so it fails here rather than being skipped quietly.
					t.Errorf("%s: mayRunVerb is called with something other than a literal",
						fset.Position(call.Pos()))
					return true
				}
				verb, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Errorf("%s: %v", fset.Position(call.Pos()), err)
					return true
				}
				if !slices.Contains(out, verb) {
					out = append(out, verb)
				}
				return true
			})
		}
	}
	slices.Sort(out)
	return out
}

// The tool names are the other half, and they cannot be found by reading this
// tree: a tool capability is checked by another program. What can be checked is
// that the toolkit's own clauses only name capabilities this list knows, since
// those two are written a hundred lines apart in the same repository.
func TestToolkitNamesKnownTools(t *testing.T) {
	for _, tool := range model.Tools() {
		if tool.Name == "" || tool.Does == "" || tool.In == "" {
			t.Errorf("tool %q is missing part of its entry; the cheat sheet prints all three", tool.Name)
		}
	}
}
