package target_test

import (
	"errors"
	"strings"
	"testing"

	"orc/anno/internal/marker"
	"orc/anno/internal/target"
	"orc/anno/internal/tree"
	"orc/common/fault"
	"orc/common/source"
)

// duplicated has two parts called "declarations", in different symbols, which
// is the case that makes partial chains ambiguous.
const duplicated = `// @:> section code
// @:> symbol Operate
a
// @:> part declarations
b
// @:< declarations
c
// @:> symbol Reduce
d
// @:> part declarations
e
`

func buildTree(t *testing.T, name, text string) tree.Tree {
	t.Helper()
	f, err := source.Parse(name, []byte(text))
	if err != nil {
		t.Fatalf("source.Parse: %v", err)
	}
	tr, err := tree.Build(f)
	if err != nil {
		t.Fatalf("tree.Build: %v", err)
	}
	return tr
}

func steps(t *testing.T, raw string) []target.Step {
	t.Helper()
	tgt, err := target.ParseOne(raw)
	if err != nil {
		t.Fatalf("ParseOne(%q): %v", raw, err)
	}
	return tgt.Steps()
}

func TestParseSplitsPathFromChain(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		path  string
		chain string
	}{
		{"example.go", "example.go", ""},
		{"example.go@code", "example.go", "@code"},
		{"example.go:Operate", "example.go", ":Operate"},
		{"example.go^declarations", "example.go", "^declarations"},
		{"example.go@code:Operate^declarations", "example.go", "@code:Operate^declarations"},
		{"./dir/example.go:Sym", "./dir/example.go", ":Sym"},
		{"a:b/c.go:Sym", "a:b/c.go", ":Sym"},
		{"C:/src/x.go^p", "C:/src/x.go", "^p"},
		{"dir", "dir", ""},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			tgt, err := target.ParseOne(tc.raw)
			if err != nil {
				t.Fatalf("ParseOne: %v", err)
			}
			if tgt.Path() != tc.path {
				t.Errorf("path = %q, want %q", tgt.Path(), tc.path)
			}
			var b strings.Builder
			for _, s := range tgt.Steps() {
				b.WriteString(s.String())
			}
			if b.String() != tc.chain {
				t.Errorf("chain = %q, want %q", b.String(), tc.chain)
			}
			if tgt.Raw() != tc.raw {
				t.Errorf("Raw = %q, want %q", tgt.Raw(), tc.raw)
			}
			if got, want := tgt.String(), tc.path+tc.chain; got != want {
				t.Errorf("String = %q, want %q", got, want)
			}
			if got := tgt.IsFile(); got != (tc.chain == "") {
				t.Errorf("IsFile = %v for chain %q", got, tc.chain)
			}
		})
	}
}

func TestParseOffersEveryReadingLongestPathFirst(t *testing.T) {
	got, err := target.Parse("a:b:c")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, c := range got {
		paths = append(paths, c.Path())
	}
	want := []string{"a:b:c", "a:b", "a"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Errorf("candidate paths = %v, want %v", paths, want)
	}
	// ParseOne takes the shortest path: the most chain-like reading.
	one, err := target.ParseOne("a:b:c")
	if err != nil {
		t.Fatal(err)
	}
	if one.Path() != "a" {
		t.Errorf("ParseOne path = %q, want %q", one.Path(), "a")
	}
}

func TestParseTreatsInvalidChainsAsPath(t *testing.T) {
	for _, raw := range []string{
		"a:b/c",  // a name may not contain a path separator
		"a: b",   // nor whitespace
		"a:",     // nor be empty
		"a:b[c]", // nor contain brackets
		"a:b\\c", // nor a backslash
	} {
		tgt, err := target.ParseOne(raw)
		if err != nil {
			t.Fatalf("ParseOne(%q): %v", raw, err)
		}
		if !tgt.IsFile() || tgt.Path() != raw {
			t.Errorf("ParseOne(%q) = %q with %d steps, want the whole string as a path",
				raw, tgt.Path(), len(tgt.Steps()))
		}
	}
}

