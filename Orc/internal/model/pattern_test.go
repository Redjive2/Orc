package model_test

import (
	"errors"
	"testing"

	"orc/common/fault"
	"orc/orc/internal/model"
)

func mustPattern(t *testing.T, raw string) model.Pattern {
	t.Helper()
	p, err := model.ParsePattern(raw)
	if err != nil {
		t.Fatalf("pattern %q: %v", raw, err)
	}
	return p
}

// TestParsePattern: the forms a permission is written in, and the ones that are
// refused rather than guessed at.
func TestParsePattern(t *testing.T) {
	good := map[string]string{
		"read(Anno/**)":            "read(Anno/**)",
		"write(Anno/internal/**)":  "write(Anno/internal/**)",
		"read(Anno/)":              "read(Anno/**)", // a trailing slash means everything under it
		"read(Anno/./internal/**)": "read(Anno/internal/**)",
		"spawn(24)":                "spawn(24)",
		"orc(Assign)":              "orc(assign)", // verbs are lowercased
		" read( Docs/** ) ":        "read(Docs/**)",
	}
	for raw, want := range good {
		if got := mustPattern(t, raw); got.String() != want {
			t.Errorf("%q parsed to %s, want %s", raw, got, want)
		}
	}

	bad := map[string]error{
		"read":             fault.ErrUsage, // no parentheses: the meaning would be a guess
		"read()":           fault.ErrUsage,
		"fly(Anno/**)":     fault.ErrUsage,
		"spawn(lots)":      fault.ErrUsage,
		"spawn(99999)":     fault.ErrUsage,
		"orc(a/b)":         fault.ErrUsage,
		"read(/etc/**)":    fault.ErrEscape, // absolute paths mean something else on every machine
		"write(../**)":     fault.ErrEscape,
		"read(Anno/../**)": fault.ErrEscape,
		"read(~/secrets)":  fault.ErrEscape,
	}
	for raw, want := range bad {
		_, err := model.ParsePattern(raw)
		if err == nil {
			t.Errorf("%q was accepted", raw)
			continue
		}
		if !errors.Is(err, want) {
			t.Errorf("%q gave %v, want %v", raw, err, want)
		}
	}
}

// TestMatches: the glob semantics a permission is written against, with ** crossing
// directories and * not.
func TestMatches(t *testing.T) {
	cases := []struct {
		pattern, target string
		want            bool
	}{
		{"read(**)", "anything/at/all.go", true},
		{"read(Anno/**)", "Anno/internal/tree/tree.go", true},
		{"read(Anno/**)", "Anno", true}, // ** covers zero segments, so the root itself
		{"read(Anno/**)", "Common/user/user.go", false},
		{"read(Anno/*)", "Anno/main.go", true},
		{"read(Anno/*)", "Anno/internal/tree.go", false}, // * stops at a separator
		{"read(Anno/*/render/**)", "Anno/internal/render/table.go", true},
		{"read(Anno/*/render/**)", "Anno/render/table.go", false},
		{"read(Docs/Orc/Vision.md)", "Docs/Orc/Vision.md", true},
		{"read(Docs/Orc/Vision.md)", "Docs/Orc/Reference.md", false},
		{"read(Anno/**)", "../Anno/x.go", false}, // an escaping target matches nothing
		{"spawn(24)", "Anno/x.go", false},        // a budget is not a path
	}
	for _, c := range cases {
		if got := mustPattern(t, c.pattern).Matches(c.target); got != c.want {
			t.Errorf("%s matches %q = %v, want %v", c.pattern, c.target, got, c.want)
		}
	}
}

