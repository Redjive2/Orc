package query_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/mailman/internal/query"
)

var (
	testNow  = time.Date(2026, 7, 24, 18, 0, 0, 0, time.UTC)
	testSent = time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
)

func subject(opts ...func(*query.Subject)) query.Subject {
	s := query.Subject{
		PUID:    4,
		MID:     "0006575f93447a00-01020304",
		Kind:    "mail",
		From:    "boss",
		To:      []string{"alice", "bob"},
		CC:      []string{"carol"},
		Subject: "RE: work",
		Body:    "Ship it today.",
		Convo:   "0006575f93447a00-0a0b0c0d",
		Title:   "work",
		Index:   3,
		Unread:  true,
		Sent:    testSent,
	}
	for _, o := range opts {
		o(&s)
	}
	return s
}

func mustParse(t *testing.T, raw string) query.Query {
	t.Helper()
	q, err := query.Parse(raw)
	if err != nil {
		t.Fatalf("Parse(%q): %v", raw, err)
	}
	return q
}

func match(t *testing.T, raw string, s query.Subject) bool {
	t.Helper()
	got, err := mustParse(t, raw).Match(s, query.At(testNow))
	if err != nil {
		t.Fatalf("Match(%q): %v", raw, err)
	}
	return got
}

// TestReferenceQueriesParse is the compatibility test: every query the
// documentation shows must work exactly as written.
func TestReferenceQueriesParse(t *testing.T) {
	for _, raw := range []string{
		`from="boss"`,
		`from="boss" & subject="RE: work"`,
		`id="0"`,
	} {
		if _, err := query.Parse(raw); err != nil {
			t.Errorf("the documented query %s failed to parse: %v", raw, err)
		}
	}

	// And they must select what the documentation says they select.
	s := subject()
	if !match(t, `from="boss"`, s) {
		t.Error(`from="boss" should match a message from boss`)
	}
	if !match(t, `from="boss" & subject="RE: work"`, s) {
		t.Error(`the conjunction should match`)
	}
	if !match(t, `id="4"`, s) {
		t.Error(`id="4" should match puid 4`)
	}
	if match(t, `id="0"`, s) {
		t.Error(`id="0" should not match puid 4`)
	}
}

