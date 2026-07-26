package marker_test

import (
	"errors"
	"strings"
	"testing"

	"orc/anno/internal/marker"
	"orc/common/fault"
)

func TestClassifyAccepts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		line  string
		op    marker.Op
		kind  marker.Kind
		anno  string
		meta  []string
		canon string
	}{
		{"open section", "// @:> section data", marker.Open, marker.Section, "data", nil, "@:> section data"},
		{"open symbol", "// @:> symbol Pair [struct L R]", marker.Open, marker.Symbol, "Pair", []string{"struct", "L", "R"}, "@:> symbol Pair [struct L R]"},
		{"open part", "\t// @:> part declarations", marker.Open, marker.Part, "declarations", nil, "@:> part declarations"},
		{"next line", "// @:; symbol Operation", marker.Next, marker.Symbol, "Operation", nil, "@:; symbol Operation"},
		{"close", "// @:< declarations", marker.Close, marker.Section, "declarations", nil, "@:< declarations"},
		{"aligned spacing", "// @:>   symbol    Pair   [struct   L]", marker.Open, marker.Symbol, "Pair", []string{"struct", "L"}, "@:> symbol Pair [struct L]"},
		{"empty metadata", "// @:> section s []", marker.Open, marker.Section, "s", []string{}, "@:> section s"},
		{"returns convention", "// @:> symbol F [A ->B]", marker.Open, marker.Symbol, "F", []string{"A", "->B"}, "@:> symbol F [A ->B]"},
		{"no comment leader", "@:> section bare", marker.Open, marker.Section, "bare", nil, "@:> section bare"},
		{"after code", "x := 1 // @:> symbol x", marker.Open, marker.Symbol, "x", nil, "@:> symbol x"},
		{"block comment closer", "/* @:> section s */", marker.Open, marker.Section, "s", nil, "@:> section s"},
		{"html closer", "<!-- @:> section s -->", marker.Open, marker.Section, "s", nil, "@:> section s"},
		{"jinja closer", "{# @:> section s #}", marker.Open, marker.Section, "s", nil, "@:> section s"},
		{"handlebars closer", "{{! @:> section s }}", marker.Open, marker.Section, "s", nil, "@:> section s"},
		{"ocaml closer", "(* @:> section s *)", marker.Open, marker.Section, "s", nil, "@:> section s"},
		{"sql closer", "-- @:> section s --", marker.Open, marker.Section, "s", nil, "@:> section s"},
		{"closer with metadata", "/* @:> symbol F [A B] */", marker.Open, marker.Symbol, "F", []string{"A", "B"}, "@:> symbol F [A B]"},
		{"trailing whitespace", "// @:> section s   \t", marker.Open, marker.Section, "s", nil, "@:> section s"},
		{"unicode name", "// @:> symbol café", marker.Open, marker.Symbol, "café", nil, "@:> symbol café"},
		{"sigil inside a string wins last", `s := "@:> section fake" // @:> section real`, marker.Open, marker.Section, "real", nil, "@:> section real"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, ok, err := marker.Classify("f.go", 7, tc.line)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !ok {
				t.Fatalf("line was not recognised as a marker")
			}
			if m.Op() != tc.op {
				t.Errorf("op = %v, want %v", m.Op(), tc.op)
			}
			if m.Op() != marker.Close && m.Kind() != tc.kind {
				t.Errorf("kind = %v, want %v", m.Kind(), tc.kind)
			}
			if m.Name() != tc.anno {
				t.Errorf("name = %q, want %q", m.Name(), tc.anno)
			}
			if strings.Join(m.Meta(), ",") != strings.Join(tc.meta, ",") {
				t.Errorf("meta = %v, want %v", m.Meta(), tc.meta)
			}
			if m.Line() != 7 {
				t.Errorf("line = %d, want 7", m.Line())
			}
			if m.Col() < 1 {
				t.Errorf("col = %d, want a positive column", m.Col())
			}
			if got := m.String(); got != tc.canon {
				t.Errorf("String() = %q, want %q", got, tc.canon)
			}
		})
	}
}

func TestClassifyIgnoresOrdinaryLines(t *testing.T) {
	for _, line := range []string{
		"",
		"package main",
		"// a comment",
		"// @: not a sigil",
		"// @:) unknown sigil",
		"x := a @ b",
		"m[key] = value",
	} {
		m, ok, err := marker.Classify("f.go", 1, line)
		if err != nil {
			t.Errorf("Classify(%q) errored: %v", line, err)
		}
		if ok {
			t.Errorf("Classify(%q) = %v, want no marker", line, m)
		}
	}
}

