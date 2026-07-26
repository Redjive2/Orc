// Package style maps Macmuffin's vocabulary onto the shared colour scheme.
//
// Nothing here decides what a colour looks like — orc/theme does — and nothing
// outside here names a role. The drawing code says what a thing *is*: an owner,
// a draft, a broken task. That indirection is what lets one setting restyle
// every Orc tool at once.
//
// The house rule: colour is a layer, never information. Every role below is
// redundant with a glyph or a word at the call site, so a pipe through grep and
// a NO_COLOR terminal lose nothing but the pleasure.
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

// Title styles the name of the thing being shown.
func (p Palette) Title(s string) string { return p.inner.Paint(s, theme.Title) }

// Header styles a column label.
func (p Palette) Header(s string) string { return p.inner.Paint(s, theme.Heading) }

// Frame styles box drawing, which should recede behind the content.
func (p Palette) Frame(s string) string { return p.inner.Paint(s, theme.Frame) }

// Muted styles secondary text: counts, timestamps, a completed task.
func (p Palette) Muted(s string) string { return p.inner.Paint(s, theme.Muted) }

// Task styles a task name.
func (p Palette) Task(s string) string { return p.inner.Paint(s, theme.Primary) }

// Agent styles an agent name.
func (p Palette) Agent(s string) string { return p.inner.Paint(s, theme.Success) }

// Draft styles the mark on an unpublished task.
func (p Palette) Draft(s string) string { return p.inner.Paint(s, theme.Subtle) }

// Score styles a priority or difficulty tag.
func (p Palette) Score(s string) string { return p.inner.Paint(s, theme.Info) }

// Path styles a scope entry.
func (p Palette) Path(s string) string { return p.inner.Paint(s, theme.Secondary) }

// Broken styles a task that is not working.
func (p Palette) Broken(s string) string { return p.inner.Strong(s, theme.Danger) }

// Slow styles a task that is struggling.
func (p Palette) Slow(s string) string { return p.inner.Paint(s, theme.Warning) }

// Nominal styles a task that is going fine.
func (p Palette) Nominal(s string) string { return p.inner.Paint(s, theme.Primary) }

// Done styles a satisfied condition.
func (p Palette) Done(s string) string { return p.inner.Paint(s, theme.Success) }

// The inks below are for prose — help text, confirmations, notes — rather than
// for the board. They exist because a tool whose tables are coloured and whose
// sentences are not looks half-finished, and because `muff create` saying which
// task it made should make the name findable at a glance.
//
// They are the same roles cq paints its terminal output with, so the two tools
// cannot disagree about what a command name or a flag looks like.

// Tool styles muff's own name, where it introduces itself.
func (p Palette) Tool(s string) string { return p.inner.Strong(s, theme.Title) }

// Command styles a command name, in help and in "try this instead" advice.
func (p Palette) Command(s string) string { return p.inner.Strong(s, theme.Primary) }

// Flag styles a flag name.
func (p Palette) Flag(s string) string { return p.inner.Paint(s, theme.Tertiary) }

// Value styles a placeholder or a literal a caller would type.
func (p Palette) Value(s string) string { return p.inner.Paint(s, theme.Secondary) }

// Setting styles an environment variable.
func (p Palette) Setting(s string) string { return p.inner.Paint(s, theme.Info) }

// Good reports a satisfied condition in prose. It is Done under another name:
// a completed task and a successful command are the same green.
func (p Palette) Good(s string) string { return p.Done(s) }

// Warn marks something needing attention.
func (p Palette) Warn(s string) string { return p.inner.Paint(s, theme.Warning) }

// Alarm reports a failure, and is what the `muff:` prefix on an error wears.
func (p Palette) Alarm(s string) string { return p.inner.Strong(s, theme.Danger) }
