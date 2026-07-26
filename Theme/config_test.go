package theme_test

import (
	"os"
	"strings"
	"testing"

	"orc/theme"
)

func env(pairs ...string) theme.Look {
	m := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return theme.MapLook(m)
}

func TestResolveDefaultsToMacchiatoOnATerminal(t *testing.T) {
	got, err := theme.Resolve(env(theme.EnvColorTerm, "truecolor"), true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !got.Palette.Enabled() {
		t.Fatalf("colour should be on for a terminal: %s", got.Reason)
	}
	if got.Palette.Flavour() != theme.Macchiato {
		t.Errorf("Flavour() = %v, want macchiato", got.Palette.Flavour())
	}
	if got.Palette.Depth() != theme.TrueColour {
		t.Errorf("Depth() = %v, want truecolor", got.Palette.Depth())
	}
}

// TestAgentsNeverGetColour is the rule that matters most here. An agent's
// output is an input to another program, and escape sequences in it are
// corruption — so ORC_AGENT wins over everything, including a forced setting.
func TestAgentsNeverGetColour(t *testing.T) {
	for _, tc := range []struct {
		name string
		look theme.Look
	}{
		{"bare", env(theme.EnvAgent, "1")},
		{"set to empty", env(theme.EnvAgent, "")},
		{"set to 0", env(theme.EnvAgent, "0")},
		{"set to false", env(theme.EnvAgent, "false")},
		{"with a theme chosen", env(theme.EnvAgent, "1", theme.EnvTheme, "mocha")},
		{"against CLICOLOR_FORCE", env(theme.EnvAgent, "1", theme.EnvForce, "1")},
		{"against everything", env(
			theme.EnvAgent, "1",
			theme.EnvTheme, "macchiato",
			theme.EnvForce, "1",
			theme.EnvColorTerm, "truecolor",
			theme.EnvTerm, "xterm-256color",
		)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Even on a real terminal.
			got, err := theme.Resolve(tc.look, true)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.Palette.Enabled() {
				t.Errorf("an agent was given colour: %s", got.Reason)
			}
			if !strings.Contains(got.Reason, theme.EnvAgent) {
				t.Errorf("the reason should name %s, got %q", theme.EnvAgent, got.Reason)
			}
			// And the palette really is inert, not merely reported as off.
			if painted := got.Palette.Paint("x", theme.Danger); painted != "x" {
				t.Errorf("an agent's palette painted: %q", painted)
			}
		})
	}
}

func TestResolveOrder(t *testing.T) {
	for _, tc := range []struct {
		name     string
		look     theme.Look
		terminal bool
		want     bool
		says     string
	}{
		{"terminal, nothing set", env(), true, true, "macchiato"},
		{"not a terminal", env(), false, false, "not a terminal"},
		{"NO_COLOR beats a terminal", env(theme.EnvNoColor, ""), true, false, theme.EnvNoColor},
		{"NO_COLOR set to 0 still disables", env(theme.EnvNoColor, "0"), true, false, theme.EnvNoColor},
		{"theme none", env(theme.EnvTheme, "none"), true, false, theme.EnvTheme},
		{"theme off", env(theme.EnvTheme, "off"), true, false, theme.EnvTheme},
		{"dumb terminal", env(theme.EnvTerm, "dumb"), true, false, "dumb"},
		{"dumb in any case", env(theme.EnvTerm, "DUMB"), true, false, "dumb"},
		{"forced through a pipe", env(theme.EnvForce, "1"), false, true, theme.EnvForce},
		{"forced but zero", env(theme.EnvForce, "0"), false, false, "not a terminal"},
		{"NO_COLOR beats force", env(theme.EnvNoColor, "", theme.EnvForce, "1"), true, false, theme.EnvNoColor},
		{"theme none beats force", env(theme.EnvTheme, "none", theme.EnvForce, "1"), true, false, theme.EnvTheme},
		{"dumb beats force", env(theme.EnvTerm, "dumb", theme.EnvForce, "1"), true, false, "dumb"},
		{"empty theme falls back to the default", env(theme.EnvTheme, "  "), true, true, "macchiato"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := theme.Resolve(tc.look, tc.terminal)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.Palette.Enabled() != tc.want {
				t.Errorf("Enabled() = %v, want %v (%s)", got.Palette.Enabled(), tc.want, got.Reason)
			}
			if !strings.Contains(got.Reason, tc.says) {
				t.Errorf("Reason = %q, should mention %q", got.Reason, tc.says)
			}
		})
	}
}