func TestPredicates(t *testing.T) {
	s := subject()
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		// text
		{`subject="RE: work"`, true},
		{`subject="re: work"`, false}, // equality is case-sensitive
		{`subject~"re: WORK"`, true},  // contains folds case
		{`subject~work`, true},
		{`subject!="other"`, true},
		{`body~"ship"`, true},
		{`body~"nonsense"`, false},
		{`title="work"`, true},

		// users
		{`from=boss`, true},
		{`from=BOSS`, true}, // names normalise
		{`from=alice`, false},
		{`from!=alice`, true},
		{`to=alice`, true},
		{`to=bob`, true},
		{`to=carol`, false}, // carol is cc, not to
		{`cc=carol`, true},
		{`to!=carol`, true},
		{`to!=alice`, false},
		{`any=carol`, true},
		{`any=boss`, true},
		{`any=dave`, false},
		{`from~os`, true},

		// numbers
		{`id=4`, true},
		{`id=5`, false},
		{`id!=5`, true},
		{`index=3`, true},
		{`index=1`, false},

		// identifiers
		{`mid="0006575f93447a00-01020304"`, true},
		{`mid~01020304`, true},
		{`mid~ffffffff`, false},
		{`convo~0a0b0c0d`, true},

		// flags
		{`unread=true`, true},
		{`unread=false`, false},
		{`unread=yes`, true},
		{`unread=1`, true},
		{`archived=false`, true},
		{`archived=true`, false},

		// kind
		{`kind=mail`, true},
		{`kind=cc`, false},
		{`kind!=cc`, true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			if got := match(t, tc.raw, s); got != tc.want {
				t.Errorf("%s matched %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestNotEqualOnAListMeansAbsence pins the semantics that would otherwise be
// quietly wrong: to!=bob asks whether bob is a recipient, not whether some
// recipient differs from bob.
func TestNotEqualOnAListMeansAbsence(t *testing.T) {
	s := subject(func(s *query.Subject) { s.To = []string{"alice", "bob"} })
	if match(t, `to!=bob`, s) {
		t.Error("to!=bob matched a message addressed to bob")
	}
	if !match(t, `to!=dave`, s) {
		t.Error("to!=dave should match a message not addressed to dave")
	}
}

func TestBooleanOperators(t *testing.T) {
	s := subject()
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`from=boss & to=alice`, true},
		{`from=boss & to=dave`, false},
		{`from=dave | to=alice`, true},
		{`from=dave | to=dave`, false},
		{`!from=dave`, true},
		{`!from=boss`, false},
		{`!!from=boss`, true},
		{`(from=boss)`, true},

		// | binds loosest: this is (from=dave) | (from=boss & to=alice).
		{`from=dave | from=boss & to=alice`, true},
		// Parentheses override it: (from=dave | from=boss) & to=dave.
		{`(from=dave | from=boss) & to=dave`, false},
		{`(from=dave | from=boss) & to=alice`, true},

		// Negation binds tighter than &.
		{`!from=dave & to=alice`, true},
		{`!(from=boss & to=alice)`, false},

		{`from=boss & from=boss & from=boss`, true},
		{`from=dave | from=dave | from=boss`, true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			if got := match(t, tc.raw, s); got != tc.want {
				t.Errorf("%s matched %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestTimePredicates(t *testing.T) {
	s := subject() // sent six hours before testNow
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`after=7d`, true},   // within the last week
		{`before=7d`, false}, // not older than a week
		{`after=1h`, false},  // not within the last hour
		{`before=1h`, true},  // older than an hour
		{`after="2026-07-24T00:00:00.000Z"`, true},
		{`before="2026-07-24T00:00:00.000Z"`, false},
		{`before="2026-07-25T00:00:00.000Z"`, true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			if got := match(t, tc.raw, s); got != tc.want {
				t.Errorf("%s matched %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestRelativeTimeUsesTheGivenInstant makes sure a relative predicate is
// resolved against the caller's clock and not against a hidden read of the
// real one, which would make every such test flaky.
func TestRelativeTimeUsesTheGivenInstant(t *testing.T) {
	q := mustParse(t, `after=1h`)
	s := subject()

	// Thirty minutes after it was sent, the message is within the last hour.
	near, err := q.Match(s, query.At(testSent.Add(30*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	// A day later, it is not.
	far, err := q.Match(s, query.At(testSent.Add(24*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if !near || far {
		t.Errorf("after=1h gave %v near and %v far; want true then false", near, far)
	}
}

func TestParseRejectsMalformedQueries(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		says string
	}{
		{"empty", "", "empty"},
		{"whitespace", "   ", "empty"},
		{"bare value", `"boss"`, "needs a field"},
		{"field alone", `from`, "expected ="},
		{"operator alone", `=`, "field name"},
		{"no value", `from=`, "expected a value"},
		{"trailing and", `from=boss &`, "expression"},
		{"trailing or", `from=boss |`, "expression"},
		{"leading and", `& from=boss`, "field name"},
		{"double and", `from=boss & & to=alice`, "field name"},
		{"unclosed group", `(from=boss`, "close"},
		{"unopened group", `from=boss)`, "never opened"},
		{"empty group", `()`, "empty parentheses"},
		{"unterminated quote", `from="boss`, "unterminated"},
		{"unterminated single quote", `from='boss`, "unterminated"},
		{"unknown field", `sender=boss`, "unknown field"},
		{"trailing junk", `from=boss to=alice`, "unexpected"},
		{"negation of nothing", `!`, "negate"},
		{"bad escape", `from="bo\ss"`, "backslash"},
		{"trailing backslash", `from="boss\`, "backslash"},
		{"control character", "from=\x07boss", "control"},
		{"not a number", `id=abc`, "whole number"},
		{"negative number", `id=-1`, "whole number"},
		{"not a flag", `unread=maybe`, "true"},
		{"not a kind", `kind=urgent`, "mail"},
		{"not a time", `before=soon`, "timestamp"},
		{"contains on a number", `id~4`, "not meaningful"},
		{"contains on a flag", `unread~true`, "not meaningful"},
		{"not-equal on a time", `before!=7d`, "not meaningful"},
		{"not-equal on a flag", `unread!=true`, "not meaningful"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := query.Parse(tc.raw)
			if !errors.Is(err, fault.ErrParse) {
				t.Fatalf("Parse(%q) = %v, want a parse fault", tc.raw, err)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("message for %q should mention %q, got:\n%s", tc.raw, tc.says, err)
			}
		})
	}
}

// TestErrorsCarryAUsableColumn: the caret is the whole point of the query
// fault type, so it must point inside the query.
func TestErrorsCarryAUsableColumn(t *testing.T) {
	for _, raw := range []string{
		`from=boss & `, `sender=boss`, `from=boss)`, `id~4`, `from="boss`,
	} {
		_, err := query.Parse(raw)
		var qf fault.Query
		if !errors.As(err, &qf) {
			t.Errorf("Parse(%q) = %v, want a fault.Query", raw, err)
			continue
		}
		if qf.Col < 1 || qf.Col > len([]rune(raw))+1 {
			t.Errorf("Parse(%q) reported column %d, outside 1..%d", raw, qf.Col, len([]rune(raw))+1)
		}
		if qf.Query != raw {
			t.Errorf("Parse(%q) reported query %q", raw, qf.Query)
		}
	}
}

func TestUnknownFieldSuggests(t *testing.T) {
	_, err := query.Parse(`sender=boss`)
	if err == nil {
		t.Fatal("expected a failure")
	}
	// The full vocabulary is always listed, so the caller never has to guess.
	for _, want := range query.FieldNames() {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message should list the field %q:\n%s", want, err)
		}
	}

	// A near miss is offered explicitly.
	_, err = query.Parse(`subjekt=x`)
	if err == nil || !strings.Contains(err.Error(), `"subject"`) {
		t.Errorf("subjekt should suggest subject, got %v", err)
	}
	_, err = query.Parse(`fro=x`)
	if err == nil || !strings.Contains(err.Error(), `"from"`) {
		t.Errorf("fro should suggest from, got %v", err)
	}
}

func TestDepthIsBounded(t *testing.T) {
	deep := strings.Repeat("(", query.MaxDepth+5) + "from=boss" + strings.Repeat(")", query.MaxDepth+5)
	_, err := query.Parse(deep)
	if !errors.Is(err, fault.ErrParse) {
		t.Fatalf("a deeply nested query = %v, want a parse fault", err)
	}
	if !strings.Contains(err.Error(), "nested") {
		t.Errorf("message %q should mention nesting", err)
	}

	// Just inside the limit must still work.
	ok := strings.Repeat("(", query.MaxDepth) + "from=boss" + strings.Repeat(")", query.MaxDepth)
	if _, err := query.Parse(ok); err != nil {
		t.Errorf("a query at exactly the depth limit failed: %v", err)
	}
}

func TestLengthIsBounded(t *testing.T) {
	long := "from=" + strings.Repeat("a", query.MaxLength)
	if _, err := query.Parse(long); !errors.Is(err, fault.ErrParse) {
		t.Errorf("an overlong query = %v, want a parse fault", err)
	}
}

func TestQuotingStyles(t *testing.T) {
	s := subject(func(s *query.Subject) { s.Subject = `a "quoted" word` })
	for _, raw := range []string{
		`subject='a "quoted" word'`,
		`subject="a \"quoted\" word"`,
	} {
		if !match(t, raw, s) {
			t.Errorf("%s should match", raw)
		}
	}

	// A bare value works where it is unambiguous, which is the common case.
	plain := subject()
	if !match(t, `from=boss`, plain) {
		t.Error("a bare value should work")
	}
	// And stops at an operator, so this is two predicates, not one long value.
	if !match(t, `from=boss&to=alice`, plain) {
		t.Error("bare values should stop at an operator")
	}
}

// TestStringRoundTrips: the canonical rendering is what `prune` shows before it
// deletes anything, so it must parse back to an expression that matches the
// same messages.
func TestStringRoundTrips(t *testing.T) {
	s := subject()
	for _, raw := range []string{
		`from=boss`,
		`from="boss" & subject="RE: work"`,
		`from=dave | from=boss & to=alice`,
		`(from=dave | from=boss) & to=alice`,
		`!from=dave`,
		`!(from=boss & to=alice)`,
		`unread=true & after=7d`,
	} {
		t.Run(raw, func(t *testing.T) {
			first := mustParse(t, raw)
			rendered := first.String()

			second, err := query.Parse(rendered)
			if err != nil {
				t.Fatalf("the rendering %q does not parse: %v", rendered, err)
			}
			if got := second.String(); got != rendered {
				t.Errorf("rendering is not stable: %q -> %q", rendered, got)
			}

			a, err := first.Match(s, query.At(testNow))
			if err != nil {
				t.Fatal(err)
			}
			b, err := second.Match(s, query.At(testNow))
			if err != nil {
				t.Fatal(err)
			}
			if a != b {
				t.Errorf("%q matched %v but its rendering %q matched %v", raw, a, rendered, b)
			}
		})
	}
}

func TestZeroQueryMatchesNothing(t *testing.T) {
	var q query.Query
	if !q.Zero() {
		t.Error("the zero Query should report itself as zero")
	}
	if got := q.String(); got != "<empty>" {
		t.Errorf("String() = %q", got)
	}
	// It must fail rather than match: a query that silently became empty must
	// never archive or prune a whole mailbox.
	if _, err := q.Match(subject(), query.At(testNow)); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("the zero Query matched with %v, want an internal fault", err)
	}
}

func TestMatchRejectsAnIncompleteSubject(t *testing.T) {
	q := mustParse(t, `from=boss`)
	for _, tc := range []struct {
		name string
		s    query.Subject
	}{
		{"no mid", subject(func(s *query.Subject) { s.MID = "" })},
		{"no sender", subject(func(s *query.Subject) { s.From = "" })},
		{"no recipients", subject(func(s *query.Subject) { s.To = nil })},
		{"no kind", subject(func(s *query.Subject) { s.Kind = "" })},
		{"no send time", subject(func(s *query.Subject) { s.Sent = time.Time{} })},
		{"negative puid", subject(func(s *query.Subject) { s.PUID = -1 })},
		{"index without convo", subject(func(s *query.Subject) { s.Convo = "" })},
		{"convo without index", subject(func(s *query.Subject) { s.Index = 0 })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := q.Match(tc.s, query.At(testNow)); !errors.Is(err, fault.ErrInternal) {
				t.Errorf("Match on %s = %v, want an internal fault", tc.name, err)
			}
		})
	}
}

func TestMatchRequiresAnInstant(t *testing.T) {
	q := mustParse(t, `from=boss`)
	if _, err := q.Match(subject(), query.Now{}); !errors.Is(err, fault.ErrInternal) {
		t.Errorf("Match without an instant = %v, want an internal fault", err)
	}
}

// TestStandaloneMessageSubject checks the other legal shape: no conversation
// and no index.
func TestStandaloneMessageSubject(t *testing.T) {
	s := subject(func(s *query.Subject) { s.Convo, s.Index, s.Title = "", 0, "" })
	if err := s.Validate(); err != nil {
		t.Fatalf("a standalone subject should be valid: %v", err)
	}
	if match(t, `convo~0a0b`, s) {
		t.Error("a standalone message should not match a conversation query")
	}
	if !match(t, `from=boss`, s) {
		t.Error("a standalone message should still match on its sender")
	}
}

func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		`from="boss"`, `from="boss" & subject="RE: work"`, `id="0"`,
		`!(a|b)`, `((((`, `from=`, `~`, `unread=true`, `before=7d`, "",
	} {
		f.Add(seed)
	}

	s := query.Subject{
		PUID: 1, MID: "0006575f93447a00-01020304", Kind: "mail",
		From: "boss", To: []string{"alice"}, Subject: "s", Sent: testSent,
	}

	f.Fuzz(func(t *testing.T, raw string) {
		q, err := query.Parse(raw)
		if err != nil {
			// Every refusal must classify as a parse fault and carry an in-range
			// column, or the caret the CLI draws would point at nothing.
			if !errors.Is(err, fault.ErrParse) {
				t.Fatalf("Parse(%q) failed with an unclassified error: %v", raw, err)
			}
			var qf fault.Query
			if errors.As(err, &qf) && qf.Col > len([]rune(raw))+1 {
				t.Fatalf("Parse(%q) reported column %d, past the end", raw, qf.Col)
			}
			return
		}

		// Anything that parses must evaluate without error and render to
		// something that parses back to the same rendering.
		got, err := q.Match(s, query.At(testNow))
		if err != nil {
			t.Fatalf("Match on a parsed query %q: %v", raw, err)
		}

		rendered := q.String()
		again, err := query.Parse(rendered)
		if err != nil {
			t.Fatalf("Parse(%q) rendered %q, which does not parse: %v", raw, rendered, err)
		}
		if second := again.String(); second != rendered {
			t.Fatalf("rendering is not stable: %q -> %q -> %q", raw, rendered, second)
		}
		twice, err := again.Match(s, query.At(testNow))
		if err != nil {
			t.Fatalf("Match on a re-parsed query: %v", err)
		}
		if twice != got {
			t.Fatalf("%q matched %v but its rendering %q matched %v", raw, got, rendered, twice)
		}
	})
}

// TestClockLayoutIsQueryable guards a coupling that is easy to break: the
// timestamp format clock writes must be one that a query can name.
func TestClockLayoutIsQueryable(t *testing.T) {
	stamped := clock.Format(testSent)
	if _, err := query.Parse(`before="` + stamped + `"`); err != nil {
		t.Errorf("a timestamp written by clock.Format is not queryable: %v", err)
	}
}
