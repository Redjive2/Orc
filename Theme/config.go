package theme

import (
	"os"
	"strings"
)

// The environment every Orc tool reads to decide how it should look.
const (
	// EnvTheme selects the flavour: macchiato, mocha, frappe, latte, or none.
	// It is the setting an operator changes for a shell session.
	EnvTheme = "ORC_THEME"

	// EnvAgent marks a process that Orc spawned to do work rather than to be
	// read by a person. When it is set, output is always plain.
	EnvAgent = "ORC_AGENT"

	// EnvNoColor is the no-color.org convention, honoured by presence.
	EnvNoColor = "NO_COLOR"

	// EnvForce turns colour on even when the stream is not a terminal, which is
	// how a person pipes coloured output into a pager.
	EnvForce = "CLICOLOR_FORCE"

	// EnvTerm names the terminal type; "dumb" cannot render colour.
	EnvTerm = "TERM"

	// EnvColorTerm advertises 24-bit support.
	EnvColorTerm = "COLORTERM"

	// EnvWTSession is set by Windows Terminal, which supports 24-bit colour and
	// advertises it no other way.
	EnvWTSession = "WT_SESSION"
)

// Look reads a variable, reporting whether it was set at all — a distinction
// NO_COLOR depends on, since it counts when set to anything including empty.
type Look func(key string) (string, bool)

// OSLook reads the process environment.
func OSLook(key string) (string, bool) { return os.LookupEnv(key) }

// MapLook reads an injected environment, for tests.
func MapLook(m map[string]string) Look {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

// Config is a resolved decision about how a stream should look, along with the
// reason — which is what lets a tool explain itself when someone asks why their
// terminal is grey.
type Config struct {
	Palette Palette
	Reason  string
}

// Resolve decides how one stream should be drawn.
//
// The order is deliberate, and every step that can say "no colour" is checked
// before any step that can say "yes":
//
//  1. ORC_AGENT — an agent's output is data for another program, and escape
//     sequences in it are corruption. This is absolute: an agent does not get
//     colour even if something else in the environment asks for it.
//  2. NO_COLOR — the standard opt-out, honoured by presence.
//  3. ORC_THEME=none — the operator turned it off explicitly.
//  4. TERM=dumb — the terminal has said it cannot.
//  5. CLICOLOR_FORCE — the operator wants colour through a pipe.
//  6. otherwise, colour only when the stream really is a terminal.
//
// An unreadable ORC_THEME is reported rather than ignored: a typo that silently
// fell back to the default would be invisible, and the operator would conclude
// the setting does not work.
func Resolve(look Look, terminal bool) (Config, error) {
	if look == nil {
		look = OSLook
	}

	// The flavour is parsed first, so a bad value is reported even when
	// something else would have disabled colour anyway. Being told about the
	// typo matters more than the answer it would not have changed.
	flavour := Default
	if raw, ok := look(EnvTheme); ok && strings.TrimSpace(raw) != "" {
		parsed, err := ParseFlavour(raw)
		if err != nil {
			return Config{Reason: EnvTheme + " is not a known theme"}, err
		}
		flavour = parsed
	}

	if _, ok := look(EnvAgent); ok {
		return Config{Reason: EnvAgent + " is set: agent output is always plain"}, nil
	}
	if _, ok := look(EnvNoColor); ok {
		return Config{Reason: EnvNoColor + " is set"}, nil
	}
	if flavour == Plain {
		return Config{Reason: EnvTheme + " is none"}, nil
	}
	if term, ok := look(EnvTerm); ok && strings.EqualFold(strings.TrimSpace(term), "dumb") {
		return Config{Reason: "the terminal is dumb"}, nil
	}

	forced := false
	if v, ok := look(EnvForce); ok && strings.TrimSpace(v) != "0" {
		forced = true
	}
	if !forced && !terminal {
		return Config{Reason: "the stream is not a terminal"}, nil
	}

	depth := depthFor(look)
	reason := flavour.String()
	if forced {
		reason += " (forced by " + EnvForce + ")"
	}
	return Config{Palette: New(flavour, depth), Reason: reason}, nil
}

// depthFor decides how much colour the terminal can show.
//
// COLORTERM is the only portable signal for 24-bit support, and terminals that
// have it set it. Everything else gets the 256-colour cube, which every colour
// terminal has had for decades — the palette is approximated rather than
// abandoned.
func depthFor(look Look) Depth {
	if v, ok := look(EnvColorTerm); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "truecolor", "24bit":
			return TrueColour
		}
	}
	// Windows Terminal renders 24-bit colour and sets no COLORTERM to say so.
	// It is the default terminal on Windows 11, so without this every Orc tool
	// there would quantise a palette the terminal could have shown exactly.
	if v, ok := look(EnvWTSession); ok && strings.TrimSpace(v) != "" {
		return TrueColour
	}
	return Ansi256
}

// IsTerminal reports whether a file is attached to a character device.
//
// Anything that is not an *os.File — a buffer in a test, a pipe wrapper — is
// not a terminal, which is the conservative answer.
func IsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// ForStream resolves how a particular stream should be drawn. It is the call a
// tool's main makes, once per stream.
func ForStream(f *os.File, look Look) (Config, error) {
	return Resolve(look, IsTerminal(f))
}

// Help describes the settings, for a tool's help text. Every Orc tool prints
// the same words, because they describe the same behaviour.
func Help() string {
	return "colour: " + EnvTheme + "=" + strings.Join(Flavours(), "|") +
		" (default " + Default.String() + ")\n" +
		"        " + EnvNoColor + " disables it; " + EnvAgent + " forces plain output for agents"
}
