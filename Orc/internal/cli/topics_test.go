package cli

import (
	"strings"
	"testing"

	"orc/orc/internal/style"
)

// The forms in the topic table are repeated from usage() rather than shared with it,
// because usage is a hand-set page in five groups. These tests are what keep the
// repetition honest.

// TestEveryVerbHasAPage. A command with no page is one `orc help <it>` refuses to
// explain, which is worse than no per-command help at all: the reader has been told
// the feature exists.
func TestEveryVerbHasATopic(t *testing.T) {
	for _, verb := range verbs() {
		if verb == "help" {
			continue // help explains itself by being run
		}
		if _, ok := topics[verb]; !ok {
			t.Errorf("`orc help %s` has nothing to say", verb)
		}
	}
}

func TestEveryTopicIsAVerb(t *testing.T) {
	known := map[string]bool{}
	for _, verb := range verbs() {
		known[verb] = true
	}
	for name := range topics {
		if !known[name] {
			t.Errorf("there is a page for %q, which is not a command", name)
		}
	}
}

// Every form on a page names a command the screen lists.
//
// The comparison is on the command words — everything before the first placeholder
// or flag — not the whole form. The screen abbreviates for width (`[--model m]`
// where a page says `[--model <m>]`, and flags that live in the description rather
// than the form), and a test that demanded the two match character for character
// would be checking spelling while the drift worth catching is a page for a verb the
// screen never mentions, or a verb renamed in one place and not the other.
func TestTopicFormsNameRealCommands(t *testing.T) {
	screen := collapse(usage(style.Plain()))

	for name, got := range topics {
		if len(got.forms) == 0 {
			t.Errorf("%s's page shows no form at all", name)
			continue
		}
		for _, form := range got.forms {
			if words := commandWords(form); !strings.Contains(screen, words) {
				t.Errorf("`%s` is in %s's page, but the screen never lists `%s`", form, name, words)
			}
		}
	}
}

// commandWords keeps the leading words of a form: `orc new identity <name>` becomes
// `orc new identity`.
func commandWords(form string) string {
	var out []string
	for _, field := range strings.Fields(form) {
		if strings.HasPrefix(field, "<") || strings.HasPrefix(field, "[") {
			break
		}
		out = append(out, field)
	}
	return strings.Join(out, " ")
}

// TestAPageIsOnlyThatCommand. The whole point of `orc help <command>`: the screen it
// prints must not be the screen `orc help` prints.
func TestPageIsNotTheWholeScreen(t *testing.T) {
	for name := range topics {
		page, ok := commandHelp(style.Plain(), name)
		if !ok {
			t.Fatalf("%s has no page", name)
		}
		if strings.Contains(page, "the model") || strings.Contains(page, "exit  0 ok") {
			t.Errorf("%s's page carries the whole help screen's extras:\n%s", name, page)
		}
		// It is not the menu. A line-count ratio was the first version of this
		// check and it was the wrong one: a thorough page for a tool with five
		// commands is legitimately half the size of that tool's whole screen.
		// What must not be there is *other commands*.
		for _, other := range othersOf(name) {
			if strings.Contains(page, other) {
				t.Errorf("%s's page lists %q, which is another command's line", name, other)
			}
		}
	}
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// othersOf returns the summary lines of every command except the named one, so a
// page can be checked for not being the list.
// Orc's help screen is written out by hand rather than from a slice, so the other
// commands' summaries come from the topic table — which the tests above have already
// established says the same thing the screen does.
func othersOf(name string) []string {
	var out []string
	for other, got := range topics {
		if other == name || strings.TrimSpace(got.does) == "" {
			continue
		}
		out = append(out, got.does)
	}
	return out
}
