package fault_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"orc/common/fault"
)

// TestEveryFaultUnwrapsToItsSentinel is the property every tool's exit code
// depends on. If a fault type stops matching its sentinel, commands start
// exiting zero on failure, and nothing else in any suite would notice.
func TestEveryFaultUnwrapsToItsSentinel(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{"parse", fault.Parse{Path: "a", Line: 2, Reason: "bad"}, fault.ErrParse},
		{"query", fault.Query{Query: "a=b", Col: 2, Reason: "bad"}, fault.ErrParse},
		{"unbalanced", fault.Unbalanced{Path: "a", Line: 1, Name: "x"}, fault.ErrUnbalanced},
		{"not found", fault.NotFound{Target: "x"}, fault.ErrNotFound},
		{"ambiguous", fault.Ambiguous{Target: "x"}, fault.ErrAmbiguous},
		{"io", fault.IO{Op: "read", Path: "p", Err: errors.New("boom")}, fault.ErrIO},
		{"conflict", fault.Conflict{Path: "p"}, fault.ErrConflict},
		{"auth", fault.Auth{Reason: "nope"}, fault.ErrAuth},
		{"denied", fault.Denied{Actor: "bob", Action: "claim", Target: "t"}, fault.ErrDenied},
		{"scope", fault.Scope{Path: "a.go", Task: "t"}, fault.ErrScope},
		{"escape", fault.Escape{Path: "../x", Root: "/r"}, fault.ErrEscape},
		{"usage", fault.Usage{Reason: "nope"}, fault.ErrUsage},
		{"internal", fault.Internal{Where: "w", Detail: "d"}, fault.ErrInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !errors.Is(tc.err, tc.want) {
				t.Errorf("%T does not unwrap to %v", tc.err, tc.want)
			}
			if tc.err.Error() == "" {
				t.Errorf("%T renders an empty message", tc.err)
			}
			// Wrapping must not lose the classification: every fault travels up
			// through at least one %w before a command sees it.
			wrapped := fmt.Errorf("while doing a thing: %w", tc.err)
			if !errors.Is(wrapped, tc.want) {
				t.Errorf("%T loses its sentinel when wrapped", tc.err)
			}
			// And it must still map to the same code once wrapped.
			if got, want := fault.Code(wrapped), fault.Code(tc.err); got != want {
				t.Errorf("wrapping changed the exit code: %d then %d", want, got)
			}
		})
	}
}

func TestSentinelsAreDistinct(t *testing.T) {
	all := fault.Sentinels()
	for i := range all {
		for j := i + 1; j < len(all); j++ {
			if errors.Is(all[i], all[j]) {
				t.Errorf("sentinel %v matches %v", all[i], all[j])
			}
		}
	}
}

// TestTheCodeTableIsTotal: every sentinel in the vocabulary must map to a
// defined code, and none may fall through to the internal default by accident.
func TestTheCodeTableIsTotal(t *testing.T) {
	defined := map[int]string{
		fault.CodeOK: "ok", fault.CodeUsage: "usage", fault.CodeNotFound: "not found",
		fault.CodeAmbiguous: "ambiguous", fault.CodeParse: "parse", fault.CodeIO: "i/o",
		fault.CodeConflict: "conflict", fault.CodeAuth: "auth", fault.CodeDenied: "denied",
		fault.CodeScope: "scope", fault.CodeUnavailable: "unavailable",
		fault.CodeEscape: "escape", fault.CodeInternal: "internal",
	}
	for _, s := range fault.Sentinels() {
		code := fault.Code(s)
		if _, ok := defined[code]; !ok {
			t.Errorf("%v maps to undefined code %d", s, code)
		}
		if s != fault.ErrInternal && code == fault.CodeInternal {
			t.Errorf("%v fell through to the internal code", s)
		}
	}
}

// TestCodesAreStable pins the numbers themselves. Hooks and shell scripts
// branch on these, so changing one is a breaking change to everything
// downstream and should have to break this test first.
func TestCodesAreStable(t *testing.T) {
	for _, tc := range []struct {
		code int
		want int
	}{
		{fault.CodeOK, 0}, {fault.CodeUsage, 1}, {fault.CodeNotFound, 2},
		{fault.CodeAmbiguous, 3}, {fault.CodeParse, 4}, {fault.CodeIO, 5},
		{fault.CodeConflict, 6}, {fault.CodeAuth, 7}, {fault.CodeDenied, 8},
		{fault.CodeScope, 9}, {fault.CodeUnavailable, 10}, {fault.CodeEscape, 11},
		{fault.CodeInternal, 70},
	} {
		if tc.code != tc.want {
			t.Errorf("an exit code moved: got %d, want %d", tc.code, tc.want)
		}
	}
}

