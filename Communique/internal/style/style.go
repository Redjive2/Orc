// Package style gives cq's terminal output its colours.
//
// An Ink names the *role* a piece of text plays — a command, a flag, a value,
// a warning — never a colour. The mapping from role to colour lives in
// orc/theme, the scheme every Orc tool shares, so one setting restyles all of
// them and `cq` and `anno` cannot disagree about what green means.
//
// Colour is applied only when writing, never while measuring. Escape sequences
// occupy no columns, so a table aligned in plain text stays aligned in colour.
package style

import "orc/theme"

// Ink is the role a piece of cq's output plays.
type Ink int

const (
	// None leaves text untouched.
	None Ink = iota
	// Tool is cq's own name, where it introduces itself.
	Tool
	// Heading is a section label.
	Heading
	// Command is a command or subcommand name.
	Command
	// Flag is a flag name.
	Flag
	// Value is a placeholder or a literal a caller would type.
	Value
	// Setting is an environment variable.
	Setting
	// Quiet is prose that should recede: descriptions, notes, rules.
	Quiet
	// Frame is box drawing, which should recede behind the content.
	Frame
	// Good reports a satisfied condition.
	Good
	// Warn marks something needing attention.
	Warn
	// Alarm reports a failure.
	Alarm
	inkCount
)

// Valid reports whether i is a defined ink.
func (i Ink) Valid() bool { return i >= None && i < inkCount }

// paints maps each ink to a role in the shared scheme, and whether it is drawn
// with extra weight.
var paints = [inkCount]struct {
	role   theme.Role
	strong bool
}{
	None:    {},
	Tool:    {role: theme.Title, strong: true},
	Heading: {role: theme.Heading, strong: true},
	Command: {role: theme.Primary, strong: true},
	Flag:    {role: theme.Tertiary},
	Value:   {role: theme.Secondary},
	Setting: {role: theme.Info},
	Quiet:   {role: theme.Muted},
	Frame:   {role: theme.Frame},
	Good:    {role: theme.Success},
	Warn:    {role: theme.Warning},
	Alarm:   {role: theme.Danger, strong: true},
}

// Palette renders text with a role, or plainly. The zero value is plain, so a
// caller that has said nothing about colour gets none — which is what a pipe,
// a hook, and a test all want.
type Palette struct {
	inner theme.Palette
}

// New wraps a resolved scheme.
func New(p theme.Palette) Palette { return Palette{inner: p} }

// Plain returns a palette that never colours anything.
func Plain() Palette { return Palette{} }

// Coloured returns a palette in the default flavour, for tests that want to
// see the sequences without owning a terminal.
func Coloured() Palette {
	return Palette{inner: theme.New(theme.Default, theme.TrueColour)}
}

// Enabled reports whether the palette emits escape sequences.
func (p Palette) Enabled() bool { return p.inner.Enabled() }

// Scheme returns the underlying palette, for naming the flavour in force.
func (p Palette) Scheme() theme.Palette { return p.inner }

// Paint wraps text in a role. A disabled palette, the None ink, an undefined
// ink, or empty text all return the text untouched, so Paint is always safe to
// call and never lengthens output that would not benefit.
func (p Palette) Paint(text string, ink Ink) string {
	if !p.Enabled() || text == "" || ink == None || !ink.Valid() {
		return text
	}
	spec := paints[ink]
	if spec.strong {
		return p.inner.Strong(text, spec.role)
	}
	return p.inner.Paint(text, spec.role)
}