func TestParseRejectsUnusableStrings(t *testing.T) {
	if _, err := target.Parse(""); !errors.Is(err, fault.ErrUsage) {
		t.Errorf("empty target should be a usage fault, got %v", err)
	}
	if _, err := target.ParseOne(""); !errors.Is(err, fault.ErrUsage) {
		t.Errorf("empty target should be a usage fault, got %v", err)
	}
	if _, err := target.Parse("a\x01b"); !errors.Is(err, fault.ErrUsage) {
		t.Errorf("non-printing character should be a usage fault, got %v", err)
	}
}

func TestLeadingResolverIsPartOfThePath(t *testing.T) {
	// A target must have a path; ":name" alone names no file.
	tgt, err := target.ParseOne(":name")
	if err != nil {
		t.Fatal(err)
	}
	if !tgt.IsFile() {
		t.Errorf("a leading resolver should not start a chain")
	}
}

func TestFullyQualifiedChainResolvesUniquely(t *testing.T) {
	tr := buildTree(t, "x.go", duplicated)
	for _, raw := range []string{
		"x.go@code:Operate^declarations",
		"x.go:Operate^declarations",
		"x.go@code:Reduce^declarations",
	} {
		matches, err := target.Resolve(tr, steps(t, raw))
		if err != nil {
			t.Fatalf("Resolve(%q): %v", raw, err)
		}
		if len(matches) != 1 {
			t.Errorf("Resolve(%q) found %d matches, want 1", raw, len(matches))
		}
	}
}

func TestPartialChainMatchesEverywhere(t *testing.T) {
	tr := buildTree(t, "x.go", duplicated)
	for _, raw := range []string{
		"x.go^declarations",      // no ancestors named at all
		"x.go@code^declarations", // skips the symbol level entirely
	} {
		matches, err := target.Resolve(tr, steps(t, raw))
		if err != nil {
			t.Fatalf("Resolve(%q): %v", raw, err)
		}
		if len(matches) != 2 {
			t.Fatalf("Resolve(%q) found %d matches, want 2", raw, len(matches))
		}
		want := []string{
			"x.go@code:Operate^declarations",
			"x.go@code:Reduce^declarations",
		}
		for i, m := range matches {
			if got := m.Qualified(); got != want[i] {
				t.Errorf("match %d = %q, want %q", i, got, want[i])
			}
			if m.File() != "x.go" {
				t.Errorf("match file = %q", m.File())
			}
			if m.Depth() != 3 {
				t.Errorf("match depth = %d, want 3", m.Depth())
			}
		}
	}
}

func TestChainOrderMatters(t *testing.T) {
	tr := buildTree(t, "x.go", duplicated)
	// The steps exist but in the wrong order, so nothing matches.
	matches, err := target.Resolve(tr, steps(t, "x.go^declarations:Operate"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Errorf("out-of-order chain matched %d nodes, want 0", len(matches))
	}
}

func TestWrongKindDoesNotMatch(t *testing.T) {
	tr := buildTree(t, "x.go", duplicated)
	matches, err := target.Resolve(tr, steps(t, "x.go@declarations"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Errorf("a part addressed as a section matched %d nodes, want 0", len(matches))
	}

	near := target.Near(tr, steps(t, "x.go@declarations"))
	if len(near) != 2 {
		t.Fatalf("Near listed %d candidates, want 2: %v", len(near), near)
	}
	for _, n := range near {
		if !strings.HasSuffix(n, "^declarations") {
			t.Errorf("near candidate %q should be fully qualified", n)
		}
	}
}

func TestNearIgnoresMatchedNodes(t *testing.T) {
	tr := buildTree(t, "x.go", duplicated)
	near := target.Near(tr, steps(t, "x.go:Operate^declarations"))
	if len(near) != 1 {
		t.Fatalf("Near = %v, want only the unmatched twin", near)
	}
	if !strings.Contains(near[0], "Reduce") {
		t.Errorf("Near = %v, want the Reduce twin", near)
	}
	if got := target.Near(tr, nil); got != nil {
		t.Errorf("Near with no steps = %v, want nil", got)
	}
}

func TestResolveRejectsEmptyChains(t *testing.T) {
	tr := buildTree(t, "x.go", duplicated)
	if _, err := target.Resolve(tr, nil); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("an empty chain should be an internal fault, got %v", err)
	}
	bad := []target.Step{{}}
	if _, err := target.Resolve(tr, bad); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("a nameless step should be an internal fault, got %v", err)
	}
}

