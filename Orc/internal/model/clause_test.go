package model_test

import (
	"errors"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/orc/internal/model"
)

// A clause is a list of what it allows and, optionally, a list of what it takes
// back out. These are the properties that make it safe to write one: the parse is
// canonical, the exception always wins, and containment stays conservative in the
// direction that fails closed.

func TestAListIsOneClause(t *testing.T) {
	good := map[string]string{
		"read(Anno/** Dock/**)":        "read(Anno/** Dock/**)",
		"read(Dock/** Anno/**)":        "read(Anno/** Dock/**)", // sorted, so order is not meaning
		"orc(new assign)":              "orc(assign new)",
		"orc(New ASSIGN)":              "orc(assign new)",
		"read(  Anno/**   Dock/**  )":  "read(Anno/** Dock/**)",
		"write(** except Docs/**)":     "write(** except Docs/**)",
		"write(** EXCEPT Docs/**)":     "write(** except Docs/**)",
		"read(** except .git/** b/**)": "read(** except .git/** b/**)",
		"orc(** except remove)":        "orc(** except remove)",
		"tool(**)":                     "tool(**)",
		"read(Anno/ Dock/)":            "read(Anno/** Dock/**)",
	}
	for raw, want := range good {
		p, err := model.ParsePattern(raw)
		if err != nil {
			t.Errorf("%q: %v", raw, err)
			continue
		}
		if got := p.String(); got != want {
			t.Errorf("%q parsed to %q, want %q", raw, got, want)
		}
	}
}

func TestTheShapesAClauseRefuses(t *testing.T) {
	bad := map[string]string{
		"read(a/** except)":                  "an except with nothing after it",
		"read(except a/**)":                  "an except with nothing before it",
		"read(a/** except b/** except c/**)": "two excepts",
		"read(a/** a/**)":                    "a repeated term",
		"read(Anno/ Anno/**)":                "two spellings of one term",
		"spawn(24 48)":                       "a budget that is a list",
		"spawn(** except 4)":                 "a budget with an exception",
		"orc(assign/grant)":                  "a slash in a verb",
		"read()":                             "nothing at all",
	}
	for raw, why := range bad {
		if p, err := model.ParsePattern(raw); err == nil {
			t.Errorf("%q (%s) parsed to %s, want a refusal", raw, why, p)
		}
	}
}

// The message for a budget says why, because "invalid" would leave somebody
// trying `spawn(24 48)` with nowhere to go.
func TestABudgetSaysWhyItIsNotAList(t *testing.T) {
	_, err := model.ParsePattern("spawn(24 48)")
	if err == nil {
		t.Fatal("spawn(24 48) parsed")
	}
	var usage fault.Usage
	if !errors.As(err, &usage) {
		t.Fatalf("want a usage fault, got %T", err)
	}
	if !strings.Contains(usage.Reason, "number") {
		t.Errorf("the refusal does not say a budget is a number: %s", usage.Reason)
	}
}

func TestATermIsAllowedAndAnExceptionTakesItBack(t *testing.T) {
	p := mustPattern(t, "write(** except Docs/** .git/**)")
	for _, allowed := range []string{"Orc/internal/cli/cli.go", "README.md", "Documentation/x"} {
		if !p.Matches(allowed) {
			t.Errorf("%s should be allowed by %s", allowed, p)
		}
	}
	for _, refused := range []string{"Docs/Orc/Reference.md", "Docs", ".git/config"} {
		if p.Matches(refused) {
			t.Errorf("%s should be taken back out by %s", refused, p)
		}
	}
}

// The exception wins wherever it lands, including over a term that names the very
// thing it excludes. Anything else and `read(Docs/** except Docs/secret/**)` would
// depend on which list was consulted first.
func TestAnExceptionBeatsAnOverlappingTerm(t *testing.T) {
	p := mustPattern(t, "read(Docs/** except Docs/secret/**)")
	if !p.Matches("Docs/Orc/Reference.md") {
		t.Error("the term stopped allowing what the exception does not name")
	}
	if p.Matches("Docs/secret/keys.md") {
		t.Error("the exception did not win")
	}
}

func TestAListMatchesAnyOfItsTerms(t *testing.T) {
	p := mustPattern(t, "orc(new assign)")
	for _, verb := range []string{"new", "assign"} {
		if !p.Matches(verb) {
			t.Errorf("orc(new assign) does not match %q", verb)
		}
	}
	if p.Matches("remove") {
		t.Error("orc(new assign) matched a verb it does not name")
	}
}

// Globs are not only for paths. A verb clause is a pattern in the same sense a
// path clause is, which is what makes `orc(** except remove)` sayable.
func TestVerbsAndToolsGlobToo(t *testing.T) {
	every := mustPattern(t, "orc(** except remove)")
	if !every.Matches("employ") {
		t.Error("orc(**) does not match a verb")
	}
	if every.Matches("remove") {
		t.Error("the exception did not hold for a verb")
	}
	if !mustPattern(t, "tool(**)").Matches("upgrade") {
		t.Error("tool(**) does not match a capability")
	}
	if !mustPattern(t, "orc(re*)").Matches("refresh") {
		t.Error("a wildcard verb does not match")
	}
}