// TestUnclassifiedErrorsAreInternal: a tool that returns an error outside the
// vocabulary has a hole in it, and exiting 1 would hide that behind what looks
// like a user mistake.
func TestUnclassifiedErrorsAreInternal(t *testing.T) {
	if got := fault.Code(errors.New("who knows")); got != fault.CodeInternal {
		t.Errorf("an unclassified error mapped to %d, want %d", got, fault.CodeInternal)
	}
	if got := fault.Code(nil); got != fault.CodeOK {
		t.Errorf("Code(nil) = %d, want 0", got)
	}
}

// TestInternalOutranksEverything: a bug wrapped in something friendlier still
// has to report as a bug.
func TestInternalOutranksEverything(t *testing.T) {
	bug := fault.Internal{Where: "w", Detail: "d"}
	for _, wrapper := range []error{
		fmt.Errorf("%w: %w", fault.ErrUsage, bug),
		fmt.Errorf("%w: %w", fault.ErrNotFound, bug),
		fmt.Errorf("%w: %w", fault.ErrIO, bug),
	} {
		if got := fault.Code(wrapper); got != fault.CodeInternal {
			t.Errorf("a wrapped bug mapped to %d, want %d", got, fault.CodeInternal)
		}
	}
}

// TestScopeAndEscapeAreDistinct. An ordinary scope refusal is routine; an
// escape is a containment failure, and in a probe it is the one thing a monitor
// should alarm on. Sharing a code would make them indistinguishable to the hook
// that has to tell them apart, so they are separate in both the vocabulary and
// the numbering.
func TestScopeAndEscapeAreDistinct(t *testing.T) {
	if fault.Code(fault.Scope{}) != fault.CodeScope {
		t.Error("Scope should map to the scope code")
	}
	if fault.Code(fault.Escape{}) != fault.CodeEscape {
		t.Error("Escape should map to the escape code")
	}
	if fault.CodeScope == fault.CodeEscape {
		t.Error("scope and escape codes should differ")
	}
	if errors.Is(fault.ErrScope, fault.ErrEscape) {
		t.Error("Scope and Escape should remain separate sentinels")
	}
}

// TestUnavailableIsNotIO. cq is the tool that has to tell a dead network from a
// bad disk, and the two are fixed by different people.
func TestUnavailableIsNotIO(t *testing.T) {
	if got := fault.Code(fault.Unavailable{Peer: "cq.example"}); got != fault.CodeUnavailable {
		t.Errorf("Unavailable mapped to %d, want %d", got, fault.CodeUnavailable)
	}
	if errors.Is(fault.ErrUnavailable, fault.ErrIO) {
		t.Error("Unavailable should not unwrap to i/o")
	}
	// The underlying cause stays reachable.
	cause := errors.New("dial tcp: refused")
	if !errors.Is(fault.Unavailable{Peer: "p", Err: cause}, cause) {
		t.Error("Unavailable should unwrap to its cause")
	}
}

