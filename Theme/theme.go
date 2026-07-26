// Package theme is the colour scheme Orc's tools share.
//
// It exists so that `anno`, `mailman`, and everything alongside them look like
// one program rather than several. A tool asks for a *role* — a title, a frame,
// something muted — and the flavour in force decides the colour. No tool spells
// an escape sequence itself, and no tool has its own opinion about what green
// means.
//
// The scheme is Catppuccin: four flavours, one of which (Macchiato) is the
// default. Each flavour is the published palette, unmodified, so output sits
// correctly beside any other Catppuccin-themed terminal.
//
// Colour is applied only when writing, never while measuring. Escape sequences
// occupy no columns, so a table aligned in plain text stays aligned in colour.
package theme

import (
	"fmt"
	"strings"
)

// Colour is a 24-bit RGB value.
type Colour struct {
	R, G, B uint8
}

// Hex renders the colour as it appears in the Catppuccin palette.
func (c Colour) Hex() string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// name indexes one of Catppuccin's twenty-six named colours. The order is the
// palette's own, so a flavour table reads against the published reference.
type name int

const (
	rosewater name = iota
	flamingo
	pink
	mauve
	red
	maroon
	peach
	yellow
	green
	teal
	sky
	sapphire
	blue
	lavender
	text
	subtext1
	subtext0
	overlay2
	overlay1
	overlay0
	surface2
	surface1
	surface0
	base
	mantle
	crust
	nameCount
)

// Flavour is one of Catppuccin's four palettes, or none at all.
type Flavour int

const (
	// Plain emits no colour. It is the zero value, so a palette nobody
	// configured is a palette that cannot corrupt output.
	Plain Flavour = iota
	// Latte is the light flavour.
	Latte
	// Frappe is the softest dark flavour.
	Frappe
	// Macchiato is Orc's default.
	Macchiato
	// Mocha is the darkest flavour.
	Mocha
)

// Default is the flavour a tool uses when nothing says otherwise.
const Default = Macchiato

// String implements fmt.Stringer, spelling the flavour as it is configured.
func (f Flavour) String() string {
	switch f {
	case Latte:
		return "latte"
	case Frappe:
		return "frappe"
	case Macchiato:
		return "macchiato"
	case Mocha:
		return "mocha"
	case Plain:
		return "none"
	default:
		return fmt.Sprintf("Flavour(%d)", int(f))
	}
}

// Valid reports whether f is a defined flavour.
func (f Flavour) Valid() bool { return f >= Plain && f <= Mocha }

// Flavours lists the configurable names, in the order help text should show
// them.
func Flavours() []string { return []string{"macchiato", "mocha", "frappe", "latte", "none"} }

// ParseFlavour reads a configured flavour name.
//
// "none", "off", and "plain" all disable colour, because an operator reaching
// for this setting means the same thing by all three and should not have to
// discover which word was chosen.
func ParseFlavour(s string) (Flavour, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "macchiato":
		return Macchiato, nil
	case "mocha":
		return Mocha, nil
	case "frappe", "frappé":
		return Frappe, nil
	case "latte":
		return Latte, nil
	case "none", "off", "plain", "no", "false":
		return Plain, nil
	default:
		return Plain, fmt.Errorf("unknown theme %q; want one of %s", s, strings.Join(Flavours(), ", "))
	}
}