// TestSessionConfigurable: the whole point of ORC_THEME is that an operator
// exports it once and every tool follows.
func TestSessionConfigurable(t *testing.T) {
	for _, tc := range []struct {
		set  string
		want theme.Flavour
	}{
		{"macchiato", theme.Macchiato},
		{"mocha", theme.Mocha},
		{"frappe", theme.Frappe},
		{"latte", theme.Latte},
	} {
		got, err := theme.Resolve(env(theme.EnvTheme, tc.set), true)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", tc.set, err)
		}
		if got.Palette.Flavour() != tc.want {
			t.Errorf("ORC_THEME=%s gave %v", tc.set, got.Palette.Flavour())
		}
		if !strings.Contains(got.Reason, tc.set) {
			t.Errorf("Reason = %q, should name the flavour", got.Reason)
		}
	}
}

// TestBadThemeIsReported: a typo that silently fell back to the default would
// be invisible, and the operator would conclude the setting does not work.
func TestBadThemeIsReported(t *testing.T) {
	got, err := theme.Resolve(env(theme.EnvTheme, "dracula"), true)
	if err == nil {
		t.Fatal("a bad theme should be reported")
	}
	if !strings.Contains(err.Error(), "dracula") || !strings.Contains(err.Error(), "macchiato") {
		t.Errorf("the error should name the bad value and the options: %v", err)
	}
	if got.Palette.Enabled() {
		t.Error("a failed resolution should not hand back a working palette")
	}

	// It is reported even when something else would have disabled colour
	// anyway: being told about the typo matters more than the answer.
	if _, err := theme.Resolve(env(theme.EnvTheme, "dracula", theme.EnvAgent, "1"), false); err == nil {
		t.Error("a bad theme should be reported even for an agent")
	}
}

func TestDepthDetection(t *testing.T) {
	for _, tc := range []struct {
		colorterm string
		set       bool
		want      theme.Depth
	}{
		{"truecolor", true, theme.TrueColour},
		{"TrueColor", true, theme.TrueColour},
		{"24bit", true, theme.TrueColour},
		{"", true, theme.Ansi256},
		{"yes", true, theme.Ansi256},
		{"", false, theme.Ansi256},
	} {
		look := env()
		if tc.set {
			look = env(theme.EnvColorTerm, tc.colorterm)
		}
		got, err := theme.Resolve(look, true)
		if err != nil {
			t.Fatal(err)
		}
		if got.Palette.Depth() != tc.want {
			t.Errorf("COLORTERM=%q set=%v gave depth %v, want %v", tc.colorterm, tc.set, got.Palette.Depth(), tc.want)
		}
	}
}

// Windows Terminal is the default terminal on Windows 11, renders 24-bit
// colour, and advertises it no other way — so without this every Orc tool there
// would quantise a palette the terminal could have shown exactly.
func TestWindowsTerminalIsTakenAtItsWord(t *testing.T) {
	got, err := theme.Resolve(env(theme.EnvWTSession, "3f2504e0-4f89-11d3-9a0c-0305e82c3301"), true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Palette.Depth() != theme.TrueColour {
		t.Errorf("depth = %v, want true colour", got.Palette.Depth())
	}

	// Set but empty is not Windows Terminal saying anything, and it must not be
	// read as a claim about the terminal's depth.
	got, err = theme.Resolve(env(theme.EnvWTSession, ""), true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Palette.Depth() != theme.Ansi256 {
		t.Errorf("an empty %s gave depth %v", theme.EnvWTSession, got.Palette.Depth())
	}
}

// TestResolveWithoutALookReadsTheProcess checks the production path. Whatever
// the environment holds, resolution must produce a usable answer.
func TestResolveWithoutALookReadsTheProcess(t *testing.T) {
	t.Setenv(theme.EnvTheme, "mocha")
	t.Setenv(theme.EnvColorTerm, "truecolor")

	got, err := theme.Resolve(nil, true)
	if err != nil {
		t.Fatalf("Resolve(nil): %v", err)
	}
	if got.Palette.Flavour() != theme.Mocha {
		t.Errorf("Flavour() = %v, want mocha", got.Palette.Flavour())
	}
}

func TestIsTerminalAndForStream(t *testing.T) {
	// A regular file is never a terminal, which is the case every test and
	// every redirected command runs under.
	f, err := createTemp(t)
	if err != nil {
		t.Fatal(err)
	}
	if theme.IsTerminal(f) {
		t.Error("a regular file should not be a terminal")
	}
	if theme.IsTerminal(nil) {
		t.Error("nil should not be a terminal")
	}

	got, err := theme.ForStream(f, env())
	if err != nil {
		t.Fatal(err)
	}
	if got.Palette.Enabled() {
		t.Errorf("a file stream got colour: %s", got.Reason)
	}
}

func TestHelpNamesTheSettings(t *testing.T) {
	help := theme.Help()
	for _, want := range []string{theme.EnvTheme, theme.EnvAgent, theme.EnvNoColor, "macchiato"} {
		if !strings.Contains(help, want) {
			t.Errorf("Help() should mention %q:\n%s", want, help)
		}
	}
}

// createTemp gives a real *os.File that is definitely not a terminal.
func createTemp(t *testing.T) (*os.File, error) {
	t.Helper()
	return os.CreateTemp(t.TempDir(), "stream-*")
}
