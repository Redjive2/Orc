package prose_test

import (
	"strings"
	"testing"

	"orc/orc/internal/prose"
)

// The two halves of the house rule are enforced differently, and the tests are
// arranged the same way: a ban is exact and a style rule is a proportion.

func TestABannedWordFailsWhateverElseIsTrue(t *testing.T) {
	// Every sentence here obeys every style rule. One word still fails the text,
	// because a ban is not a proportion.
	for _, text := range []string{
		"This is honestly a short line.",
		"We add one caveat here.",
		"The check is a genuine one.",
		"That flag is load-bearing here.",
		"Two caveats apply to the plan.",
	} {
		got := prose.Check(text)
		if !got.Banned() {
			t.Errorf("%q passed the ban", text)
		}
		if got.OK() {
			t.Errorf("%q was accepted with a banned word in it", text)
		}
		if got.Score() < 1 {
			t.Errorf("%q was also marked down for style, which it does not break", text)
		}
	}
}

// A ban that fired inside longer words would make whole families of ordinary words
// unusable, and somebody would turn the tool off rather than rename them.
func TestABanMatchesWholeWordsOnly(t *testing.T) {
	for _, text := range []string{
		"The value is dishonest data.",
		"We ingenuinely mangled that word.",
		"It carries a load bearing down on it.",
	} {
		if got := prose.Check(text); got.Banned() && !strings.Contains(text, "load bearing") {
			t.Errorf("%q was refused for a word inside another word", text)
		}
	}
	// And the two-word spelling is caught, because it is the same phrase.
	if !prose.Check("That flag is load bearing.").Banned() {
		t.Error("the spaced spelling of the phrase was allowed")
	}
}

func TestTheStyleRulesCatchWhatSTEIsAbout(t *testing.T) {
	for _, tc := range []struct {
		text string
		rule prose.Rule
	}{
		{"The store is read on every pass.", prose.RulePassive},
		{"The record was written by the hook that runs after each tool call ends.", prose.RulePassive},
		{strings.Repeat("word ", 30) + ".", prose.RuleLength},
		{"It works because the store is small, although the fleet is large, whereas the queue is not.", prose.RuleClauses},
	} {
		got := prose.Check(tc.text)
		found := false
		for _, f := range got.Findings {
			if f.Rule == tc.rule {
				found = true
			}
		}
		if !found {
			t.Errorf("%q did not break %s: %+v", shorten(tc.text), tc.rule, got.Findings)
		}
	}
}

// Active, short, single-clause prose passes. A checker that failed good writing
// would be one people write around.
func TestPlainWritingPasses(t *testing.T) {
	text := `Orc reads the store on every pass.
It starts a session for each agent on the worklist.
A session that has stopped comes back, and the agent carries on.
The cycle says what it did.`
	got := prose.Check(text)
	if !got.OK() {
		t.Errorf("plain writing scored %.2f: %+v", got.Score(), got.Findings)
	}
	if got.Sentences != 4 {
		t.Errorf("it found %d sentences, want 4", got.Sentences)
	}
}

// The threshold is a proportion for a reason: prose has sentences that need the
// length, and a rule that failed every one of them would be a rule nobody keeps.
//
// At nine in ten there is room for one such sentence in a paragraph of ten, and no
// room for a second.
func TestTheScoreIsAProportionRatherThanAVerdictOnEachSentence(t *testing.T) {
	good := strings.Repeat("Orc reads the store. ", 9)
	bad := strings.Repeat("word ", 30) + "."

	if got := prose.Check(good + bad); !got.OK() {
		t.Errorf("nine good sentences and one long one scored %.2f, which should pass", got.Score())
	}
	if got := prose.Check(good + bad + " " + bad); got.OK() {
		t.Errorf("nine good sentences and two long ones scored %.2f, which should fail", got.Score())
	}
}

// Structure is not prose. Scoring headings, tables, and code would put most of a
// document's failures where the rule was never meant to apply.
func TestMarkdownStructureIsNotMeasured(t *testing.T) {
	text := "# A heading that is quite long and would be marked down as a sentence otherwise\n" +
		"\n" +
		"```\nthe code is read by the machine and this line is very long indeed you see\n```\n" +
		"\n" +
		"| a | table | row | that | is | read | by | nobody |\n" +
		"\n" +
		"Orc reads the store on every pass.\n"
	got := prose.Check(text)
	if got.Sentences != 1 {
		t.Errorf("it measured %d sentences, want 1: %+v", got.Sentences, got.Findings)
	}
	if !got.OK() {
		t.Errorf("structure was scored as prose: %+v", got.Findings)
	}
}

// A list item is prose with a bullet in front of it.
func TestListItemsAreMeasured(t *testing.T) {
	got := prose.Check("- The store is read on every pass.\n")
	if got.Sentences != 1 {
		t.Fatalf("it measured %d sentences, want 1", got.Sentences)
	}
	if got.OK() {
		t.Error("a passive list item passed")
	}
}

// An empty text is not wrong. A score of zero on a file with nothing in it would
// fail every new document at the moment it is created.
func TestNothingScoresClean(t *testing.T) {
	if got := prose.Check(""); !got.OK() || got.Score() != 1 {
		t.Errorf("an empty text scored %.2f", got.Score())
	}
}

func shorten(s string) string {
	if len(s) <= 50 {
		return s
	}
	return s[:49] + "…"
}