// palettes holds each flavour's twenty-six colours, transcribed from the
// published Catppuccin palette. Nothing here is adjusted or approximated: a
// tool's output has to sit correctly beside every other Catppuccin-themed
// window on the same screen.
var palettes = [...][nameCount]Colour{
	Plain: {},
	Latte: {
		rosewater: {0xdc, 0x8a, 0x78}, flamingo: {0xdd, 0x78, 0x78},
		pink: {0xea, 0x76, 0xcb}, mauve: {0x88, 0x39, 0xef},
		red: {0xd2, 0x0f, 0x39}, maroon: {0xe6, 0x45, 0x53},
		peach: {0xfe, 0x64, 0x0b}, yellow: {0xdf, 0x8e, 0x1d},
		green: {0x40, 0xa0, 0x2b}, teal: {0x17, 0x92, 0x99},
		sky: {0x04, 0xa5, 0xe5}, sapphire: {0x20, 0x9f, 0xb5},
		blue: {0x1e, 0x66, 0xf5}, lavender: {0x72, 0x87, 0xfd},
		text: {0x4c, 0x4f, 0x69}, subtext1: {0x5c, 0x5f, 0x77},
		subtext0: {0x6c, 0x6f, 0x85}, overlay2: {0x7c, 0x7f, 0x93},
		overlay1: {0x8c, 0x8f, 0xa1}, overlay0: {0x9c, 0xa0, 0xb0},
		surface2: {0xac, 0xb0, 0xbe}, surface1: {0xbc, 0xc0, 0xcc},
		surface0: {0xcc, 0xd0, 0xda}, base: {0xef, 0xf1, 0xf5},
		mantle: {0xe6, 0xe9, 0xef}, crust: {0xdc, 0xe0, 0xe8},
	},
	Frappe: {
		rosewater: {0xf2, 0xd5, 0xcf}, flamingo: {0xee, 0xbe, 0xbe},
		pink: {0xf4, 0xb8, 0xe4}, mauve: {0xca, 0x9e, 0xe6},
		red: {0xe7, 0x82, 0x84}, maroon: {0xea, 0x99, 0x9c},
		peach: {0xef, 0x9f, 0x76}, yellow: {0xe5, 0xc8, 0x90},
		green: {0xa6, 0xd1, 0x89}, teal: {0x81, 0xc8, 0xbe},
		sky: {0x99, 0xd1, 0xdb}, sapphire: {0x85, 0xc1, 0xdc},
		blue: {0x8c, 0xaa, 0xee}, lavender: {0xba, 0xbb, 0xf1},
		text: {0xc6, 0xd0, 0xf5}, subtext1: {0xb5, 0xbf, 0xe2},
		subtext0: {0xa5, 0xad, 0xce}, overlay2: {0x94, 0x9c, 0xbb},
		overlay1: {0x83, 0x8b, 0xa7}, overlay0: {0x73, 0x79, 0x94},
		surface2: {0x62, 0x68, 0x80}, surface1: {0x51, 0x57, 0x6d},
		surface0: {0x41, 0x45, 0x59}, base: {0x30, 0x34, 0x46},
		mantle: {0x29, 0x2c, 0x3c}, crust: {0x23, 0x26, 0x34},
	},
	Macchiato: {
		rosewater: {0xf4, 0xdb, 0xd6}, flamingo: {0xf0, 0xc6, 0xc6},
		pink: {0xf5, 0xbd, 0xe6}, mauve: {0xc6, 0xa0, 0xf6},
		red: {0xed, 0x87, 0x96}, maroon: {0xee, 0x99, 0xa0},
		peach: {0xf5, 0xa9, 0x7f}, yellow: {0xee, 0xd4, 0x9f},
		green: {0xa6, 0xda, 0x95}, teal: {0x8b, 0xd5, 0xca},
		sky: {0x91, 0xd7, 0xe3}, sapphire: {0x7d, 0xc4, 0xe4},
		blue: {0x8a, 0xad, 0xf4}, lavender: {0xb7, 0xbd, 0xf8},
		text: {0xca, 0xd3, 0xf5}, subtext1: {0xb8, 0xc0, 0xe0},
		subtext0: {0xa5, 0xad, 0xcb}, overlay2: {0x93, 0x9a, 0xb7},
		overlay1: {0x80, 0x87, 0xa2}, overlay0: {0x6e, 0x73, 0x8d},
		surface2: {0x5b, 0x60, 0x78}, surface1: {0x49, 0x4d, 0x64},
		surface0: {0x36, 0x3a, 0x4f}, base: {0x24, 0x27, 0x3a},
		mantle: {0x1e, 0x20, 0x30}, crust: {0x18, 0x19, 0x26},
	},
	Mocha: {
		rosewater: {0xf5, 0xe0, 0xdc}, flamingo: {0xf2, 0xcd, 0xcd},
		pink: {0xf5, 0xc2, 0xe7}, mauve: {0xcb, 0xa6, 0xf7},
		red: {0xf3, 0x8b, 0xa8}, maroon: {0xeb, 0xa0, 0xac},
		peach: {0xfa, 0xb3, 0x87}, yellow: {0xf9, 0xe2, 0xaf},
		green: {0xa6, 0xe3, 0xa1}, teal: {0x94, 0xe2, 0xd5},
		sky: {0x89, 0xdc, 0xeb}, sapphire: {0x74, 0xc7, 0xec},
		blue: {0x89, 0xb4, 0xfa}, lavender: {0xb4, 0xbe, 0xfe},
		text: {0xcd, 0xd6, 0xf4}, subtext1: {0xba, 0xc2, 0xde},
		subtext0: {0xa6, 0xad, 0xc8}, overlay2: {0x93, 0x99, 0xb2},
		overlay1: {0x7f, 0x84, 0x9c}, overlay0: {0x6c, 0x70, 0x86},
		surface2: {0x58, 0x5b, 0x70}, surface1: {0x45, 0x47, 0x5a},
		surface0: {0x31, 0x32, 0x44}, base: {0x1e, 0x1e, 0x2e},
		mantle: {0x18, 0x18, 0x25}, crust: {0x11, 0x11, 0x1b},
	},
}

