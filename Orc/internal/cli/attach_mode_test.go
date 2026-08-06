package cli_test

import (
	"strings"
	"testing"
)

// Which mode an attach lands in.
//
// It has been both ways round, so it is worth a test rather than a comment. The
// pane is the default: attaching to the raw terminal puts every keystroke into a
// working agent's session, and a mistyped key there is a prompt it acts on. The
// pane buffers instead and sends on ^S, and ^] hands the terminal over from
// inside it when the raw view is what is wanted.
func TestAttachDefaultsToTheComposedPane(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember")

	// Neither mode can run without a terminal, and they say so differently —
	// which is what makes this readable without a tty.
	const wantsPane = "needs a terminal to draw in"
	const wantsRaw = "--direct needs a terminal"

	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"attach", "ember"}, wantsPane},
		{[]string{"attach", "ember", "--view"}, wantsPane},
		{[]string{"attach", "ember", "--direct"}, wantsRaw},
	} {
		got := r.run("boss", c.args...)
		if !strings.Contains(got.stderr, c.want) {
			t.Errorf("%v did not reach the expected mode (wanted %q):\n%s",
				c.args, c.want, got.stderr)
		}
	}

	// Asking for both is a question with two answers.
	if got := r.run("boss", "attach", "ember", "--view", "--direct"); !strings.Contains(
		got.stderr, "name one") {
		t.Errorf("both flags together should be refused:\n%s", got.stderr)
	}
}
