package fault_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"orc/cq/internal/fault"
)

var sentinels = []error{
	fault.ErrUsage, fault.ErrNotFound, fault.ErrAmbiguous, fault.ErrParse,
	fault.ErrIO, fault.ErrConflict, fault.ErrUnauthenticated,
	fault.ErrUnavailable, fault.ErrInternal,
}

// samples pairs each fault type with the sentinel it must unwrap to.
var samples = []struct {
	err  error
	want error
	code fault.Code
}{
	{fault.Parse{Where: "f", Line: 2, Reason: "r"}, fault.ErrParse, fault.CodeParse},
	{fault.Usage{Reason: "r"}, fault.ErrUsage, fault.CodeUsage},
	{fault.NotFound{What: "task", Name: "n"}, fault.ErrNotFound, fault.CodeNotFound},
	{fault.Ambiguous{Target: "t", Candidates: []string{"a", "b"}}, fault.ErrAmbiguous, fault.CodeAmbiguous},
	{fault.IO{Op: "read", Subject: "f"}, fault.ErrIO, fault.CodeIO},
	{fault.Conflict{Subject: "f", Reason: "r"}, fault.ErrConflict, fault.CodeConflict},
	{fault.Unauthenticated{Reason: "no token"}, fault.ErrUnauthenticated, fault.CodeUnauthenticated},
	{fault.Unavailable{Peer: "server"}, fault.ErrUnavailable, fault.CodeUnavailable},
	{fault.Internal{Where: "w", Detail: "d"}, fault.ErrInternal, fault.CodeInternal},
}

// TestEachFaultClassifiesToExactlyOneSentinel is the property the exit codes and
// HTTP statuses rest on.
func TestEachFaultClassifiesToExactlyOneSentinel(t *testing.T) {
	for _, s := range samples {
		if !errors.Is(s.err, s.want) {
			t.Errorf("%T does not unwrap to %v", s.err, s.want)
		}
		for _, other := range sentinels {
			if other == s.want {
				continue
			}
			if errors.Is(s.err, other) {
				t.Errorf("%T also unwraps to %v, making its classification ambiguous", s.err, other)
			}
		}
		if got := fault.Classify(s.err); got != s.code {
			t.Errorf("Classify(%T) = %q, want %q", s.err, got, s.code)
		}
		if s.err.Error() == "" {
			t.Errorf("%T renders an empty message", s.err)
		}
	}
}

func TestClassifyTotality(t *testing.T) {
	if got := fault.Classify(nil); got != "" {
		t.Errorf("Classify(nil) = %q, want empty", got)
	}
	if got := fault.Classify(errors.New("unrecognised")); got != fault.CodeInternal {
		t.Errorf("an unrecognised error should classify as internal, got %q", got)
	}
}

func TestExitAndStatusAreTotal(t *testing.T) {
	if got := fault.Exit(nil); got != fault.ExitOK {
		t.Errorf("Exit(nil) = %d, want %d", got, fault.ExitOK)
	}
	if got := fault.Status(nil); got != http.StatusOK {
		t.Errorf("Status(nil) = %d, want 200", got)
	}

	seen := map[int]bool{}
	for _, s := range samples {
		exit := fault.Exit(s.err)
		if exit == fault.ExitOK {
			t.Errorf("%T must not exit zero", s.err)
		}
		seen[exit] = true

		status := fault.Status(s.err)
		if status < 400 || status > 599 {
			t.Errorf("%T maps to status %d, want a 4xx or 5xx", s.err, status)
		}
	}
	if len(seen) < 8 {
		t.Errorf("only %d distinct exit codes are reachable; each fault should be distinguishable", len(seen))
	}

	// An unrecognised error must still be safe on both channels.
	other := errors.New("who knows")
	if fault.Exit(other) != fault.ExitInternal || fault.Status(other) != http.StatusInternalServerError {
		t.Errorf("an unrecognised error should be internal on both channels")
	}
}

func TestStatusMapping(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{fault.Usage{Reason: "r"}, http.StatusBadRequest},
		{fault.Parse{Reason: "r"}, http.StatusBadRequest},
		{fault.Unauthenticated{}, http.StatusUnauthorized},
		{fault.NotFound{What: "x"}, http.StatusNotFound},
		{fault.Conflict{Subject: "s"}, http.StatusConflict},
		{fault.Ambiguous{Target: "t"}, http.StatusConflict},
		{fault.Unavailable{Peer: "p"}, http.StatusServiceUnavailable},
		{fault.Internal{Where: "w"}, http.StatusInternalServerError},
	} {
		if got := fault.Status(tc.err); got != tc.want {
			t.Errorf("Status(%T) = %d, want %d", tc.err, got, tc.want)
		}
	}
}

func TestCodeValidity(t *testing.T) {
	for _, c := range []fault.Code{
		fault.CodeUsage, fault.CodeNotFound, fault.CodeAmbiguous, fault.CodeParse,
		fault.CodeIO, fault.CodeConflict, fault.CodeUnauthenticated,
		fault.CodeUnavailable, fault.CodeInternal,
	} {
		if !c.Valid() {
			t.Errorf("%q should be a valid code", c)
		}
	}
	for _, c := range []fault.Code{"", "nonsense", "INTERNAL", "not_found "} {
		if c.Valid() {
			t.Errorf("%q should not be a valid code", c)
		}
	}
}

