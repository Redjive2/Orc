package anno_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/dock/internal/anno"
)

// recorder stands in for the binary, so every path is reachable without
// arranging a process.
type recorder struct {
	stdout string
	stderr string
	code   int
	err    error
	calls  [][]string
}

func (r *recorder) Run(_ context.Context, args ...string) (string, string, int, error) {
	r.calls = append(r.calls, args)
	return r.stdout, r.stderr, r.code, r.err
}

func TestVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rec    recorder
		want   anno.Verdict
		reason string
	}{
		{"exists", recorder{stdout: "func Operate() {}\n", code: fault.CodeOK}, anno.Exists, ""},
		{"missing", recorder{stderr: "anno: no annotation matches \"x.go:Nope\"\n", code: fault.CodeNotFound},
			anno.Missing, "no annotation matches"},
		{"ambiguous", recorder{stderr: "anno: ambiguous target \"x.go^decls\" — 2 matches:\n  x.go@a:B^decls <1:4>\n  x.go@c:D^decls <9:12>\n", code: fault.CodeAmbiguous},
			anno.Ambiguous, "ambiguous target"},
		{"unparseable file", recorder{stderr: "anno: x.go:4: close of \"a\" matches no open annotation\n", code: fault.CodeParse},
			anno.Unknown, "matches no open annotation"},
		{"anno's own io failure", recorder{stderr: "anno: read x.go: permission denied\n", code: fault.CodeIO},
			anno.Unknown, "permission denied"},
		{"could not run", recorder{err: errors.New("fork/exec: no such file")}, anno.Unknown, "could not be run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := tc.rec
			got := anno.NewWith(&rec).Check("x.go:Target")
			if got.Verdict != tc.want {
				t.Errorf("verdict = %v, want %v (why: %q)", got.Verdict, tc.want, got.Why)
			}
			if tc.reason != "" && !strings.Contains(got.Why, tc.reason) {
				t.Errorf("why = %q, want it to mention %q", got.Why, tc.reason)
			}
			if got.Verdict != anno.Exists && got.Why == "" {
				t.Error("a non-exists verdict carries no reason")
			}
		})
	}
}

// TestAMissingBinaryIsUnknownNotMissing is the rule the package exists for.
// Reporting a link as broken because the tool that resolves it is absent would
// send someone to fix a document that is correct.
func TestAMissingBinaryIsUnknownNotMissing(t *testing.T) {
	var zero anno.Tool
	if zero.Available() {
		t.Error("the zero tool claims to be available")
	}
	got := zero.Check("x.go:Operate")
	if got.Verdict != anno.Unknown {
		t.Errorf("verdict = %v, want unknown", got.Verdict)
	}
	if !strings.Contains(got.Why, "PATH") {
		t.Errorf("why = %q, want it to say anno is missing", got.Why)
	}
}

func TestReadReturnsContentAndCheckDoesNot(t *testing.T) {
	const body = "func Operate() {}\n"
	rec := recorder{stdout: body, code: fault.CodeOK}

	if got := anno.NewWith(&rec).Read("x.go:Operate"); got.Content != body {
		t.Errorf("Read content = %q, want %q", got.Content, body)
	}
	// Check asks the same question but wants only the status, so it must not
	// carry a whole annotation's text into a report.
	if got := anno.NewWith(&rec).Check("x.go:Operate"); got.Content != "" {
		t.Errorf("Check returned content: %q", got.Content)
	}
}

func TestCandidatesAreExtracted(t *testing.T) {
	rec := recorder{
		code: fault.CodeAmbiguous,
		stderr: "anno: ambiguous target \"example.go^declarations\" — 2 matches:\n" +
			"  example.go@code:Operate^declarations      <25:28>\n" +
			"  example.go@code:Reduce^declarations       <41:44>\n",
	}
	got := anno.NewWith(&rec).Check("example.go^declarations")
	want := []string{
		"example.go@code:Operate^declarations",
		"example.go@code:Reduce^declarations",
	}
	if len(got.Candidates) != len(want) {
		t.Fatalf("got %d candidates, want %d: %q", len(got.Candidates), len(want), got.Candidates)
	}
	for i := range want {
		if got.Candidates[i] != want[i] {
			t.Errorf("candidate %d = %q, want %q", i, got.Candidates[i], want[i])
		}
	}
}

func TestTheTargetIsPassedThroughVerbatim(t *testing.T) {
	rec := recorder{code: fault.CodeOK}
	anno.NewWith(&rec).Check("../code/example.go@code:Operate^declarations")
	if len(rec.calls) != 1 {
		t.Fatalf("made %d calls, want 1", len(rec.calls))
	}
	want := []string{"read", "../code/example.go@code:Operate^declarations"}
	for i := range want {
		if rec.calls[0][i] != want[i] {
			t.Errorf("arg %d = %q, want %q", i, rec.calls[0][i], want[i])
		}
	}
}

func TestEmptyTargetIsRefusedWithoutRunning(t *testing.T) {
	rec := recorder{code: fault.CodeOK}
	got := anno.NewWith(&rec).Check("  ")
	if got.Verdict != anno.Unknown {
		t.Errorf("verdict = %v, want unknown", got.Verdict)
	}
	if len(rec.calls) != 0 {
		t.Errorf("ran anno for an empty target: %v", rec.calls)
	}
}

// TestADeadlineIsSet: a subprocess that hangs must not hang a check over a
// whole corpus.
func TestADeadlineIsSet(t *testing.T) {
	var seen bool
	anno.NewWith(runnerFunc(func(ctx context.Context, args ...string) (string, string, int, error) {
		if _, ok := ctx.Deadline(); ok {
			seen = true
		}
		return "", "", 0, nil
	})).Check("x.go:A")
	if !seen {
		t.Error("no deadline was set on the call")
	}
}

type runnerFunc func(context.Context, ...string) (string, string, int, error)

func (f runnerFunc) Run(ctx context.Context, args ...string) (string, string, int, error) {
	return f(ctx, args...)
}