func TestClassifyRejects(t *testing.T) {
	for _, tc := range []struct{ name, line, want string }{
		{"no body", "// @:>", "no body"},
		{"only whitespace", "// @:;   ", "no body"},
		{"unknown kind", "// @:> chapter x", "unknown annotation kind"},
		{"missing name", "// @:> section", "expected"},
		{"too many words", "// @:> section a b", "words before"},
		{"close with kind", "// @:< symbol s", "exactly one name"},
		{"close with metadata", "// @:< s [a]", "no metadata"},
		{"unclosed bracket", "// @:> section s [a b", "not closed"},
		{"stray close bracket", "// @:> section s a]", "never opens"},
		{"nested bracket", "// @:> section s [a [b]]", "nested bracket"},
		{"bracket not at end", "// @:> section [a] s", "not closed"},
		{"name with resolver", "// @:> section a:b", "resolver character"},
		{"name with path separator", "// @:> section a/b", "path separator"},
		{"empty name in brackets", "// @:> section []", "expected"},
		{"close name with resolver", "// @:< a:b", "resolver character"},
		{"control character in name", "// @:> section a\x01b", "non-printing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ok, err := marker.Classify("f.go", 3, tc.line)
			if err == nil {
				t.Fatalf("Classify(%q) = ok:%v, want an error", tc.line, ok)
			}
			if ok {
				t.Errorf("a rejected line must not also report a marker")
			}
			if !errors.Is(err, fault.ErrParse) {
				t.Errorf("error = %v, want a parse fault", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message %q should mention %q", err, tc.want)
			}
			var p fault.Parse
			if !errors.As(err, &p) {
				t.Fatalf("error is not fault.Parse")
			}
			if p.Line != 3 || p.Col < 1 || p.Path != "f.go" {
				t.Errorf("position = %s:%d:%d, want f.go:3 with a column", p.Path, p.Line, p.Col)
			}
		})
	}
}

func TestClassifyRejectsNonPositiveLineNumbers(t *testing.T) {
	_, _, err := marker.Classify("f.go", 0, "// @:> section s")
	if !errors.Is(err, fault.ErrInternal) {
		t.Fatalf("error = %v, want internal", err)
	}
}

func TestKindProperties(t *testing.T) {
	for _, tc := range []struct {
		kind     marker.Kind
		spelling string
		resolver rune
		rank     int
	}{
		{marker.Section, "section", '@', 0},
		{marker.Symbol, "symbol", ':', 1},
		{marker.Part, "part", '^', 2},
	} {
		if got := tc.kind.String(); got != tc.spelling {
			t.Errorf("String() = %q, want %q", got, tc.spelling)
		}
		if got := tc.kind.Resolver(); got != tc.resolver {
			t.Errorf("Resolver() = %q, want %q", got, tc.resolver)
		}
		if got := tc.kind.Rank(); got != tc.rank {
			t.Errorf("Rank() = %d, want %d", got, tc.rank)
		}
		if !tc.kind.Valid() {
			t.Errorf("%v should be valid", tc.kind)
		}
		if k, ok := marker.ParseKind(tc.spelling); !ok || k != tc.kind {
			t.Errorf("ParseKind(%q) = %v,%v", tc.spelling, k, ok)
		}
		if k, ok := marker.KindForResolver(tc.resolver); !ok || k != tc.kind {
			t.Errorf("KindForResolver(%q) = %v,%v", tc.resolver, k, ok)
		}
		if !marker.IsResolver(tc.resolver) {
			t.Errorf("%q should be a resolver", tc.resolver)
		}
	}

	if _, ok := marker.ParseKind("nope"); ok {
		t.Errorf("ParseKind should reject unknown kinds")
	}
	if _, ok := marker.KindForResolver('%'); ok {
		t.Errorf("KindForResolver should reject unknown resolvers")
	}
	if marker.IsResolver('%') {
		t.Errorf("%% is not a resolver")
	}

	invalid := marker.Kind(99)
	if invalid.Valid() {
		t.Errorf("Kind(99) should not be valid")
	}
	if got := invalid.String(); !strings.Contains(got, "99") {
		t.Errorf("String() of an invalid kind = %q", got)
	}
	if got := invalid.Resolver(); got != 0 {
		t.Errorf("Resolver() of an invalid kind = %q, want 0", got)
	}
}

func TestOpProperties(t *testing.T) {
	for _, tc := range []struct {
		op    marker.Op
		name  string
		sigil string
	}{
		{marker.Open, "open", "@:>"},
		{marker.Close, "close", "@:<"},
		{marker.Next, "next", "@:;"},
	} {
		if got := tc.op.String(); got != tc.name {
			t.Errorf("String() = %q, want %q", got, tc.name)
		}
		if got := tc.op.Sigil(); got != tc.sigil {
			t.Errorf("Sigil() = %q, want %q", got, tc.sigil)
		}
	}
	invalid := marker.Op(42)
	if got := invalid.String(); !strings.Contains(got, "42") {
		t.Errorf("String() of an invalid op = %q", got)
	}
	if got := invalid.Sigil(); got != "" {
		t.Errorf("Sigil() of an invalid op = %q, want empty", got)
	}
}

func TestMetaIsACopy(t *testing.T) {
	m, _, err := marker.Classify("f.go", 1, "// @:> symbol s [a b]")
	if err != nil {
		t.Fatal(err)
	}
	meta := m.Meta()
	meta[0] = "clobbered"
	if m.Meta()[0] != "a" {
		t.Errorf("Meta() exposed internal state")
	}
}