// Role is what a piece of text *is*, rather than what colour it should be.
//
// Tools name roles; the flavour decides the colour. That indirection is the
// whole point: it is what lets one setting restyle every tool at once, and what
// stops two tools disagreeing about what a heading looks like.
type Role int

const (
	// Text is ordinary content.
	Text Role = iota
	// Heading is a column label or a field name: content, emphasised.
	Heading
	// Title names the whole thing being shown.
	Title
	// Muted is secondary content — counts, timestamps, things already read.
	Muted
	// Subtle is quieter still: an aside the eye should skip.
	Subtle
	// Frame is box drawing and rules, which should recede behind the content.
	Frame
	// Primary is the main accent, for the thing a reader is looking for.
	Primary
	// Secondary is a second category, distinct from Primary at a glance.
	Secondary
	// Tertiary is a third.
	Tertiary
	// Accent highlights something unusual without implying it is wrong.
	Accent
	// Info marks identifiers and references.
	Info
	// Success marks a satisfied condition.
	Success
	// Warning marks something needing attention — unread mail, a caution.
	Warning
	// Danger marks a failure.
	Danger
	roleCount
)

// String implements fmt.Stringer, for tests and diagnostics.
func (r Role) String() string {
	if !r.Valid() {
		return fmt.Sprintf("Role(%d)", int(r))
	}
	return roles[r].label
}

// Valid reports whether r is a defined role.
func (r Role) Valid() bool { return r >= Text && r < roleCount }

// face is how one role is drawn: a named colour plus text attributes.
type face struct {
	label  string
	colour name
	bold   bool
	dim    bool
	italic bool
}

// roles maps every role to a named colour. This table *is* the scheme: it is
// the one place that decides what a heading or a warning looks like across
// every Orc tool.
var roles = [roleCount]face{
	Text:      {label: "text", colour: text},
	Heading:   {label: "heading", colour: text, bold: true},
	Title:     {label: "title", colour: mauve, bold: true},
	Muted:     {label: "muted", colour: overlay1},
	Subtle:    {label: "subtle", colour: overlay0, italic: true},
	Frame:     {label: "frame", colour: surface2},
	Primary:   {label: "primary", colour: blue},
	Secondary: {label: "secondary", colour: mauve},
	Tertiary:  {label: "tertiary", colour: teal},
	Accent:    {label: "accent", colour: peach},
	Info:      {label: "info", colour: sapphire},
	Success:   {label: "success", colour: green},
	Warning:   {label: "warning", colour: yellow},
	Danger:    {label: "danger", colour: red},
}

// Depth is how much colour a terminal can show.
type Depth int

const (
	// NoColour emits no escape sequences at all.
	NoColour Depth = iota
	// Ansi256 uses the xterm 256-colour cube, which every colour terminal has.
	Ansi256
	// TrueColour uses 24-bit sequences, which render the palette exactly.
	TrueColour
)

// String implements fmt.Stringer.
func (d Depth) String() string {
	switch d {
	case NoColour:
		return "none"
	case Ansi256:
		return "256"
	case TrueColour:
		return "truecolor"
	default:
		return fmt.Sprintf("Depth(%d)", int(d))
	}
}

// Palette draws text in a flavour. The zero value is plain, so a palette that
// nobody configured is one that cannot corrupt output.
type Palette struct {
	flavour Flavour
	depth   Depth
}

