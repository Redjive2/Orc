package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestMutatesKnowsEveryCommand is the test that keeps this table honest.
//
// A command added to the dispatch and not to the table answers false and never
// nudges, so the website goes stale on exactly the new verb nobody thought to
// check. The list is read from `route` itself rather than from the usage text,
// because the usage text is allowed to be incomplete — `assign` is dispatched
// and deliberately undocumented — and a guard that trusts documentation guards
// the documentation.
func TestMutatesKnowsEveryCommand(t *testing.T) {
	for _, command := range routedCommands(t, "cli.go", "route") {
		if _, known := changesThePool[command]; !known {
			t.Errorf("%q is dispatched but the mirror does not know whether it changes anything", command)
		}
	}
}

// TestTheTableHasNoCommandsThatDoNotExist catches the other drift: an entry left
// behind after a command was renamed, which reads as coverage and is not.
func TestTheTableHasNoCommandsThatDoNotExist(t *testing.T) {
	routed := map[string]bool{"help": true} // answered before route is reached
	for _, c := range routedCommands(t, "cli.go", "route") {
		routed[c] = true
	}
	for command := range changesThePool {
		if !routed[command] {
			t.Errorf("the table classifies %q, which is not a command", command)
		}
	}
}

// routedCommands reads the case labels of one function's switch out of the
// source, which is the only listing of commands that cannot be out of date with
// the dispatch — because it *is* the dispatch.
func routedCommands(t *testing.T, path, function string) []string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	var out []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != function {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			clause, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, expr := range clause.List {
				if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					out = append(out, mustUnquote(t, lit.Value))
				}
			}
			return true
		})
	}
	if len(out) == 0 {
		t.Fatalf("no commands found in %s of %s", function, path)
	}
	return out
}

func mustUnquote(t *testing.T, quoted string) string {
	t.Helper()
	if len(quoted) < 2 {
		t.Fatalf("not a quoted string: %q", quoted)
	}
	return quoted[1 : len(quoted)-1]
}

func TestMutatesClassifiesEachCommand(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    bool
	}{
		{"create", true},
		{"push", true},
		{"claim", true},
		{"scope", true},
		{"worktree", true},
		{"status", true},
		{"complete", true},
		{"delete", true},
		{"invite", true},
		{"kick", true},
		{"leave", true},

		{"pool", false},
		{"info", false},
		{"verify", false},
		{"help", false},

		// check-scope reads the scope to decide an exit code, and is called from
		// a hook on every edit. Nudging there would sync on every keystroke.
		{"check-scope", false},

		// assign gives a task an owner. It was documented-but-unbuilt when this
		// table was written and classified false on the grounds that a command
		// which always fails changes nothing; it is built now, so it changes the
		// pool like any other write.
		{"assign", true},

		{"nonsense", false},
	} {
		t.Run(tc.command, func(t *testing.T) {
			if got := mutates(tc.command); got != tc.want {
				t.Errorf("mutates(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}