// FuzzClassify asserts the property that matters most: Classify never panics,
// and never returns a marker and an error at the same time. Any input, however
// malformed, is either a marker, not a marker, or a diagnosed fault.
// TestASigilInsideAStringIsAMention is the rule that lets Anno read its own
// source.
//
// Without it, any file that *talks about* the syntax is a file Anno refuses:
// `return "@:>"` is parsed as a marker whose kind is a quotation mark, and one
// such line makes the whole file unreadable. Fifteen of the seventeen files in
// this repository that mention a sigil were refused for exactly this.
func TestASigilInsideAStringIsAMention(t *testing.T) {
	for _, line := range []string{
		`		return "@:>"`,
		`		return "@:< <name>"`,
		`	if strings.Contains(line, "@:>") || strings.Contains(line, "@:<") {`,
		`	Reason: "@:; annotates the next line, but that line is itself a marker",`,
		`	src := "// @:> section s
// @:> symbol sym
old
"`,
		"	const open = `@:>`",
		"// The `@:;` marker claims the next line.",
		`// Written "@:>" it is a mention, not a marker.`,
	} {
		m, ok, err := marker.Classify("f.go", 1, line)
		if ok || err != nil {
			t.Errorf("Classify(%q) = (%v, %v, %v); want it treated as ordinary text", line, m, ok, err)
		}
	}
}

// A quoted sigil at the end of a line must not hide a real marker earlier on it,
// which is what taking simply the *last* sigil would do.
func TestAQuotedSigilDoesNotHideARealOne(t *testing.T) {
	m, ok, err := marker.Classify("f.go", 1, `	x := "@:< nope" // @:> section real`)
	if err != nil || !ok {
		t.Fatalf("Classify = (%v, %v, %v)", m, ok, err)
	}
	if m.Op() != marker.Open || m.Name() != "real" {
		t.Errorf("marker = %+v, want the unquoted open", m)
	}
}

// The rule must not weaken what Anno catches. A malformed marker involves no
// quotes, so it still fails loudly — which is the whole reason Anno refuses
// rather than guessing.
func TestAMalformedMarkerStillFails(t *testing.T) {
	for _, line := range []string{
		"// @:> secton foo",
		"// @:> section",
		"// @:< one two",
		"// @:>",
	} {
		if _, ok, err := marker.Classify("f.go", 1, line); err == nil || ok {
			t.Errorf("Classify(%q) should still be a parse error", line)
		}
	}
}

// A marker at the end of a line of code is the documented form, and a string
// earlier on that line — closed before the marker — must not disturb it.
func TestATrailingMarkerAfterCodeStillWorks(t *testing.T) {
	for _, line := range []string{
		`	Pair struct { // @:> symbol Pair`,
		`	x := "hello" // @:> symbol greeting`,
		"	y := `raw` // @:> symbol y",
		`	z := "a \" b" // @:> symbol z`, // an escaped quote inside the string
	} {
		m, ok, err := marker.Classify("f.go", 1, line)
		if err != nil || !ok {
			t.Errorf("Classify(%q) = (%v, %v, %v); want a marker", line, m, ok, err)
		}
	}
}

// The single quote is not a delimiter here: it is an apostrophe far more often,
// and treating one as an open string would hide every marker after it.
func TestAnApostropheIsNotAQuote(t *testing.T) {
	m, ok, err := marker.Classify("f.go", 1, `	// don't do this // @:> section x`)
	if err != nil || !ok {
		t.Fatalf("Classify = (%v, %v, %v)", m, ok, err)
	}
	if m.Name() != "x" {
		t.Errorf("marker = %+v", m)
	}
}

func FuzzClassify(f *testing.F) {
	for _, seed := range []string{
		"// @:> section data",
		"// @:; symbol x [a b]",
		"// @:< x",
		"@:>",
		"@:> @:< @:;",
		"// @:> section [",
		"x",
		"",
		"@:>\x00",
		`return "@:>"`,
		"`@:>` in prose",
		`x := "@:< a" // @:> section b`,
		"// @:> section " + strings.Repeat("a", 500),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, line string) {
		m, ok, err := marker.Classify("f.go", 1, line)
		if err != nil && ok {
			t.Fatalf("Classify(%q) returned both a marker and an error", line)
		}
		if err != nil {
			if !errors.Is(err, fault.ErrParse) && !errors.Is(err, fault.ErrInternal) {
				t.Fatalf("Classify(%q) returned an unclassified error: %v", line, err)
			}
			return
		}
		if !ok {
			return
		}
		if m.Name() == "" {
			t.Fatalf("Classify(%q) produced a marker with no name", line)
		}
		if m.Op() != marker.Close && !m.Kind().Valid() {
			t.Fatalf("Classify(%q) produced an invalid kind", line)
		}
		// A canonical rendering must itself re-parse to the same marker.
		again, ok2, err2 := marker.Classify("f.go", 1, m.String())
		if err2 != nil || !ok2 {
			t.Fatalf("re-parsing %q from %q failed: %v", m.String(), line, err2)
		}
		if again.String() != m.String() {
			t.Fatalf("rendering is not stable: %q then %q", m.String(), again.String())
		}
	})
}