func TestParsePosition(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  fault.Parse
		want string
	}{
		{"path only", fault.Parse{Path: "a.msg", Reason: "bad"}, "a.msg: bad"},
		{"with line", fault.Parse{Path: "a.msg", Line: 4, Reason: "bad"}, "a.msg:4: bad"},
		{"with column", fault.Parse{Path: "a.msg", Line: 4, Col: 9, Reason: "bad"}, "a.msg:4:9: bad"},
		{"no path", fault.Parse{Line: 4, Reason: "bad"}, "<input>:4: bad"},
		{"no reason", fault.Parse{Path: "a.msg"}, "a.msg: malformed data"},
		// A column without a line has no meaning and must not be printed alone.
		{"column without line", fault.Parse{Path: "a.msg", Col: 9, Reason: "bad"}, "a.msg: bad"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestQueryDrawsACaret(t *testing.T) {
	err := fault.Query{Query: `from="boss" & `, Col: 15, Reason: "expected a field name"}
	want := "bad query at column 15: expected a field name\n  from=\"boss\" & \n                ^"
	if got := err.Error(); got != want {
		t.Errorf("Error() =\n%s\nwant\n%s", got, want)
	}

	// A column past the end must not index out of the string.
	if got := (fault.Query{Query: "ab", Col: 99, Reason: "x"}).Error(); !strings.Contains(got, "^") {
		t.Errorf("an overrun column lost its caret: %q", got)
	}
	if got := (fault.Query{Query: "ab", Reason: "x"}).Error(); strings.Contains(got, "\n") {
		t.Errorf("a query with no column should be one line: %q", got)
	}
}

// TestAuthKeepsDetailOutOfTheMessage is a security property, not a formatting
// one: the visible text must not distinguish "no such user" from "wrong key".
func TestAuthKeepsDetailOutOfTheMessage(t *testing.T) {
	err := fault.Auth{Reason: "authentication failed", Detail: "no such user bob"}
	if got := err.Error(); got != "authentication failed" {
		t.Errorf("Error() = %q, want the reason alone", got)
	}
	if !strings.Contains(err.Detail, "bob") {
		t.Error("Detail should still carry the cause for logs and tests")
	}
	if got := (fault.Auth{}).Error(); got != "authentication failed" {
		t.Errorf("zero Auth reads %q", got)
	}
}

// TestDeniedRedirects: a refusal that ends the conversation is worse than one
// that says who to ask.
func TestDeniedRedirects(t *testing.T) {
	got := fault.Denied{Actor: "bob", Action: "scope", Target: "fix-parser", Owner: "alice"}.Error()
	for _, want := range []string{"bob", "scope", "fix-parser", "alice"} {
		if !strings.Contains(got, want) {
			t.Errorf("message %q should name %q", got, want)
		}
	}

	// With no owner the message must not claim one.
	unowned := fault.Denied{Actor: "bob", Action: "scope", Target: "t", Reason: "claim it first"}.Error()
	if strings.Contains(unowned, "belongs to") {
		t.Errorf("an unowned target should not report an owner: %q", unowned)
	}
	if !strings.Contains(unowned, "claim it first") {
		t.Errorf("the reason should survive: %q", unowned)
	}
	// The zero value still reads as a sentence.
	if got := (fault.Denied{}).Error(); !strings.Contains(got, "may not") {
		t.Errorf("zero Denied reads %q", got)
	}
}

// TestScopeShowsWhatIsAllowed: an agent told only that it may not touch a file
// has to guess; an agent shown the scope can act.
func TestScopeShowsWhatIsAllowed(t *testing.T) {
	got := fault.Scope{
		Path: "internal/render/render.go", Task: "fix-parser",
		InScope: []string{"internal/tree/", "cmd/anno/main.go"},
	}.Error()
	for _, want := range []string{"internal/render/render.go", "fix-parser", "in scope", "internal/tree/"} {
		if !strings.Contains(got, want) {
			t.Errorf("message %q should contain %q", got, want)
		}
	}
	// With no scope listed there is no empty "in scope:" line.
	if got := (fault.Scope{Path: "a", Task: "t"}).Error(); strings.Contains(got, "in scope") {
		t.Errorf("an empty scope should not be announced: %q", got)
	}
}

func TestIOUnwrapsToBothSentinelAndCause(t *testing.T) {
	cause := errors.New("disk on fire")
	err := fault.IO{Op: "read", Path: "/x", Err: cause}
	if !errors.Is(err, fault.ErrIO) || !errors.Is(err, cause) {
		t.Error("IO should reach both its sentinel and its cause")
	}
	if got := (fault.IO{Op: "read", Path: "/x"}).Error(); strings.Contains(got, "nil") {
		t.Errorf("a nil cause should not print: %q", got)
	}
}

func TestListErrorsRenderCandidates(t *testing.T) {
	amb := fault.Ambiguous{Target: "x", Candidates: []string{"a", "b"}}
	for _, want := range []string{"x", "2 matches", "a", "b"} {
		if !strings.Contains(amb.Error(), want) {
			t.Errorf("Error() %q should contain %q", amb, want)
		}
	}
	nf := fault.NotFound{Target: "bos", Near: []string{"boss"}}
	if !strings.Contains(nf.Error(), "did you mean") {
		t.Errorf("Error() %q should suggest the near miss", nf)
	}
	if strings.Contains((fault.NotFound{Target: "x"}).Error(), "did you mean") {
		t.Error("an empty suggestion list should not be offered")
	}
}

func TestUnbalancedListsOpenNames(t *testing.T) {
	err := fault.Unbalanced{Path: "a.go", Line: 9, Name: "body", Open: []string{"code", "operate"}}
	for _, want := range []string{"a.go:9", "body", "code", "operate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Error() %q should contain %q", err, want)
		}
	}
	if strings.Contains((fault.Unbalanced{Path: "a", Line: 1, Name: "x"}).Error(), "open here") {
		t.Error("with nothing open, the parenthetical should be absent")
	}
}

func TestCheckReportsRatherThanAborts(t *testing.T) {
	if err := fault.Check(true, "w", "should not fire"); err != nil {
		t.Errorf("Check(true) = %v, want nil", err)
	}
	err := fault.Check(false, "pkg.Thing", "count is %d, want %d", 3, 4)
	if !errors.Is(err, fault.ErrInternal) {
		t.Fatalf("Check(false) = %v, want an internal fault", err)
	}
	for _, want := range []string{"pkg.Thing", "count is 3, want 4", "bug"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q should contain %q", err, want)
		}
	}
}

func TestConflictAndUsageDefaults(t *testing.T) {
	if !strings.Contains((fault.Conflict{Path: "p"}).Error(), "p") {
		t.Error("Conflict without a reason should still name the path")
	}
	if got := (fault.Usage{}).Error(); got != "invalid usage" {
		t.Errorf("zero Usage reads %q", got)
	}
	if !strings.Contains((fault.Escape{}).Error(), "resolves outside") {
		t.Error("zero Escape should still read as a sentence")
	}
}
