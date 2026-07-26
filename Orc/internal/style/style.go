// Package style maps Orc's vocabulary onto the shared colour scheme.
//
// Nothing here decides what a colour looks like — orc/theme does — and nothing
// outside here names a role. The drawing code says what a thing *is*: an
// identity, an authority level, a capped permission, a dead session. That
// indirection is what lets one setting restyle every Orc tool at once.
//
// The house rule: colour is a layer, never information. Every role below is
// redundant with a glyph or a word at the call site, so a pipe through grep and a
// NO_COLOR terminal lose nothing but the pleasure.
package style

import "orc/theme"

// Palette renders text in a role. The zero value is plain, which is what makes
// "colour off" the trivially safe path and lets every golden test compare exact
// bytes.
type Palette struct {
	inner theme.Palette
}

// New wraps a resolved scheme.
func New(p theme.Palette) Palette { return Palette{inner: p} }

// Plain returns a palette that never colours anything.
func Plain() Palette { return Palette{} }

// Coloured returns a palette in the default flavour, so a test can assert the
// sequences appear without owning a terminal.
func Coloured() Palette { return Palette{inner: theme.New(theme.Default, theme.TrueColour)} }

// Enabled reports whether this palette emits escape sequences.
func (p Palette) Enabled() bool { return p.inner.Enabled() }

// Scheme returns the underlying palette.
func (p Palette) Scheme() theme.Palette { return p.inner }

// The fleet's own vocabulary.

// Title styles the name of the thing being shown.
func (p Palette) Title(s string) string { return p.inner.Paint(s, theme.Title) }

// Header styles a column label or a field name.
func (p Palette) Header(s string) string { return p.inner.Paint(s, theme.Heading) }

// Frame styles box drawing, which should recede behind the content.
func (p Palette) Frame(s string) string { return p.inner.Paint(s, theme.Frame) }

// Muted styles secondary text: counts, timestamps, an empty column.
func (p Palette) Muted(s string) string { return p.inner.Paint(s, theme.Muted) }

// Identity styles an agent's name.
func (p Palette) Identity(s string) string { return p.inner.Paint(s, theme.Success) }

// Operator styles the one identity at the top of the tree. It is the same green
// as any other agent, emphasised — the operator is an identity like the rest, and
// a different colour would suggest a different kind of thing.
func (p Palette) Operator(s string) string { return p.inner.Strong(s, theme.Success) }

// Role styles a role name.
func (p Palette) Role(s string) string { return p.inner.Paint(s, theme.Info) }

// Authority styles an authority level.
func (p Palette) Authority(s string) string { return p.inner.Paint(s, theme.Primary) }

// Permission styles a permission name.
func (p Palette) Permission(s string) string { return p.inner.Paint(s, theme.Secondary) }

// Path styles a pattern's path argument.
func (p Palette) Path(s string) string { return p.inner.Paint(s, theme.Tertiary) }

// Capped styles a value the boss chain lowered. It wears the warning colour
// because it is the one number on a card that is not what somebody asked for,
// and the card says why in words beside it.
func (p Palette) Capped(s string) string { return p.inner.Paint(s, theme.Warning) }

// Granted styles a permission that came from a grant rather than a role —
// something with an expiry, and therefore something worth noticing.
func (p Palette) Granted(s string) string { return p.inner.Paint(s, theme.Accent) }

// Live styles a populated session.
func (p Palette) Live(s string) string { return p.inner.Paint(s, theme.Success) }

// Idle styles a session that is up but not working.
func (p Palette) Idle(s string) string { return p.inner.Paint(s, theme.Muted) }

// Dead styles a session that exited.
func (p Palette) Dead(s string) string { return p.inner.Strong(s, theme.Danger) }

// The inks below are for prose — help text, confirmations, notes — rather than
// for the fleet. They are the same roles every other Orc tool paints its terminal
// output with, so the tools cannot disagree about what a command name looks like.

// Tool styles orc's own name, where it introduces itself.
func (p Palette) Tool(s string) string { return p.inner.Strong(s, theme.Title) }

// Command styles a command name, in help and in "try this instead" advice.
func (p Palette) Command(s string) string { return p.inner.Strong(s, theme.Primary) }

// Flag styles a flag name.
func (p Palette) Flag(s string) string { return p.inner.Paint(s, theme.Tertiary) }

// Value styles a placeholder or a literal a caller would type.
func (p Palette) Value(s string) string { return p.inner.Paint(s, theme.Secondary) }

// Setting styles an environment variable.
func (p Palette) Setting(s string) string { return p.inner.Paint(s, theme.Info) }

// Good reports a satisfied condition in prose.
func (p Palette) Good(s string) string { return p.inner.Paint(s, theme.Success) }

// Warn marks something needing attention.
func (p Palette) Warn(s string) string { return p.inner.Paint(s, theme.Warning) }

// Alarm reports a failure, and is what the `orc:` prefix on an error wears.
func (p Palette) Alarm(s string) string { return p.inner.Strong(s, theme.Danger) }
