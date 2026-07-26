package cli_test

import (
	"strings"
	"testing"

	"orc/common/fault"
)

// strip removes SGR escape sequences, so a coloured rendering can be compared
// with the plain one it must be a layer over.
func strip(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			i = j + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// TestColourIsOnlyEverALayer. Every screen muff prints must be byte-identical
// to its plain form once the escapes are removed — otherwise colour has become
// information, and a pipe, a NO_COLOR terminal, or an agent loses some of it.
func TestColourStripsToPlain(t *testing.T) {
	runs := [][]string{
		{"help"},
		{"create", "fix-the-parser", "4", "3"},
		{"scope", "fix-the-parser", "internal/tree"},
		{"claim", "fix-the-parser"},
		{"status", "fix-the-parser", "2"},
		{"create", "fix-the-parser", "--sub", "write-the-tests"},
		{"invite", "bob", "fix-the-parser"},
		{"complete", "fix-the-parser", "--sub", "write-the-tests"},
		{"info", "fix-the-parser"},
		{"pool"},
		{"pool", "--all"},
		{"verify"},
		{"leave", "fix-the-parser"},   // a refusal: the owner cannot leave
		{"push"},                      // a usage error, which prints the help too
		{"info", "nothing-like-this"}, // a not-found
	}

	for _, args := range runs {
		// Two stores, so the two runs see identical state rather than each
		// other's leftovers.
		plain, coloured := newRig(t), newRig(t)
		plain.worktree(t, "internal/tree")
		coloured.worktree(t, "internal/tree")

		var got, want string
		for _, r := range []*rig{plain, coloured} {
			flags := []string{"--no-color"}
			if r == coloured {
				flags = []string{"--color"}
			}
			out := r.run("alice", append(flags, args...)...)
			text := out.stdout + out.stderr
			if r == plain {
				want = text
			} else {
				got = text
			}
		}

		// Each run has its own store, and the path appears in the output, so the
		// roots are normalised before comparing. Without this the test would be
		// comparing tempdir names rather than colour.
		got = strings.ReplaceAll(got, coloured.storePath(), "<store>")
		want = strings.ReplaceAll(want, plain.storePath(), "<store>")

		if strip(got) != want {
			t.Errorf("%v differs once stripped.\n got: %q\nwant: %q", args, strip(got), want)
		}
		if got == want && strings.Contains(strings.Join(args, " "), "help") {
			t.Errorf("%v was not coloured at all under --color", args)
		}
	}
}

// The flags are the facility Orc will use to keep colour out of the way, so
// they must work from either side of the command word.
func TestColourFlagsArePositionIndependent(t *testing.T) {
	r := newRig(t)

	for _, args := range [][]string{
		{"--no-color", "help"},
		{"help", "--no-color"},
	} {
		got := r.run("alice", args...)
		if got.code != fault.CodeOK {
			t.Fatalf("%v exited %d: %s", args, got.code, got.stderr)
		}
		if strings.Contains(got.stdout, "\x1b[") {
			t.Errorf("%v still emitted colour", args)
		}
	}

	// And they are not passed through to the command as arguments.
	if got := r.run("alice", "create", "a-task", "3", "3", "--no-color"); got.code != fault.CodeOK {
		t.Errorf("--no-color reached the command as an argument: exit %d\n%s", got.code, got.stderr)
	}
}

// An agent gets plain output whatever the flags say: ORC_AGENT is how Orc turns
// this off for every tool at once, and a flag must not defeat it.
func TestAgentsNeverGetColour(t *testing.T) {
	r := newRig(t)
	r.env["ORC_AGENT"] = "1"

	got := r.run("alice", "--color", "help")
	if strings.Contains(got.stdout, "\x1b[") {
		t.Errorf("an agent was sent colour:\n%q", got.stdout)
	}
}
