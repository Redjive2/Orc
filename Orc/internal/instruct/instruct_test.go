package instruct_test

import (
	"strings"
	"testing"

	"orc/common/fault"
	"orc/orc/internal/instruct"
	"orc/orc/internal/prose"
)

// The two composition rules are opposites, and everything built on this assumes
// which is which. They are what this file is mostly about.

// TestPromptLayersAreAdditive. The fleet prompt is the floor, not a default: a role
// cannot shadow it and an identity cannot shadow either. An operator who wants one
// agent to ignore the fleet prompt does not need a feature for that; they need to
// edit the fleet prompt.
func TestComposeIsAdditive(t *testing.T) {
	got, err := instruct.Compose(instruct.Layers{
		System:       "ask before you guess",
		Role:         "you write the parser",
		Identity:     "you are covering for atlas this week",
		RoleName:     "engineer",
		IdentityName: "ember",
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"ask before you guess",
		"you write the parser",
		"you are covering for atlas this week",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("a layer was lost:\n%s", got)
		}
	}

	// In order, fleet first. The order is the meaning: a later layer adds to what
	// came before it, and reversing them would read as the specific being qualified
	// by the general.
	fleet := strings.Index(got, "ask before you guess")
	role := strings.Index(got, "you write the parser")
	identity := strings.Index(got, "covering for atlas")
	if !(fleet < role && role < identity) {
		t.Errorf("the layers are out of order:\n%s", got)
	}
}

