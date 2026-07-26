package cli_test

import (
	"bytes"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"orc/common/fault"
	"orc/dock/internal/cli"
	"orc/dock/internal/hook"
	"orc/dock/internal/style"
	"orc/theme"
)

var escapes = regexp.MustCompile("\x1b\\[[0-9;]*m")

// coloured drives a command with colour forced on through the flag, which is
// the path a caller assembling one command takes.
func coloured(t *testing.T, look theme.Look, args ...string) (string, string, int) {
	t.Helper()
	var out, errs bytes.Buffer
	app := cli.New(&out, &errs, style.Plain())
	app.Look = look
	code := app.Main(append([]string{cli.FlagColour}, args...))
	return out.String(), errs.String(), code
}

// TestColourStripsToPlain is the house rule made a test: colour is a layer, so
// removing every escape sequence must return exactly what a pipe would have
// seen. If it does not, colour has become information.
func TestColourStripsToPlain(t *testing.T) {
	dir := corpus(t)
	guide := filepath.Join(dir, "guide.md")
	env := theme.MapLook(map[string]string{})

	// read is absent deliberately: its output is a document's content, not
	// dock's presentation of anything, and TestReadIsNeverColoured covers why.
	for _, args := range [][]string{
		{"help"},
		{"index", guide},
		{"links", guide + "§1.1"},
		{"check", dir},
		{"overview", dir},
		{"find", dir + "§1.1"},
		{"read", guide + "§1", "--follow"},
	} {
		t.Run(strings.Join(args[:1], " "), func(t *testing.T) {
			plain, plainErr, _ := run(t, args...)
			painted, paintedErr, _ := coloured(t, env, args...)

			if painted == plain && paintedErr == plainErr {
				t.Fatalf("%v produced no colour at all", args)
			}
			if got := escapes.ReplaceAllString(painted, ""); got != plain {
				t.Errorf("stdout differs once stripped:\n--- stripped ---\n%s\n--- plain ---\n%s", got, plain)
			}
			if got := escapes.ReplaceAllString(paintedErr, ""); got != plainErr {
				t.Errorf("stderr differs once stripped:\n--- stripped ---\n%s\n--- plain ---\n%s", got, plainErr)
			}
		})
	}
}

// TestColourFlagsArePositionIndependent: the flags are global, taken off the
// line before dispatch, so no command has to know they exist.
func TestColourFlagsArePositionIndependent(t *testing.T) {
	dir := corpus(t)
	guide := filepath.Join(dir, "guide.md")
	env := theme.MapLook(map[string]string{})

	for _, args := range [][]string{
		{cli.FlagColour, "index", guide},
		{"index", cli.FlagColour, guide},
		{"index", guide, cli.FlagColour},
	} {
		var out, errs bytes.Buffer
		app := cli.New(&out, &errs, style.Plain())
		app.Look = env
		if code := app.Main(args); code != fault.CodeOK {
			t.Fatalf("%v: code = %d, stderr = %s", args, code, errs.String())
		}
		if !strings.Contains(out.String(), "\x1b[") {
			t.Errorf("%v produced no colour", args)
		}
	}

	// And the flag never reaches the command as an argument.
	if _, _, code := run(t, "index", guide, cli.FlagNoColour); code != fault.CodeOK {
		t.Errorf("%s was taken for an argument: code = %d", cli.FlagNoColour, code)
	}
}

// TestNoColourWins. --no-color is the side that can say no, and every step that
// can say no is checked before any step that can say yes.
func TestNoColourWins(t *testing.T) {
	dir := corpus(t)
	guide := filepath.Join(dir, "guide.md")

	var out, errs bytes.Buffer
	app := cli.New(&out, &errs, style.Coloured())
	app.Look = theme.MapLook(map[string]string{})
	if code := app.Main([]string{"index", guide, cli.FlagNoColour}); code != fault.CodeOK {
		t.Fatalf("code = %d", code)
	}
	if strings.Contains(out.String(), "\x1b[") {
		t.Errorf("%s did not turn colour off:\n%q", cli.FlagNoColour, out.String())
	}

	// Both flags at once: no wins.
	out.Reset()
	app = cli.New(&out, &errs, style.Coloured())
	app.Look = theme.MapLook(map[string]string{})
	app.Main([]string{cli.FlagColour, cli.FlagNoColour, "index", guide})
	if strings.Contains(out.String(), "\x1b[") {
		t.Errorf("--color beat --no-color:\n%q", out.String())
	}
}