// TestPublicWithholdsInternalDetail is the rule that keeps store paths and
// invariant text out of a response body.
func TestPublicWithholdsInternalDetail(t *testing.T) {
	internal := fault.Internal{Where: "store.Snapshot", Detail: "/var/lib/cq/machines/studio is unreadable"}
	got := fault.Public(internal)
	if got != "internal error" {
		t.Errorf("Public(internal) = %q, want a neutral message", got)
	}
	for _, leak := range []string{"/var/lib", "store.Snapshot", "unreadable"} {
		if strings.Contains(got, leak) {
			t.Errorf("Public leaked %q", leak)
		}
	}

	unauth := fault.Unauthenticated{Reason: "token digest mismatch for machine studio"}
	if got := fault.Public(unauth); got != "not authenticated" {
		t.Errorf("Public(unauthenticated) = %q, want no detail", got)
	}

	// Faults that describe the caller's own mistake are safe to relay.
	usage := fault.Usage{Reason: "sync takes no arguments"}
	if got := fault.Public(usage); got != "sync takes no arguments" {
		t.Errorf("Public(usage) = %q, want the reason", got)
	}
	if got := fault.Public(nil); got != "" {
		t.Errorf("Public(nil) = %q, want empty", got)
	}
}

func TestMessagesCarryTheirSubject(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want []string
	}{
		{fault.Parse{Where: "a.json", Line: 3, Reason: "bad"}, []string{"a.json:3", "bad"}},
		{fault.Parse{Where: "a.json", Reason: "bad"}, []string{"a.json: bad"}},
		{fault.NotFound{What: "task", Name: "x"}, []string{"task", `"x"`}},
		{fault.NotFound{What: "task"}, []string{"no such task"}},
		{fault.Ambiguous{Target: "t", Candidates: []string{"a", "b"}}, []string{"2 matches", "a", "b"}},
		{fault.IO{Op: "read", Subject: "f", Err: errors.New("boom")}, []string{"read f", "boom"}},
		{fault.IO{Op: "read", Subject: "f"}, []string{"read", "f"}},
		{fault.Conflict{Subject: "s", Reason: "changed"}, []string{"s: changed"}},
		// A conflict about no named thing is a sentence, not a sentence with a
		// colon in front of it.
		{fault.Conflict{Reason: "this action already worked"}, []string{"this action already worked"}},
		{fault.Unavailable{Peer: "srv", Err: errors.New("timeout")}, []string{"srv", "timeout"}},
		{fault.Unavailable{Peer: "srv"}, []string{"cannot reach srv"}},
		{fault.Internal{Where: "w", Detail: "d"}, []string{"w", "d", "bug in cq"}},
	} {
		got := tc.err.Error()
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("%T message %q should contain %q", tc.err, got, want)
			}
		}
	}
}

// TestNoFaultRendersEmpty guards the one failure mode worse than a bad message.
func TestNoFaultRendersEmpty(t *testing.T) {
	for _, err := range []error{
		fault.Parse{}, fault.Usage{}, fault.NotFound{}, fault.Ambiguous{},
		fault.IO{}, fault.Conflict{}, fault.Unauthenticated{},
		fault.Unavailable{}, fault.Internal{},
	} {
		if got := fmt.Sprintf("%v", err); strings.TrimSpace(got) == "" {
			t.Errorf("%T renders empty", err)
		}
	}
}

func TestIOAndUnavailableKeepTheirCause(t *testing.T) {
	cause := errors.New("disk on fire")
	io := fault.IO{Op: "read", Subject: "f", Err: cause}
	if !errors.Is(io, cause) || !errors.Is(io, fault.ErrIO) {
		t.Errorf("IO should expose both its cause and its sentinel")
	}
	un := fault.Unavailable{Peer: "p", Err: cause}
	if !errors.Is(un, cause) || !errors.Is(un, fault.ErrUnavailable) {
		t.Errorf("Unavailable should expose both its cause and its sentinel")
	}
}

func TestCheck(t *testing.T) {
	if err := fault.Check(true, "w", "never %s", "seen"); err != nil {
		t.Errorf("a satisfied check returns nil, got %v", err)
	}
	err := fault.Check(false, "pkg.Fn", "value %d is wrong", 7)
	if !errors.Is(err, fault.ErrInternal) {
		t.Fatalf("a failed check should be internal, got %v", err)
	}
	if !strings.Contains(err.Error(), "value 7 is wrong") || !strings.Contains(err.Error(), "pkg.Fn") {
		t.Errorf("message %q should carry the site and the detail", err)
	}
}

func TestField(t *testing.T) {
	err := fault.Field("Message", "subject", "is %d characters", 2000)
	if !errors.Is(err, fault.ErrParse) {
		t.Fatalf("Field should produce a parse fault, got %v", err)
	}
	if got := err.Error(); !strings.Contains(got, "Message.subject") || !strings.Contains(got, "2000") {
		t.Errorf("message %q should name the field and the detail", got)
	}
}

func TestJoinedFaultsStayClassifiable(t *testing.T) {
	joined := errors.Join(
		fault.Parse{Where: "a", Reason: "one"},
		fault.Unavailable{Peer: "b"},
	)
	if !errors.Is(joined, fault.ErrParse) || !errors.Is(joined, fault.ErrUnavailable) {
		t.Errorf("a joined error should expose every sentinel it holds")
	}
	var p fault.Parse
	if !errors.As(joined, &p) || p.Where != "a" {
		t.Errorf("errors.As should reach into a joined error")
	}
}
