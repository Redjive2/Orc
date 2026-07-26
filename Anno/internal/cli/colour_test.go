package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"orc/anno/internal/cli"
	"orc/anno/internal/style"
	"orc/theme"
)

// strip removes SGR escapes, so a coloured rendering can be compared with the plain
// one it must be a layer over.
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

// runColoured drives a command with an explicit palette pair.
func runColoured(t *testing.T, p style.Palette, args ...string) string {
	t.Helper()

	var out, errOut bytes.Buffer
	cli.Main(cli.App{
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &errOut,
		Out:    p,
		Err:    p,
		Scope:  allow,
	}, args)
	return out.String() + errOut.String()
}

// TestColourIsOnlyEverALayer. Every screen Anno prints must be byte-identical to its
// plain form once the escapes are removed — otherwise colour has become information,
// and a pipe, a NO_COLOR terminal, or an agent loses some of it.
func TestColourStripsToPlain(t *testing.T) {
	dir := workspace(t, map[string]string{
		"a.go": "// @:> section types\n" +
			"// :> symbol Pair\n" +
			"type Pair struct{}\n" +
			"// <:\n" +
			"// <@\n",
		"b.go": "plain file, nothing annotated\n",
	})

	for _, tc := range []struct {
		args    []string
		painted bool
	}{
		{args: []string{"help"}, painted: true},
		{args: []string{"index", dir + "/a.go"}, painted: true},
		{args: []string{"overview", dir}, painted: true},
		{args: []string{"find", dir + "@types"}, painted: true},
		{args: []string{"index", dir + "/missing.go"}, painted: true}, // a failure, on stderr
		{args: []string{"frobnicate"}, painted: true},                 // a usage error, which prints the help

		// `read` emits the span verbatim — no dedent, no trimming, original line
		// endings — which is what makes `read` and `write` inverses of each
		// other. Colouring somebody's file content would break that, so this one
		// is deliberately plain in both palettes.
		{args: []string{"read", dir + "/a.go@types"}, painted: false},
	} {
		plain := runColoured(t, style.Plain(), tc.args...)
		coloured := runColoured(t, style.Coloured(), tc.args...)

		if got := strip(coloured); got != plain {
			t.Errorf("%v differs once stripped.\n got: %q\nwant: %q", tc.args, got, plain)
		}
		if painted := coloured != plain; painted != tc.painted {
			if tc.painted {
				t.Errorf("%v was not coloured at all", tc.args)
			} else {
				t.Errorf("%v was coloured; it emits file content verbatim", tc.args)
			}
		}
	}
}

// The flags are how Orc keeps escapes out of a command it assembles, so they must
// work from either side of the command word.
func TestColourFlagsArePositionIndependent(t *testing.T) {
	for _, args := range [][]string{
		{cli.FlagNoColour, "help"},
		{"help", cli.FlagNoColour},
	} {
		var out, errOut bytes.Buffer
		code := cli.Main(cli.App{
			Stdout: &out,
			Stderr: &errOut,
			Out:    style.Coloured(), // asked for, and overridden by the flag
			Err:    style.Coloured(),
		}, args)

		if code != cli.CodeOK {
			t.Fatalf("%v exited %d: %s", args, code, errOut.String())
		}
		if strings.Contains(out.String(), "\x1b[") {
			t.Errorf("%v still emitted colour", args)
		}
	}
}

// And they are not passed through to the command as arguments.
func TestColourFlagsAreNotArguments(t *testing.T) {
	dir := workspace(t, map[string]string{"a.go": "// @:> section s\nx\n// <@\n"})

	var out, errOut bytes.Buffer
	code := cli.Main(cli.App{
		Stdout: &out,
		Stderr: &errOut,
		Scope:  allow,
	}, []string{"index", dir + "/a.go", cli.FlagNoColour})

	if code != cli.CodeOK {
		t.Errorf("--no-color reached the command as an argument: exit %d\n%s", code, errOut.String())
	}
}

// --color forces colour on a stream that is not a terminal, which is what a caller
// piping Anno into something that renders escapes wants.
func TestColourFlagForces(t *testing.T) {
	var out, errOut bytes.Buffer
	cli.Main(cli.App{
		Stdout: &out,
		Stderr: &errOut,
		Out:    style.Plain(), // a buffer is never a terminal
		Err:    style.Plain(),
		Look:   theme.MapLook(map[string]string{}),
	}, []string{cli.FlagColour, "help"})

	if !strings.Contains(out.String(), "\x1b[") {
		t.Errorf("--color did not force colour on:\n%q", out.String())
	}
}

// TestAgentsNeverGetColour. ORC_AGENT is how Orc turns this off for every tool at
// once, and one command's flag must not defeat it.
func TestAgentsNeverGetColour(t *testing.T) {
	var out, errOut bytes.Buffer
	cli.Main(cli.App{
		Stdout: &out,
		Stderr: &errOut,
		Look:   theme.MapLook(map[string]string{"ORC_AGENT": "1"}),
	}, []string{cli.FlagColour, "help"})

	if strings.Contains(out.String(), "\x1b[") {
		t.Errorf("an agent was sent colour:\n%q", out.String())
	}
}

// A misspelled ORC_THEME leaves the help plain rather than unprintable: refusing to
// draw is a better failure than refusing to say why.
func TestBadThemeStillPrints(t *testing.T) {
	var out, errOut bytes.Buffer
	code := cli.Main(cli.App{
		Stdout: &out,
		Stderr: &errOut,
		Look:   theme.MapLook(map[string]string{"ORC_THEME": "not-a-flavour"}),
	}, []string{cli.FlagColour, "help"})

	if code != cli.CodeOK {
		t.Errorf("help exited %d with a bad theme", code)
	}
	if !strings.Contains(out.String(), "anno") {
		t.Errorf("the help did not print:\n%s", out.String())
	}
}