// TestAgentsNeverGetColour. ORC_AGENT is absolute: it beats --color, a chosen
// flavour, and a real terminal. An agent's output is an input to another
// program, and escape sequences in it are corruption rather than decoration.
func TestAgentsNeverGetColour(t *testing.T) {
	dir := corpus(t)
	guide := filepath.Join(dir, "guide.md")
	agent := theme.MapLook(map[string]string{"ORC_AGENT": "1"})

	out, errs, code := coloured(t, agent, "index", guide)
	if code != fault.CodeOK {
		t.Fatalf("code = %d, stderr = %s", code, errs)
	}
	if strings.Contains(out, "\x1b[") {
		t.Errorf("an agent was given colour despite ORC_AGENT:\n%q", out)
	}

	// The same holds for CLICOLOR_FORCE, which ORC_AGENT also outranks.
	forced := theme.MapLook(map[string]string{"ORC_AGENT": "1", "CLICOLOR_FORCE": "1"})
	out, _, _ = coloured(t, forced, "index", guide)
	if strings.Contains(out, "\x1b[") {
		t.Errorf("CLICOLOR_FORCE beat ORC_AGENT:\n%q", out)
	}
}

// TestTheFlavourIsHonoured: --color re-resolves through the shared scheme, so
// ORC_THEME still decides which colours appear.
func TestTheFlavourIsHonoured(t *testing.T) {
	dir := corpus(t)
	guide := filepath.Join(dir, "guide.md")

	macchiato, _, _ := coloured(t, theme.MapLook(map[string]string{"ORC_THEME": "macchiato"}), "index", guide)
	latte, _, _ := coloured(t, theme.MapLook(map[string]string{"ORC_THEME": "latte"}), "index", guide)

	if macchiato == latte {
		t.Error("two flavours produced identical output; ORC_THEME was ignored")
	}
	// Both still strip to the same plain text: a flavour changes the colours,
	// never the layout.
	if escapes.ReplaceAllString(macchiato, "") != escapes.ReplaceAllString(latte, "") {
		t.Error("a flavour changed the layout")
	}
}

// TestAMisspelledThemeIsAUsageError: a setting that quietly does nothing is one
// the operator concludes is broken.
func TestAMisspelledThemeIsAUsageError(t *testing.T) {
	if _, err := theme.Resolve(theme.MapLook(map[string]string{"ORC_THEME": "mocchiato"}), true); err == nil {
		t.Error("a misspelled flavour resolved silently")
	}
}

// TestReadIsNeverColoured. A section's content is emitted verbatim whatever the
// palette says, because read and write are inverses: colouring the bytes on the
// way out would mean write could never put them back.
//
// The headers --follow adds around *other* sections are dock's own words and are
// painted; the content between them is not.
func TestReadIsNeverColoured(t *testing.T) {
	dir := corpus(t)
	guide := filepath.Join(dir, "guide.md")
	env := theme.MapLook(map[string]string{})

	plain, _, _ := run(t, "read", guide+"§1.2")
	painted, _, _ := coloured(t, env, "read", guide+"§1.2")
	if painted != plain {
		t.Errorf("read painted a document's content:\n%q", painted)
	}
	if strings.Contains(painted, "\x1b[") {
		t.Errorf("content carries escape sequences:\n%q", painted)
	}

	// A section reached *through* --follow is verbatim too. §1's own prose cites
	// grammar.md§2, so following from §1 must carry that section's content
	// unchanged, however painted the header above it is.
	reached, _, _ := run(t, "read", filepath.Join(dir, "grammar.md")+"§2")
	followed, _, _ := coloured(t, env, "read", guide+"§1", "--follow")
	for _, line := range strings.Split(reached, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.Contains(followed, line) {
			t.Errorf("--follow altered a line of the content it pulled in: %q", line)
		}
	}
	if !strings.Contains(followed, "\x1b[") {
		t.Error("--follow painted none of its own headers")
	}
}

// TestTheHookIsNeverColoured: its output is read by a model, not a terminal.
// The hook takes no palette at all, which is the structural version of the rule
// — this asserts the behaviour so that adding one later fails here.
func TestTheHookIsNeverColoured(t *testing.T) {
	dir := corpus(t)
	path := filepath.Join(dir, "guide.md")
	in := []byte(`{"hook_event_name":"PostToolUse","tool_name":"Read","tool_input":{"file_path":"` + path + `"}}`)

	out := hook.Run(in)
	if len(out) == 0 {
		t.Fatal("the hook said nothing about a document with sections")
	}
	if bytes.Contains(out, []byte("\x1b[")) {
		t.Errorf("the hook emitted escape sequences:\n%s", out)
	}
}