// TestContains is the decision the whole permission model rests on: a child's clause
// survives only if the boss's provably covers it.
//
// The cases below include the conservative ones — overlapping globs where neither
// provably contains the other — and they answer false, which is the fail-closed
// direction.
func TestContains(t *testing.T) {
	cases := []struct {
		boss, child string
		want        bool
	}{
		{"read(**)", "read(Anno/**)", true},
		{"read(**)", "read(Docs/Orc/Vision.md)", true},
		{"read(Anno/**)", "read(Anno/internal/**)", true},
		{"read(Anno/**)", "read(Anno/internal/tree.go)", true},
		{"read(Anno/**)", "read(Anno/**)", true},
		{"read(Anno/internal/**)", "read(Anno/**)", false},
		{"read(Anno/**)", "read(Common/**)", false},
		{"read(Anno/**)", "write(Anno/**)", false}, // kinds never mix
		{"spawn(24)", "spawn(8)", true},
		{"spawn(8)", "spawn(24)", false},
		{"orc(assign)", "orc(assign)", true},
		{"orc(assign)", "orc(remove)", false},

		// Conservative: a pattern with a wildcard in the middle cannot be proven to
		// contain another, so it does not.
		{"read(*/internal/**)", "read(Anno/*/render/**)", false},
		{"read(Anno/*)", "read(Anno/internal/**)", false},
	}
	for _, c := range cases {
		if got := mustPattern(t, c.boss).Contains(mustPattern(t, c.child)); got != c.want {
			t.Errorf("%s contains %s = %v, want %v", c.boss, c.child, got, c.want)
		}
	}
}

// TestNarrow: either side may be the narrower one, and an unprovable pair survives as
// nothing at all.
func TestNarrow(t *testing.T) {
	cases := []struct {
		child, boss, want string
	}{
		{"write(Common/**)", "write(Common/user/**)", "write(Common/user/**)"},
		{"write(Common/user/store.go)", "write(Common/**)", "write(Common/user/store.go)"},
		{"spawn(24)", "spawn(8)", "spawn(8)"},
		{"read(Anno/**)", "read(Common/**)", ""},
	}
	for _, c := range cases {
		got, ok := model.Narrow(mustPattern(t, c.child), mustPattern(t, c.boss))
		if c.want == "" {
			if ok {
				t.Errorf("%s under %s survived as %s, want nothing", c.child, c.boss, got)
			}
			continue
		}
		if !ok || got.String() != c.want {
			t.Errorf("%s under %s = %s (%v), want %s", c.child, c.boss, got, ok, c.want)
		}
	}
}

// TestPermissionFloorCannotBeTheOperators: a floor of 100 would put a permission out
// of every role's reach, which is a permission that exists and can never be used.
func TestPermissionFloorCannotBeTheOperators(t *testing.T) {
	name, err := model.ParseName("impossible")
	if err != nil {
		t.Fatalf("name: %v", err)
	}
	patterns, err := model.ParsePatterns([]string{"read(**)"})
	if err != nil {
		t.Fatalf("patterns: %v", err)
	}
	if _, err := model.NewPermission(name, model.OperatorAuthority(), patterns, epoch); err == nil {
		t.Errorf("a permission with the operator's floor was accepted")
	}
}

// TestRoleCannotClaimOperatorAuthority: there is one operator, and it is not a job
// anybody can be given.
func TestRoleCannotClaimOperatorAuthority(t *testing.T) {
	name, err := model.ParseName("god")
	if err != nil {
		t.Fatalf("name: %v", err)
	}
	if _, err := model.NewRole(name, model.OperatorAuthority(), "everything", epoch); err == nil {
		t.Errorf("a role with authority 100 was accepted")
	}
	if _, err := model.ParseAuthority("100"); err != nil {
		// Parsing 100 is allowed — bootstrap needs it — and the refusal belongs to
		// the things that hand authority out.
		t.Errorf("authority 100 should parse: %v", err)
	}
	if _, err := model.ParseAuthority("0"); err == nil {
		t.Errorf("authority 0 was accepted")
	}
	if _, err := model.ParseAuthority("101"); err == nil {
		t.Errorf("authority 101 was accepted")
	}
}