// New builds a palette. A flavour of Plain or a depth of NoColour both give a
// palette that emits nothing, whichever way the caller arrived at it.
func New(f Flavour, d Depth) Palette {
	if !f.Valid() || f == Plain || d == NoColour {
		return Palette{}
	}
	return Palette{flavour: f, depth: d}
}

// Enabled reports whether the palette emits escape sequences.
func (p Palette) Enabled() bool { return p.flavour != Plain && p.depth != NoColour }

// Flavour returns the flavour in force.
func (p Palette) Flavour() Flavour { return p.flavour }

// Depth returns the colour depth in use.
func (p Palette) Depth() Depth { return p.depth }

// Colour returns the colour a role resolves to under this palette, and whether
// the palette would actually draw it.
func (p Palette) Colour(r Role) (Colour, bool) {
	if !p.Enabled() || !r.Valid() {
		return Colour{}, false
	}
	return palettes[p.flavour][roles[r].colour], true
}

// Paint draws text in a role.
//
// A disabled palette, an undefined role, and empty text all return the text
// untouched, so Paint is always safe to call and never lengthens output that
// would not benefit from it.
func (p Palette) Paint(text string, r Role) string {
	return p.paint(text, r, false)
}

// Strong draws text in a role, emphasised. It is for the cases where a tool
// wants the scheme's colour but more weight than the role carries by default.
func (p Palette) Strong(text string, r Role) string {
	return p.paint(text, r, true)
}

func (p Palette) paint(s string, r Role, strong bool) string {
	if !p.Enabled() || s == "" || !r.Valid() {
		return s
	}
	f := roles[r]

	var attrs []string
	if f.bold || strong {
		attrs = append(attrs, "1")
	}
	if f.dim && !strong {
		attrs = append(attrs, "2")
	}
	if f.italic {
		attrs = append(attrs, "3")
	}
	attrs = append(attrs, p.sgr(palettes[p.flavour][f.colour]))

	return "\x1b[" + strings.Join(attrs, ";") + "m" + s + "\x1b[0m"
}

// sgr renders a colour as the foreground parameters for the palette's depth.
func (p Palette) sgr(c Colour) string {
	if p.depth == TrueColour {
		return fmt.Sprintf("38;2;%d;%d;%d", c.R, c.G, c.B)
	}
	return fmt.Sprintf("38;5;%d", index256(c))
}

// index256 maps a colour to the nearest xterm 256-colour index.
//
// The cube is 6×6×6 with unevenly spaced levels, plus a 24-step grey ramp. Near
// -grey colours land better on the ramp than in the cube, so both candidates are
// measured and the closer wins — without that, Catppuccin's greys (which most
// of the frame is drawn in) collapse onto a handful of muddy cube entries.
func index256(c Colour) int {
	cubeIdx := 16 +
		36*nearestLevel(c.R) +
		6*nearestLevel(c.G) +
		nearestLevel(c.B)
	cube := Colour{levels[nearestLevel(c.R)], levels[nearestLevel(c.G)], levels[nearestLevel(c.B)]}

	// The grey ramp runs 8, 18, … 238 at indices 232..255.
	avg := (int(c.R) + int(c.G) + int(c.B)) / 3
	step := (avg - 8 + 5) / 10
	step = clamp(step, 0, 23)
	greyValue := uint8(8 + 10*step)
	grey := Colour{greyValue, greyValue, greyValue}

	if distance(c, grey) < distance(c, cube) {
		return 232 + step
	}
	return cubeIdx
}

// levels are the six values the xterm colour cube uses per channel.
var levels = [6]uint8{0, 95, 135, 175, 215, 255}

func nearestLevel(v uint8) int {
	best, bestDist := 0, 1<<31-1
	for i, level := range levels {
		d := int(v) - int(level)
		if d < 0 {
			d = -d
		}
		if d < bestDist {
			best, bestDist = i, d
		}
	}
	return best
}

// distance is squared Euclidean distance in RGB. It is not perceptually
// uniform, which for choosing between two already-close candidates is not worth
// the arithmetic.
func distance(a, b Colour) int {
	dr := int(a.R) - int(b.R)
	dg := int(a.G) - int(b.G)
	db := int(a.B) - int(b.B)
	return dr*dr + dg*dg + db*db
}

func clamp(v, lo, hi int) int {
	switch {
	case v < lo:
		return lo
	case v > hi:
		return hi
	default:
		return v
	}
}
