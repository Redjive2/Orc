package web_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The interface keeps its own copy of Orc's vocabulary — the verbs `orc()` checks
// and the capabilities `tool()` names — for the case where a fleet answers without
// one: an older orc, or a machine that could not be reached. The sheet under the
// clause box is drawn from it, and `unknownWord` decides from it whether to tell
// somebody their clause controls nothing.
//
// A copy is a thing that goes out of date, and this one goes out of date silently:
// nothing fails, the sheet simply stops mentioning a verb that has existed for
// months. So the copy is checked against the original rather than trusted.
//
// Orc is a separate module and cq must build without it, so this reads its *source*
// rather than importing it, and skips when the tree is not laid out side by side —
// a release build of cq alone is not the place this drift gets caught. The
// developer tree is.
const orcVocabulary = "../../../Orc/internal/model/vocabulary.go"

func TestTheFallbackVocabularyMatchesOrc(t *testing.T) {
	if _, err := os.Stat(orcVocabulary); err != nil {
		t.Skipf("Orc's source is not beside cq's, so the copy was not checked: %v", err)
	}

	for _, kind := range []struct{ fn, list string }{
		{"OrcVerbs", "verbs"},
		{"Tools", "tools"},
	} {
		want := orcWords(t, kind.fn)
		got := fallbackWords(t, kind.list)

		if len(want) == 0 {
			t.Fatalf("read no words out of %s(); the check would pass on anything", kind.fn)
		}
		for word, does := range want {
			mine, ok := got[word]
			if !ok {
				t.Errorf("orc checks %q and the interface's %s do not list it.\n"+
					"A fleet that answers without its vocabulary gets a sheet missing this verb, "+
					"and is told a clause naming it controls nothing.\n"+
					"Add it to FALLBACK_WORDS in app/clauses.js: { word: %q, does: %q }",
					word, kind.list, word, does)
				continue
			}
			if mine != does {
				t.Errorf("%q is described differently in the two places:\n  orc: %s\n   cq: %s",
					word, does, mine)
			}
		}
		for word := range got {
			if _, ok := want[word]; !ok {
				t.Errorf("the interface's %s list %q, which orc no longer checks. "+
					"The sheet is offering a word that controls nothing.", kind.list, word)
			}
		}
	}
}

// orcWords reads the words one of Orc's vocabulary functions returns.
//
// Through the parser rather than a regexp because the shape being read is Go: an
// entry rewritten across two lines, or a description containing a brace, is still
// the same list, and a pattern over the text would quietly stop seeing it.
func orcWords(t *testing.T, fn string) map[string]string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), orcVocabulary, nil, 0)
	if err != nil {
		t.Fatalf("reading orc's vocabulary: %v", err)
	}

	words := map[string]string{}
	for _, decl := range file.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if !ok || d.Name.Name != fn {
			continue
		}
		ast.Inspect(d, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || len(lit.Elts) < 2 {
				return true
			}
			word, ok := literal(lit.Elts[0])
			if !ok {
				return true
			}
			does, ok := literal(lit.Elts[1])
			if !ok {
				return true
			}
			words[word] = does
			return true
		})
	}
	return words
}

