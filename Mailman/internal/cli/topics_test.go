package cli

import (
	"strings"
	"testing"

	"orc/mailman/internal/style"
)

// The forms in the topic table are repeated from usage() rather than shared with it,
// because usage is laid out by hand in groups. These two tests are what keep the
// repetition honest: neither list may grow, shrink, or be reworded without the other.

// TestEveryVerbHasATopic. A command with no page is one `mailman help <it>` refuses
// to explain, which is worse than no per-command help at all — the reader has been
// told the feature exists.
func TestEveryVerbHasATopic(t *testing.T) {
	for _, verb := range verbs() {
		if verb == "help" {
			continue // help explains itself by being run
		}
		if _, ok := topics[verb]; !ok {
			t.Errorf("`mailman help %s` has nothing to say", verb)
		}
	}
}

// And nothing has a page that is not a command.
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

// TestTopicFormsMatchTheHelpScreen. The same command described two ways is how a
// help text starts lying about its own flags.
func TestTopicFormsAppearInUsage(t *testing.T) {
	screen := usage(style.Plain())

	for name, got := range topics {
		// The first form is the canonical one, and is what the screen lists. A
		// page may show more of them — `admin` has three subcommands and one
		// line on the screen — but the one the screen shows has to be real.
		if len(got.forms) == 0 {
			t.Errorf("%s's page shows no form at all", name)
			continue
		}
		if !strings.Contains(collapse(screen), collapse(got.forms[0])) {
			t.Errorf("`%s` is in %s's page but not on the help screen", got.forms[0], name)
		}
		if !strings.Contains(collapse(screen), collapse(got.does)) {
			t.Errorf("%s's summary is not the one the help screen gives", name)
		}
	}
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// TestAPageIsOnlyThatCommand — the whole point of `mailman help <command>`.
func TestPageIsNotTheWholeScreen(t *testing.T) {
	for name := range topics {
		page, ok := commandHelp(style.Plain(), name)
		if !ok {
			t.Fatalf("%s has no page", name)
		}
		for _, extra := range []string{"queries select mail by field", "exit  0 ok", "ORC_THEME", "identity comes from orc"} {
			if strings.Contains(page, extra) {
				t.Errorf("%s's page carries the whole screen's %q", name, extra)
			}
		}
		// And it is not the menu: no other command's summary is on it.
		for other, got := range topics {
			if other == name || strings.TrimSpace(got.does) == "" {
				continue
			}
			if strings.Contains(page, got.does) {
				t.Errorf("%s's page lists %q, which is %s's line", name, got.does, other)
			}
		}
	}
}
