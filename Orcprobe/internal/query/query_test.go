package query

import (
	"errors"
	"strings"
	"testing"
	"time"

	"orc/orcprobe/internal/fault"
	"orc/orcprobe/internal/read"
)

func now() time.Time { return time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC) }

func messages() []read.Message {
	return []read.Message{
		{
			MID: "aa-1", Kind: "mail", From: "boss", To: []string{"alice"}, CC: []string{"carol"},
			Subject: "the plan", Sent: time.Date(2026, time.July, 1, 9, 0, 0, 0, time.UTC),
			Body:   []byte("read the plan"),
			PUID:   map[string]int{"alice": 0, "carol": 3},
			Unread: map[string]bool{"alice": true, "carol": false},
		},
		{
			MID: "bb-2", Kind: "mail", From: "alice", To: []string{"boss"},
			Subject: "RE: the plan", Sent: time.Date(2026, time.July, 20, 9, 0, 0, 0, time.UTC),
			Body:   []byte("done"),
			PUID:   map[string]int{"boss": 1},
			Unread: map[string]bool{"boss": false},
		},
		{
			MID: "cc-3", Kind: "cc", From: "alice", To: []string{"dave"},
			Subject: "work order", Sent: time.Date(2026, time.July, 23, 9, 0, 0, 0, time.UTC),
			Body:   []byte("nothing"),
			PUID:   map[string]int{"dave": 0},
			Unread: map[string]bool{"dave": true},
		},
	}
}

func match(t *testing.T, text string) []string {
	t.Helper()
	q, err := Parse(text)
	if err != nil {
		t.Fatalf("Parse(%q): %v", text, err)
	}
	var out []string
	for _, m := range q.Select(messages(), now()) {
		out = append(out, m.MID)
	}
	return out
}

func TestSelects(t *testing.T) {
	cases := []struct {
		query string
		want  []string
	}{
		{``, []string{"aa-1", "bb-2", "cc-3"}},
		{`from="boss"`, []string{"aa-1"}},
		{`from="BOSS"`, []string{"aa-1"}},
		{`subject~"plan"`, []string{"aa-1", "bb-2"}},
		{`subject~"PLAN"`, []string{"aa-1", "bb-2"}},
		{`kind="cc"`, []string{"cc-3"}},
		{`body~"done"`, []string{"bb-2"}},
		{`from="alice" & subject~"work"`, []string{"cc-3"}},
		{`from="boss" | kind="cc"`, []string{"aa-1", "cc-3"}},
		{`!(from="alice")`, []string{"aa-1"}},
		{`(from="boss" | from="alice") & subject~"plan"`, []string{"aa-1", "bb-2"}},

		// A recipient field means "is a recipient", and its negation means "is
		// not one" — not "some recipient is somebody else".
		{`to="alice"`, []string{"aa-1"}},
		{`cc="carol"`, []string{"aa-1"}},
		{`to!="alice"`, []string{"bb-2", "cc-3"}},
		{`any="carol"`, []string{"aa-1"}},
		{`any="alice"`, []string{"aa-1", "bb-2", "cc-3"}},

		// Cross-mailbox fields: unread by anybody, any recipient's puid.
		{`unread=true`, []string{"aa-1", "cc-3"}},
		{`unread=false`, []string{"bb-2"}},
		{`id="3"`, []string{"aa-1"}},
		{`id="0"`, []string{"aa-1", "cc-3"}},

		{`after="2026-07-15"`, []string{"bb-2", "cc-3"}},
		{`before="2026-07-15"`, []string{"aa-1"}},
	}

	for _, c := range cases {
		t.Run(c.query, func(t *testing.T) {
			got := match(t, c.query)
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("matched %v, want %v", got, c.want)
			}
		})
	}
}

// TestUnknownFieldIsAnError is Mailman's rule, kept: a typo must not read as
// "no results".
func TestUnknownFieldIsAnError(t *testing.T) {
	_, err := Parse(`sender="boss"`)
	if err == nil {
		t.Fatal("an unknown field was accepted")
	}
	if !errors.Is(err, fault.ErrParse) {
		t.Fatalf("error is %T, want a parse fault", err)
	}
	// The candidates are listed, so the fix is a copy rather than a guess.
	if !strings.Contains(err.Error(), "from") {
		t.Fatalf("the error does not list the fields:\n%v", err)
	}
}

func TestMalformedQueriesPointAtTheColumn(t *testing.T) {
	for _, text := range []string{
		`from=`,
		`from`,
		`from="boss" &`,
		`(from="boss"`,
		`from="boss`,
		`& from="boss"`,
		`from="boss") | to="alice"`,
	} {
		t.Run(text, func(t *testing.T) {
			_, err := Parse(text)
			if err == nil {
				t.Fatalf("Parse(%q) accepted a malformed query", text)
			}
			var q fault.Query
			if !errors.As(err, &q) {
				t.Fatalf("error is %T, want a fault.Query that carries a column", err)
			}
			if !strings.Contains(err.Error(), "^") {
				t.Fatalf("the error does not underline the mistake:\n%v", err)
			}
		})
	}
}

func TestQuotingAndBareValues(t *testing.T) {
	for _, text := range []string{`from="boss"`, `from='boss'`, `from=boss`} {
		if got := match(t, text); len(got) != 1 || got[0] != "aa-1" {
			t.Fatalf("Parse(%q) matched %v", text, got)
		}
	}
	// A value with a space in it needs its quotes, and keeps them.
	if got := match(t, `subject="the plan"`); len(got) != 1 || got[0] != "aa-1" {
		t.Fatalf("a quoted value with a space matched %v", got)
	}
}

func TestPrecedence(t *testing.T) {
	// | binds loosest: this is (from=boss) | (from=alice & kind=cc), not
	// (from=boss | from=alice) & kind=cc.
	got := match(t, `from="boss" | from="alice" & kind="cc"`)
	if strings.Join(got, ",") != "aa-1,cc-3" {
		t.Fatalf("matched %v; | must bind looser than &", got)
	}
}

func TestSpanInBeforeAndAfter(t *testing.T) {
	// "sent in the last 48 hours", measured from the injected now.
	got := match(t, `after="48h"`)
	if strings.Join(got, ",") != "cc-3" {
		t.Fatalf("matched %v, want the message from the day before now", got)
	}
}