func literal(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

var (
	// The list itself, up to the line that closes it. Anchored on the two-space
	// indent of a top-level key so a `]` inside a description cannot end it early.
	fallbackList = regexp.MustCompile(`(?s)\n  (verbs|tools): \[(.*?)\n  \],`)
	fallbackWord = regexp.MustCompile(`\{ word: "([^"]*)", does: "([^"]*)"`)
)

// fallbackWords reads one half of FALLBACK_WORDS out of the interface's source.
//
// By pattern, because there is no JavaScript parser in the standard library and
// pulling one in to read a literal would be a dependency for a test. The shape it
// depends on is pinned by TestTheFallbackIsReadable below: if the file is written
// some other way, that fails rather than this silently matching nothing.
func fallbackWords(t *testing.T, list string) map[string]string {
	t.Helper()

	src, err := os.ReadFile("app/clauses.js")
	if err != nil {
		t.Fatalf("reading the interface's clauses: %v", err)
	}

	words := map[string]string{}
	for _, block := range fallbackList.FindAllStringSubmatch(string(src), -1) {
		if block[1] != list {
			continue
		}
		for _, m := range fallbackWord.FindAllStringSubmatch(block[2], -1) {
			words[m[1]] = m[2]
		}
	}
	return words
}

// A copy that cannot be read is a copy that is never checked, and the check above
// passes loudly on an empty list only because this one says why it was empty.
func TestTheFallbackIsReadable(t *testing.T) {
	for _, list := range []string{"verbs", "tools"} {
		if len(fallbackWords(t, list)) == 0 {
			t.Errorf("found no %s in FALLBACK_WORDS in app/clauses.js. Either the list is "+
				"empty — the sheet has nothing to show a fleet that answers without its "+
				"vocabulary — or it is no longer written as `{ word: \"x\", does: \"y\" }` "+
				"entries under a two-space `%s: [`, and the check against orc has been "+
				"reading nothing.", list, list)
		}
	}
}

// --- what runs with no clause ---------------------------------------------

// The innocuous list is the same hazard in a different shape.
//
// It is not words a clause may *name* — it is what an identity may run without
// one — so it is a plain list rather than word/description pairs, and it needs
// its own reader. The consequence of drift is worse than for the two above: a
// browser whose copy is short tells somebody an agent cannot run a command it can
// run, and one whose copy is long tells them the opposite about a command orc
// will refuse.
func TestTheFallbackInnocuousMatchesOrc(t *testing.T) {
	if _, err := os.Stat(orcVocabulary); err != nil {
		t.Skipf("Orc's source is not beside cq's, so the copy was not checked: %v", err)
	}

	names := orcStrings(t, "Innocuous")
	if len(names) == 0 {
		t.Fatal("read no words out of Innocuous(); the check would pass on anything")
	}

	// The server sends model.InnocuousWords, not the bare names: some of these
	// commands are default in one form and not another, and a list that says
	// `mailman` while `mailman admin` is refused is worse than no list at all. The
	// fallback has to be what the server would have sent, so the same annotation
	// is composed here from the same two sources.
	guarded := orcGuarded(t)
	want := make([]string, 0, len(names))
	for _, name := range names {
		if subs, ok := guarded[name]; ok && len(subs) > 0 {
			// Spelled as model.InnocuousWords spells it: one "not" per part, so a
			// command with two exceptions names both.
			parts := make([]string, 0, len(subs))
			for _, sub := range subs {
				parts = append(parts, "not "+name+" "+sub)
			}
			want = append(want, name+" ("+strings.Join(parts, ", ")+")")
			continue
		}
		want = append(want, name)
	}
	slices.Sort(want)

	got := fallbackInnocuous(t)
	if !slices.Equal(want, got) {
		t.Errorf("what runs with no shell clause differs between the two:\n"+
			"  orc: %v\n   cq: %v\n"+
			"Update `innocuous` in FALLBACK_WORDS in app/clauses.js.", want, got)
	}
}

// orcGuarded reads the `guarded` map out of Orc's vocabulary: the subcommands a
// default command does not cover.
//
// An empty result is not an error. The map is allowed to be empty — no command
// has to have an exception — and a test that insisted otherwise would fail the
// day the last one was removed.
func orcGuarded(t *testing.T) map[string][]string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), orcVocabulary, nil, 0)
	if err != nil {
		t.Fatalf("reading orc's vocabulary: %v", err)
	}
	out := map[string][]string{}
	for _, decl := range file.Decls {
		d, ok := decl.(*ast.GenDecl)
		if !ok || d.Tok != token.VAR {
			continue
		}
		for _, spec := range d.Specs {
			v, ok := spec.(*ast.ValueSpec)
			if !ok || len(v.Names) != 1 || v.Names[0].Name != "guarded" {
				continue
			}
			for _, value := range v.Values {
				lit, ok := value.(*ast.CompositeLit)
				if !ok {
					continue
				}
				for _, e := range lit.Elts {
					kv, ok := e.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, kok := literal(kv.Key)
					if !kok {
						continue
					}
					// A command may have more than one part that cannot check its
					// caller — `orc bootstrap` and `orc env` are both — so the value
					// is a list. It was a single string once, and reading it as one
					// after it grew made this find nothing at all: the check still
					// ran, still passed, and had stopped comparing the thing it was
					// written for.
					subs, ok := kv.Value.(*ast.CompositeLit)
					if !ok {
						continue
					}
					for _, e := range subs.Elts {
						if sub, ok := literal(e); ok {
							out[key] = append(out[key], sub)
						}
					}
				}
			}
		}
	}
	return out
}

// orcStrings reads a `[]string` a function in Orc's vocabulary returns.
func orcStrings(t *testing.T, fn string) []string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), orcVocabulary, nil, 0)
	if err != nil {
		t.Fatalf("reading orc's vocabulary: %v", err)
	}
	var out []string
	for _, decl := range file.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if !ok || d.Name.Name != fn {
			continue
		}
		ast.Inspect(d, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			for _, e := range lit.Elts {
				if s, ok := literal(e); ok {
					out = append(out, s)
				}
			}
			return true
		})
	}
	slices.Sort(out)
	return out
}

// fallbackInnocuous reads `innocuous: [...]` out of the interface's source.
var fallbackFree = regexp.MustCompile(`(?s)\n  innocuous: \[(.*?)\],`)

func fallbackInnocuous(t *testing.T) []string {
	t.Helper()

	src, err := os.ReadFile("app/clauses.js")
	if err != nil {
		t.Fatalf("reading the interface's clauses: %v", err)
	}
	m := fallbackFree.FindStringSubmatch(string(src))
	if m == nil {
		t.Fatal("found no `innocuous: [` list in FALLBACK_WORDS in app/clauses.js, so the " +
			"check against orc has been reading nothing")
	}
	var out []string
	for _, w := range regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(m[1], -1) {
		out = append(out, w[1])
	}
	slices.Sort(out)
	return out
}
