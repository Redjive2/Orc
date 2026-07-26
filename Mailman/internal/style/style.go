// Package style is the only place in Mailman that knows an escape sequence
// exists.
//
// Two jobs. It decides whether colour is wanted at all, and it measures text
// the way a terminal will lay it out — which is not the same as counting bytes,
// or even runes, once a subject line contains CJK or an emoji. Every table in
// render aligns on Width, so a wrong answer here shears a column; that is why
// measurement lives with the terminal knowledge rather than with the drawing
// code.
//
// The house rule for colour: it is a layer, never information. Every colour in
// the palette is redundant with a glyph or a word, so piping through grep or
// setting NO_COLOR loses nothing but the pleasure.
//
// The colours themselves come from orc/theme, which every Orc tool shares.
// Nothing here decides what a heading looks like; it only decides what is a
// heading.
package style

import (
	"orc/theme"
)

// Palette renders text in a role. It is a thin adapter over the shared scheme
// in orc/theme, so Mailman looks like every other Orc tool and one setting
// restyles all of them at once. The zero Palette is plain, which is what makes
// "colour off" the trivially safe path and lets every golden test compare exact
// bytes.
type Palette struct {
	inner theme.Palette
}

// New wraps a resolved scheme.
func New(p theme.Palette) Palette { return Palette{inner: p} }

// Plain returns a palette that never colours anything. Golden tests use it, so
// the layout they pin is readable in a diff.
func Plain() Palette { return Palette{} }

// Coloured returns a palette in the default flavour. It exists so a test can
// assert the escape sequences appear without owning a terminal.
func Coloured() Palette {
	return Palette{inner: theme.New(theme.Default, theme.TrueColour)}
}

// Enabled reports whether this palette emits escape sequences.
func (p Palette) Enabled() bool { return p.inner.Enabled() }

// Scheme returns the underlying palette, for a tool that needs to name the
// flavour in force.
func (p Palette) Scheme() theme.Palette { return p.inner }

// The roles Mailman draws with. Each maps to a role in the shared scheme rather
// than to a colour, so what "unread" looks like is decided in one place for
// every tool rather than here.
//
// Every one of these is redundant with a glyph or a word at the call site — a
// pipe through grep loses the colour and nothing else.

// Title styles a table's heading.
func (p Palette) Title(s string) string { return p.inner.Paint(s, theme.Title) }

// Header styles a column label.
func (p Palette) Header(s string) string { return p.inner.Paint(s, theme.Heading) }

// Frame styles the box-drawing characters, which should recede.
func (p Palette) Frame(s string) string { return p.inner.Paint(s, theme.Frame) }

// Muted styles secondary text: read mail, absent values, counts.
func (p Palette) Muted(s string) string { return p.inner.Paint(s, theme.Muted) }

// Unread styles an unread row, which should be the first thing the eye lands on.
func (p Palette) Unread(s string) string { return p.inner.Strong(s, theme.Warning) }

// User styles a user name.
func (p Palette) User(s string) string { return p.inner.Paint(s, theme.Success) }

// Subject styles a subject line.
func (p Palette) Subject(s string) string { return p.inner.Paint(s, theme.Primary) }

// Convo styles a conversation reference.
func (p Palette) Convo(s string) string { return p.inner.Paint(s, theme.Secondary) }

// ID styles an identifier.
func (p Palette) ID(s string) string { return p.inner.Paint(s, theme.Info) }

// Good styles a satisfied condition — mail that has been read, a healthy store.
func (p Palette) Good(s string) string { return p.inner.Paint(s, theme.Success) }

// Bad styles a problem.
func (p Palette) Bad(s string) string { return p.inner.Paint(s, theme.Danger) }

// Note styles an aside.
func (p Palette) Note(s string) string { return p.inner.Paint(s, theme.Subtle) }

// Measurement forwards to the shared scheme.
//
// Width, Sanitise, Truncate, and Pad describe how a terminal lays text out,
// which is the same knowledge orc/theme already holds and which Macmuffin needs
// identically. They are forwarded rather than re-implemented so there is one
// answer, and kept as names here so Mailman's drawing code reads as before.

// Ellipsis marks a truncated cell.
const Ellipsis = theme.Ellipsis

// Width returns how many terminal cells s occupies.
func Width(s string) int { return theme.Width(s) }

// Sanitise makes text safe to place in a table cell.
func Sanitise(s string) string { return theme.Sanitise(s) }

// Truncate shortens s to at most max cells, marking the cut with an ellipsis.
func Truncate(s string, max int) (string, error) { return theme.Truncate(s, max) }

// Pad extends s to exactly width cells.
func Pad(s string, width int, align byte) (string, error) { return theme.Pad(s, width, align) }

// The inks below are for prose — help text, confirmations, diagnostics — rather
// than for a mailbox. They exist because a tool whose tables are coloured and
// whose sentences are not looks half-finished, and because `mailman help` is the
// screen a new agent reads first.
//
// They are the same roles Macmuffin, Orc, and cq paint their prose with, so no
// two tools in this tree can disagree about what a command name looks like.

// Tool styles mailman's own name, where it introduces itself.
func (p Palette) Tool(s string) string { return p.inner.Strong(s, theme.Title) }

// Command styles a command name, in help and in "try this instead" advice.
func (p Palette) Command(s string) string { return p.inner.Strong(s, theme.Primary) }

// Flag styles a flag name.
func (p Palette) Flag(s string) string { return p.inner.Paint(s, theme.Tertiary) }

// Value styles a placeholder or a literal a caller would type.
func (p Palette) Value(s string) string { return p.inner.Paint(s, theme.Secondary) }

// Setting styles an environment variable.
func (p Palette) Setting(s string) string { return p.inner.Paint(s, theme.Info) }

// Warn marks something needing attention.
func (p Palette) Warn(s string) string { return p.inner.Paint(s, theme.Warning) }

// Alarm reports a failure, and is what the `mailman:` prefix on an error wears.
func (p Palette) Alarm(s string) string { return p.inner.Strong(s, theme.Danger) }
