// Package style adds colour to Anno's output.
//
// A Palette is a value: the zero Palette is plain, so every code path that has
// not asked for colour gets none, and tests comparing exact bytes keep working
// without opting out of anything.
//
// Colour is applied only when writing, never while measuring. Escape sequences
// occupy no columns, so a table that is aligned in plain text stays aligned in
// colour — the layout pass never sees a code.
//
// The colours come from orc/theme, the scheme every Orc tool shares. Nothing
// here decides what a name or a range looks like; it only decides what *is* a
// name or a range. That is what lets one setting restyle every tool at once,
// and what stops `anno` and `mailman` disagreeing about what green means.
package style

import (
	"orc/theme"
)

// Ink is the role a piece of Anno's output plays. It names what the text is,
// not what colour it should be.
type Ink int

const (
	// None leaves text untouched.
	None Ink = iota
	// Name is an annotation's name: the thing being looked for.
	Name
	// Quiet is structure that should recede — rules, indent guides, counts.
	Quiet
	// Meta is an annotation's metadata list.
	Meta
	// Span is a line range.
	Span
	// Section is a section annotation.
	Section
	// Symbol is a symbol annotation.
	Symbol
	// Part is a part annotation.
	Part
	// Good reports that something was done.
	Good
	// Alarm reports a failure.
	Alarm

	// The inks below are for prose rather than for an index: the help text, and
	// the advice a refusal ends with. They exist because a tool whose index is
	// coloured and whose sentences are not looks half-finished — and they are
	// the same roles `muff`, `cq`, and `orc` paint their help with, so no two
	// tools disagree about what a command name looks like.

	// Tool is anno's own name, where it introduces itself.
	Tool
	// Heading is a section label in the help.
	Heading
	// Command is a command name, in help and in advice.
	Command
	// Value is a placeholder or a literal a caller would type.
	Value
	// Setting is an environment variable.
	Setting
	inkCount
)

// Valid reports whether i is a defined ink.
func (i Ink) Valid() bool { return i >= None && i < inkCount }

// paints maps each ink to a role in the shared scheme, and whether it is drawn
// with extra weight. The three annotation kinds take three visually distinct
// roles, because telling them apart at a glance is the whole point of colouring
// an index.
var paints = [inkCount]struct {
	role   theme.Role
	strong bool
}{
	None:    {},
	Name:    {role: theme.Heading},
	Quiet:   {role: theme.Muted},
	Meta:    {role: theme.Accent},
	Span:    {role: theme.Info},
	Section: {role: theme.Secondary, strong: true},
	Symbol:  {role: theme.Tertiary, strong: true},
	Part:    {role: theme.Success, strong: true},
	Good:    {role: theme.Success},
	Alarm:   {role: theme.Danger, strong: true},
	Tool:    {role: theme.Title, strong: true},
	Heading: {role: theme.Heading, strong: true},
	Command: {role: theme.Primary, strong: true},
	Value:   {role: theme.Secondary},
	Setting: {role: theme.Info},
}

// Palette renders text with a role, or plainly. The zero value is plain.
type Palette struct {
	inner theme.Palette
}

// New wraps a resolved scheme.
func New(p theme.Palette) Palette { return Palette{inner: p} }

// Plain returns a palette that never colours anything.
func Plain() Palette { return Palette{} }

// Coloured returns a palette in the default flavour, for tests that want to see
// the sequences without owning a terminal.
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
