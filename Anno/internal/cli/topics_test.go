package cli

import (
	"strings"
	"testing"

	"orc/anno/internal/style"
)

// TestEveryVerbHasAPage. A command with no page is one `anno help <it>` refuses to
// explain, which is worse than no per-command help at all.
func TestEveryVerbHasAPage(t *testing.T) {
	for _, verb := range verbs() {
		if verb == "help" {
			continue // help explains itself by being run
		}
		if _, ok := commandHelp(style.Plain(), verb); !ok {
			t.Errorf("`anno help %s` has nothing to say", verb)
		}
	}
}

// Every command in the table has the detail its page is for.
func TestEveryCommandHasDetail(t *testing.T) {
	for _, c := range commands {
		if strings.TrimSpace(c.detail) == "" {
			t.Errorf("%s has no detail, so its page would be the list line twice", c.name)
		}
		if len(c.examples) == 0 {
			t.Errorf("%s has no examples", c.name)
		}
	}
}

// TestAPageIsOnlyThatCommand — the whole point of `anno help <command>`.
func TestPageIsNotTheWholeScreen(t *testing.T) {
	for _, c := range commands {
		page, ok := commandHelp(style.Plain(), c.name)
		if !ok {
			t.Fatalf("%s has no page", c.name)
		}
		for _, extra := range []string{"a chain addresses an annotation", "exit codes:", "ORC_THEME"} {
			if strings.Contains(page, extra) {
				t.Errorf("%s's page carries the whole screen's %q", c.name, extra)
			}
		}
		// It is not the menu. A line-count ratio was the first version of this
		// check and it was the wrong one: a thorough page for a tool with five
		// commands is legitimately half the size of that tool's whole screen.
		// What must not be there is *other commands*.
		for _, other := range othersOf(c.name) {
			if strings.Contains(page, other) {
				t.Errorf("%s's page lists %q, which is another command's line", c.name, other)
			}
		}
		// And it is about the command that was asked for.
		if !strings.Contains(page, "anno "+c.name) {
			t.Errorf("%s's page does not name it:\n%s", c.name, page)
		}
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