func TestContainmentOverLists(t *testing.T) {
	cases := []struct {
		wide, narrow string
		want         bool
	}{
		{"read(Anno/** Dock/**)", "read(Anno/**)", true},
		{"read(Anno/**)", "read(Anno/** Dock/**)", false},
		{"read(**)", "read(Anno/** Dock/**)", true},
		{"orc(new assign remove)", "orc(assign)", true},
		{"orc(assign)", "orc(new assign)", false},
		// An exception that cannot reach anything the narrower clause allows does
		// not stop containment: .git and Anno diverge at their first segment.
		{"read(** except .git/**)", "read(Anno/**)", true},
		// One that can, does.
		{"read(** except Anno/internal/**)", "read(Anno/**)", false},
		// Unless the narrower clause already excludes at least as much.
		{"read(** except Anno/internal/**)", "read(Anno/** except Anno/internal/**)", true},
		{"read(** except Anno/**)", "read(Anno/** except Anno/internal/**)", false},
	}
	for _, c := range cases {
		got := mustPattern(t, c.wide).Contains(mustPattern(t, c.narrow))
		if got != c.want {
			t.Errorf("%s contains %s = %v, want %v", c.wide, c.narrow, got, c.want)
		}
	}
}

// The reason lists are worth having: a child allowed two directories under a boss
// allowed one keeps the one. Before lists it kept neither or both.
func TestNarrowingKeepsTheTermsThatSurvive(t *testing.T) {
	child := mustPattern(t, "write(Anno/** Dock/** Orc/**)")
	boss := mustPattern(t, "write(Anno/** Dock/internal/**)")

	got, ok := model.Narrow(child, boss)
	if !ok {
		t.Fatal("nothing survived a narrowing that should keep two terms")
	}
	if want := "write(Anno/** Dock/internal/**)"; got.String() != want {
		t.Errorf("narrowed to %s, want %s", got, want)
	}
}

// Neither party asked for the other's exceptions, and taking more out is the safe
// direction, so a clause narrowed term by term carries both.
func TestNarrowingKeepsEveryException(t *testing.T) {
	child := mustPattern(t, "read(Anno/** Dock/** except Anno/vendor/**)")
	boss := mustPattern(t, "read(Anno/** Orc/** except Orc/legacy/**)")

	got, ok := model.Narrow(child, boss)
	if !ok {
		t.Fatal("nothing survived")
	}
	if want := "read(Anno/** except Anno/vendor/** Orc/legacy/**)"; got.String() != want {
		t.Errorf("narrowed to %s, want %s", got, want)
	}
}

// What a narrowing must never do is allow something one of its parents does not.
// The clause it returns may drop an exception — a boss who never allowed `.git`
// has no need of the child's rule about it — but not the refusal behind one.
func TestANarrowedClauseAllowsNothingEitherParentRefuses(t *testing.T) {
	child := mustPattern(t, "read(** except .git/**)")
	boss := mustPattern(t, "read(Anno/** Dock/** except Anno/vendor/**)")

	got, ok := model.Narrow(child, boss)
	if !ok {
		t.Fatal("nothing survived")
	}
	for _, path := range []string{".git/config", "Anno/vendor/x.go", "Orc/internal/cli/cli.go"} {
		if got.Matches(path) && !(child.Matches(path) && boss.Matches(path)) {
			t.Errorf("%s allows %s, which one of its parents refuses", got, path)
		}
	}
	if !got.Matches("Anno/internal/x.go") {
		t.Errorf("%s lost what both parents allow", got)
	}
}

func TestNothingSurvivesADisjointNarrowing(t *testing.T) {
	if _, ok := model.Narrow(mustPattern(t, "read(Anno/**)"), mustPattern(t, "read(Dock/**)")); ok {
		t.Error("two disjoint clauses narrowed to something")
	}
}

// The shell splits on spaces, so a clause with a space in it arrives in pieces
// unless it was quoted. Rejoining is not guessing: an unclosed clause is still an
// error, and it says so.
func TestAClauseTheShellTookApartIsPutBackTogether(t *testing.T) {
	got, err := model.ParsePatterns([]string{"read(Anno/**", "Dock/**)", "spawn(24)"})
	if err != nil {
		t.Fatalf("rejoining: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d clauses, want 2: %v", len(got), model.PatternStrings(got))
	}
	if got[0].String() != "read(Anno/** Dock/**)" {
		t.Errorf("rejoined to %s", got[0])
	}
}

func TestAnUnclosedClauseIsRefusedWithItsOwnMessage(t *testing.T) {
	_, err := model.ParsePatterns([]string{"read(Anno/**", "Dock/**"})
	if err == nil {
		t.Fatal("an unclosed clause parsed")
	}
	if !strings.Contains(err.Error(), "quoting") {
		t.Errorf("the refusal does not say how to fix it: %v", err)
	}
}

func TestTermsAndExceptsComeBackApart(t *testing.T) {
	p := mustPattern(t, "read(a/** b/** except c/**)")
	if got := strings.Join(p.Terms(), ","); got != "a/**,b/**" {
		t.Errorf("terms are %q", got)
	}
	if got := strings.Join(p.Excepts(), ","); got != "c/**" {
		t.Errorf("excepts are %q", got)
	}
	budget := mustPattern(t, "spawn(24)")
	if len(budget.Terms()) != 0 || len(budget.Excepts()) != 0 {
		t.Error("a budget reported terms; it is a number")
	}
}

func contains(list []string, want string) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}