// Each layer says where it came from, because an agent following an instruction
// should be able to say which of three documents it is in — and so should the
// operator asking why it did.
func TestComposeNamesItsSources(t *testing.T) {
	got, err := instruct.Compose(instruct.Layers{
		System: "a", Role: "b", Identity: "c",
		RoleName: "engineer", IdentityName: "ember",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# the fleet", "# the engineer role", "# ember"} {
		if !strings.Contains(got, want) {
			t.Errorf("the composed prompt does not say %q:\n%s", want, got)
		}
	}
}

// A fleet that has set nothing composes to nothing, rather than to a document of
// empty headings.
func TestComposeEmpty(t *testing.T) {
	got, err := instruct.Compose(instruct.Layers{RoleName: "engineer", IdentityName: "ember"})
	if err != nil {
		t.Fatal(err)
	}
	// The house writing rule, and nothing else. It is not a layer anybody sets —
	// see Compose — so a fleet that has written no instructions still has one thing
	// to say to its agents.
	if got != "# how to write\n\n"+instruct.House {
		t.Errorf("nothing set composed to %q", got)
	}

	// And one layer alone is that layer, with its heading and nothing else.
	got, err = instruct.Compose(instruct.Layers{System: "only this"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "only this") || strings.Contains(got, "role") {
		t.Errorf("one layer composed to:\n%s", got)
	}
}

// TestWakeMessagesOverride — the opposite rule, and for a plain reason: a system
// prompt is a document and documents concatenate, while a wake message is one thing
// you say. Three stapled together is not a message.
func TestWakeOverrides(t *testing.T) {
	for _, tc := range []struct {
		what                  string
		identity, role, fleet string
		want                  string
		source                instruct.Kind
	}{
		{"the identity's wins", "get back to the parser", "keep working", "carry on", "get back to the parser", instruct.Identity},
		{"then the role's", "", "keep working", "carry on", "keep working", instruct.Role},
		{"then the fleet's", "", "", "carry on", "carry on", instruct.Wake},
		{"and continue at the bottom", "", "", "", instruct.DefaultWake, ""},
		{"whitespace is not a message", "   \n ", "", "", instruct.DefaultWake, ""},
	} {
		if got := instruct.WakeFor(tc.identity, tc.role, tc.fleet); got != tc.want {
			t.Errorf("%s: sent %q, want %q", tc.what, got, tc.want)
		}
		if got := instruct.Source(tc.identity, tc.role, tc.fleet); got != tc.source {
			t.Errorf("%s: came from %q, want %q", tc.what, got, tc.source)
		}
	}
}

// TestOnlyOneWakeMessageIsSent. The rule that makes it an override rather than a
// concatenation: the ones that lost are not appended anywhere.
func TestWakeSendsOnlyTheWinner(t *testing.T) {
	got := instruct.WakeFor("mine", "the role's", "the fleet's")
	if strings.Contains(got, "the role's") || strings.Contains(got, "the fleet's") {
		t.Errorf("a losing wake message was sent too: %q", got)
	}
}

// A fleet that configures nothing behaves exactly as it did before this existed.
func TestDefaultWakeIsWhatPokeAlwaysSent(t *testing.T) {
	if instruct.DefaultWake != "continue" {
		t.Errorf("the built-in bottom is %q; `orc poke`'s default has always been \"continue\"",
			instruct.DefaultWake)
	}
}

// TestALayerOverItsLimitIsRefusedNotCut. Silently cutting an instruction in half is
// how an agent ends up following the first paragraph of a rule and none of the rest
// — which is worse than following none of it, because it looks like obedience.
func TestBoundsRefuseRatherThanTruncate(t *testing.T) {
	long := strings.Repeat("x", instruct.MaxLayer+1)

	err := instruct.Check(instruct.System, long)
	if err == nil {
		t.Fatal("an oversized layer was accepted")
	}
	// The refusal names the kind and both sizes, so the fix is arithmetic rather
	// than guesswork.
	for _, want := range []string{"system", "KiB"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}

	// A wake message is a sentence, and is bounded much more tightly.
	if err := instruct.Check(instruct.Wake, strings.Repeat("x", instruct.MaxWake+1)); err == nil {
		t.Error("an oversized wake message was accepted")
	}
	if err := instruct.Check(instruct.Wake, strings.Repeat("x", instruct.MaxLayer)); err == nil {
		t.Error("a wake message the size of a whole layer was accepted")
	}
}

// Three layers each inside their own bound can still exceed what a command line
// should carry, so the total is bounded as well as each part.
func TestComposedTotalIsBounded(t *testing.T) {
	full := strings.Repeat("x", instruct.MaxLayer)

	_, err := instruct.Compose(instruct.Layers{System: full, Role: full, Identity: full})
	if err == nil {
		t.Fatal("three full layers composed without complaint")
	}
	if !strings.Contains(err.Error(), "composed") {
		t.Errorf("the refusal should say it is the total: %v", err)
	}
}

// A prompt goes onto a command line and into a pty, where a control character does
// something nobody asked for and an escape sequence can repaint a screen.
func TestControlCharactersAreRefused(t *testing.T) {
	if err := instruct.Check(instruct.System, "before\x1b[31m after"); err == nil {
		t.Error("an escape sequence was accepted into a system prompt")
	}
	if err := instruct.Check(instruct.System, "a\x00b"); err == nil {
		t.Error("a NUL was accepted")
	}
	// Newlines and tabs are how prose is written.
	if err := instruct.Check(instruct.System, "a rule\n\tand its exception\n"); err != nil {
		t.Errorf("ordinary prose was refused: %v", err)
	}
}

func TestCheckRejectsBadInput(t *testing.T) {
	if err := instruct.Check("nonsense", "x"); err == nil {
		t.Error("an unknown kind was accepted")
	} else if !errorsIs(err, fault.ErrInternal) {
		t.Errorf("an unknown kind is a defect, not a usage error: %v", err)
	}
	if err := instruct.Check(instruct.System, string([]byte{0xff, 0xfe})); err == nil {
		t.Error("invalid UTF-8 was accepted")
	}
}

// The kinds are two mechanisms, and the code that composes them asks which.
func TestKindsAreTotal(t *testing.T) {
	layers := 0
	for _, k := range instruct.Kinds() {
		if !k.Valid() {
			t.Errorf("%q is listed and not valid", k)
		}
		if k.Layer() {
			layers++
		}
	}
	if layers != 3 {
		t.Errorf("%d kinds compose into the prompt, want the three layers", layers)
	}
	if instruct.Wake.Layer() {
		t.Error("a wake message is not a prompt layer; it is sent to a session that already exists")
	}
}

func errorsIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// The writing rule reaches every agent, whatever the fleet has or has not set.
//
// It is not a layer. A fleet where half the agents write one way produces documents
// that read as though several people wrote them, which is what the rule is for — so
// nobody can turn it off and nobody has to remember to turn it on.
func TestTheWritingRuleIsAlwaysComposedIn(t *testing.T) {
	for _, layers := range []instruct.Layers{
		{},
		{System: "the fleet's own words"},
		{System: "a", Role: "b", Identity: "c", RoleName: "engineer", IdentityName: "ember"},
	} {
		got, err := instruct.Compose(layers)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"ASD-STE100", "80%", "orc prose", "load-bearing"} {
			if !strings.Contains(got, want) {
				t.Errorf("the composition does not mention %q:\n%s", want, got)
			}
		}
		// First, so it is read before whatever else a fleet says.
		if !strings.HasPrefix(got, "# how to write") {
			t.Errorf("the writing rule is not at the top:\n%s", got)
		}
	}
}

// And the rule itself obeys the rule. A prompt that broke its own instruction would
// be the first thing an agent read and the first thing it learned to discount.
func TestTheWritingRuleObeysItself(t *testing.T) {
	got := prose.Check(instruct.House)
	if got.Banned() {
		t.Errorf("the writing rule uses a word it bans: %+v", got.Findings)
	}
	if !got.OK() {
		t.Errorf("the writing rule scores %.0f%%, under its own threshold: %+v",
			got.Score()*100, got.Findings)
	}
}