func TestChainLongerThanDepthCannotMatch(t *testing.T) {
	tr := buildTree(t, "x.go", "// @:> section s\nx\n")
	matches, err := target.Resolve(tr, steps(t, "x.go@s:a^b"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Errorf("an over-long chain matched %d nodes, want 0", len(matches))
	}
}

func TestNewStep(t *testing.T) {
	s, err := target.NewStep(marker.Symbol, "Name")
	if err != nil {
		t.Fatal(err)
	}
	if s.Kind() != marker.Symbol || s.Name() != "Name" {
		t.Errorf("step = %+v", s)
	}
	if got, want := s.String(), ":Name"; got != want {
		t.Errorf("String = %q, want %q", got, want)
	}
	if _, err := target.NewStep(marker.Kind(9), "x"); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("invalid kind should be internal, got %v", err)
	}
	if _, err := target.NewStep(marker.Symbol, ""); !errors.Is(err, fault.ErrUsage) {
		t.Errorf("empty name should be usage, got %v", err)
	}
}

func TestLastReportsTheAddressedStep(t *testing.T) {
	tgt, err := target.ParseOne("x.go:Operate")
	if err != nil {
		t.Fatal(err)
	}
	last, ok := tgt.Last()
	if !ok || last.Name() != "Operate" {
		t.Errorf("Last = %v, %v", last, ok)
	}
	empty, err := target.ParseOne("x.go")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := empty.Last(); ok {
		t.Errorf("a chainless target has no last step")
	}
}

func TestWithPathKeepsTheChain(t *testing.T) {
	tgt, err := target.ParseOne("dir^p")
	if err != nil {
		t.Fatal(err)
	}
	moved := tgt.WithPath("dir/file.go")
	if moved.Path() != "dir/file.go" {
		t.Errorf("path = %q", moved.Path())
	}
	if len(moved.Steps()) != 1 || moved.Steps()[0].Name() != "p" {
		t.Errorf("steps = %v", moved.Steps())
	}
	if moved.Raw() != "dir^p" {
		t.Errorf("Raw = %q, want the original", moved.Raw())
	}
}

func TestStepsIsACopy(t *testing.T) {
	tgt, err := target.ParseOne("x.go:a^b")
	if err != nil {
		t.Fatal(err)
	}
	s := tgt.Steps()
	s[0] = target.Step{}
	if tgt.Steps()[0].Name() != "a" {
		t.Errorf("Steps() exposed internal state")
	}
}

func TestMatchAccessors(t *testing.T) {
	tr := buildTree(t, "x.go", duplicated)
	matches, err := target.Resolve(tr, steps(t, "x.go:Operate^declarations"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("Resolve: %v, %d matches", err, len(matches))
	}
	m := matches[0]

	node, err := m.Node()
	if err != nil {
		t.Fatal(err)
	}
	if node.Name() != "declarations" {
		t.Errorf("node = %q", node.Name())
	}

	path := m.Path()
	if len(path) != 3 {
		t.Fatalf("path length = %d, want 3", len(path))
	}
	path[0] = tree.Node{}
	if m.Path()[0].Name() != "code" {
		t.Errorf("Path() exposed internal state")
	}

	if _, err := (target.Match{}).Node(); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("an empty match should report internally, got %v", err)
	}
}
