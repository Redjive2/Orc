package cli

import (
	"strings"
	"testing"

	"orc/macmuffin/internal/style"
)

// TestEveryVerbHasAPage. A command with no page is one `muff help <it>` refuses to
// explain, which is worse than no per-command help at all: the reader has been told
// the feature exists.
func TestEveryVerbHasATopic(t *testing.T) {
	for _, verb := range verbs() {
		if verb == "help" {
			continue // help explains itself by being run
		}
		if _, ok := topics[verb]; !ok {
			t.Errorf("`muff help %s` has nothing to say", verb)
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

// TestAPageIsOnlyThatCommand — the whole point of `muff help <command>`.
func TestPageIsNotTheWholeScreen(t *testing.T) {
	for name := range topics {
		page, ok := commandHelp(style.Plain(), name)
		if !ok {
			t.Fatalf("%s has no page", name)
		}
		for _, extra := range []string{"status runs 1 to 4", "scores run 1 to 5", "exit codes:", "ORC_THEME"} {
			if strings.Contains(page, extra) {
				t.Errorf("%s's page carries the whole screen's %q", name, extra)
			}
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

// Both forms of a command with two of them appear on its page: a reader asking about
// `create` wants the subtask form too, and one explanation rather than two.
func TestAllFormsOfACommandAreShown(t *testing.T) {
	page, ok := commandHelp(style.Plain(), "create")
	if !ok {
		t.Fatal("create has no page")
	}
	for _, want := range []string{"<task> <priority> <difficulty>", "--sub <name>"} {
		if !strings.Contains(page, want) {
			t.Errorf("create's page lacks the %q form:\n%s", want, page)
		}
	}
	if strings.Count(page, "create a draft task") != 1 {
		t.Errorf("the summary is repeated once per form:\n%s", page)
	}
}

// othersOf returns the summary lines of every command except the named one, so a
// page can be checked for not being the list.
func othersOf(name string) []string {
	var out []string
	for _, c := range commands {
		if c.name == name || strings.TrimSpace(c.does) == "" {
			continue
		}
		out = append(out, c.does)
	}
	return out
}
