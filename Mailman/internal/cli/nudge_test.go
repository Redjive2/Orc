package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestMutatesKnowsEveryCommand is the test that keeps this table honest.
//
// A command added to the dispatch and not to the table answers false and never
// nudges, so the website goes stale on exactly the new verb nobody thought to
// check. The list is read from `route` itself rather than from the usage text,
// because a guard that trusts documentation guards the documentation.
func TestMutatesKnowsEveryCommand(t *testing.T) {
	for _, command := range routedCommands(t, "cli.go", "route") {
		if _, known := changesTheStore[command]; !known {
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
	for command := range changesTheStore {
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
					out = append(out, lit.Value[1:len(lit.Value)-1])
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

func TestMutatesClassifiesEachCommand(t *testing.T) {
	for _, tc := range []struct {
		command string
		args    []string
		want    bool
	}{
		{"send", []string{"subject", "bob", "body"}, true},
		{"reply", []string{"id=1", "subject", "body"}, true},
		{"read", []string{"id=1"}, true},
		{"prune", []string{"id=1", "--yes"}, true},
		{"cc", []string{"id=1", "bob"}, true},

		{"inbox", nil, false},
		{"open", []string{"id=1"}, false},
		{"convo", []string{"c1"}, false},
		{"check", []string{"id=1"}, false},
		{"verify", nil, false},
		{"help", nil, false},

		// `archive` is two commands wearing one name.
		{"archive", []string{`from="bob"`}, true},
		{"archive", nil, false},

		// So is `admin`.
		{"admin", []string{"user", "add", "dave"}, true},
		{"admin", []string{"user", "remove", "dave"}, true},
		{"admin", []string{"user", "list"}, false},
		{"admin", []string{"user"}, false},
		{"admin", nil, false},

		// A verb that does not exist cannot have changed anything.
		{"nonsense", nil, false},
	} {
		name := tc.command + " " + strings.Join(tc.args, " ")
		t.Run(strings.TrimSpace(name), func(t *testing.T) {
			if got := mutates(tc.command, tc.args); got != tc.want {
				t.Errorf("mutates(%q, %v) = %v, want %v", tc.command, tc.args, got, tc.want)
			}
		})
	}
}

// TestReadIsAChange guards a judgement that looks wrong at a glance.
//
// `read` sounds like a reader, and it is the one verb here whose name argues
// against its classification: it marks mail read, visibly to the sender, and cq
// mirrors read state. A mirror that ignored it would keep showing mail as unread
// after it had been read.
func TestReadIsAChange(t *testing.T) {
	if !mutates("read", []string{"id=1"}) {
		t.Error("marking mail read is a change the mirror needs")
	}
}
