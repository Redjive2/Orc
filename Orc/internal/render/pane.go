package render

import (
	"fmt"
	"strings"

	"orc/orc/internal/style"
	"orc/orc/internal/view"
	"orc/theme"
)

// The clean `attach` pane from Plan.md §6.2.
//
// It is a pure function of a model: the same bytes for the same input, no terminal,
// no session, no clock. That is what lets the whole screen be tested against a
// hand-written fixture, and it is why the interactive parts — reading keys, tailing
// the feed — live in the CLI and not here.
//
// Colour is a layer, as everywhere: every colour is redundant with a glyph or a word,
// and a test asserts the coloured screen stripped of escapes is byte-for-byte the
// plain one.

// Pane sizing. A pane narrower than the minimum cannot hold a path and a verdict on
// one line, and one shorter than the minimum has no room for a feed between its
// header and its compose box.
const (
	MinPaneHeight = 10
	PaneHeight    = 24
)

// Glyphs for the feed. Each sits beside a word.
const (
	glyphAction   = "●"
	glyphBlocked  = "▲"
	glyphWaiting  = "◆"
	glyphPrompt   = "›"
	glyphLifespan = "·"
	glyphSaid     = "“"
)

// Screen is everything the pane draws.
//
// It is one struct rather than a dozen arguments because the pane is drawn on every
// keystroke and every feed change: a signature that grew a field would otherwise
// touch every call site, and the call sites are in a loop that must stay readable.
type Screen struct {
	// Session is the folded event feed.
	Session view.Session
	// Facts are the things around the feed: who this is, what it costs, what it
	// has waiting for it.
	Facts view.Facts
	// Prose is what the transcript said, oldest first.
	Prose []view.Prose
	// ProseAvailable reports whether the transcript could be read at all, which
	// is a different state from "it said nothing" and is shown differently.
	ProseAvailable bool
	// Compose is the unsent buffer, one entry per line.
	Compose []string
	// ComposeFull reports that the buffer hit its limit.
	ComposeFull bool
	// Notice is a one-line message from the view itself: a warning, the result of
	// a send. It is drawn in the compose divider, where the eye already is.
	Notice string
	// Alarm marks the notice as a failure rather than a remark.
	Alarm bool
}

// DrawPane renders the whole screen.
//
// Height is honoured rather than assumed: an operator watching four agents has small
// panes, and a screen that drew 24 rows into a 12-row terminal would scroll its own
// header away.
func DrawPane(s Screen, p style.Palette, width, height int) (string, error) {
	width = Clamp(width)
	if height < MinPaneHeight {
		height = MinPaneHeight
	}

	inner := width - 2
	compose := s.Compose
	if len(compose) == 0 {
		compose = []string{""}
	}
	// The compose box takes what it needs up to a third of the pane: a long
	// message being written should not eat the feed it is being written about.
	if max := height / 3; len(compose) > max {
		compose = compose[len(compose)-max:]
	}

	var b strings.Builder
	if err := paneHeader(&b, s, p, inner); err != nil {
		return "", err
	}

	// header + compose divider + compose lines + footer.
	used := 2 + len(compose) + 1
	body := height - used
	if body < 1 {
		body = 1
	}
	if err := paneBody(&b, s, p, inner, body); err != nil {
		return "", err
	}
	if err := paneCompose(&b, s, p, inner, compose); err != nil {
		return "", err
	}
	if err := paneFooter(&b, s, p, inner); err != nil {
		return "", err
	}
	return b.String(), nil
}

// paneHeader draws the identity bar: who, what job, what it costs, which turn.
func paneHeader(b *strings.Builder, s Screen, p style.Palette, inner int) error {
	left := " " + s.Session.Identity.String()
	if s.Facts.Role != "" {
		left += " ─ " + s.Facts.Role
		if s.Facts.Authority > 0 {
			left += fmt.Sprintf("(%d)", s.Facts.Authority)
		}
	}
	if s.Facts.Model != "" {
		left += " ─ " + s.Facts.Model
		if s.Facts.Effort != "" {
			left += "/" + s.Facts.Effort
		}
	}
	if s.Facts.Load > 0 {
		left += fmt.Sprintf(" ─ load %d", s.Facts.Load)
	}

	right := ""
	if s.Session.Turn > 0 {
		right = fmt.Sprintf("turn %d ", s.Session.Turn)
	}

	return rule(b, p, leftBar{text: left, paint: p.Header}, right, inner+2, cardTopRight)
}

// paneBody draws the feed, newest at the bottom.
//
// The bottom is where the eye is — the next line will appear there — and it is where
// a terminal puts new output, so a feed that grew upward would be the one thing on
// the screen moving the wrong way.
func paneBody(b *strings.Builder, s Screen, p style.Palette, inner, height int) error {
	lines := feedLines(s, p, inner-2)

	// Prose is a band under the feed rather than interleaved with it. The
	// transcript carries no timestamps this reader trusts, and threading it into a
	// timestamped feed would mean inventing an order for it.
	if band := proseLines(s, p, inner-2); len(band) > 0 {
		lines = append(lines, band...)
	}

	if over := len(lines) - height; over > 0 {
		lines = lines[over:]
	}
	for _, line := range lines {
		if err := paneLine(b, p, inner, line.painted, line.plain); err != nil {
			return err
		}
	}
	// Pad downward so the compose box sits at the bottom of the pane rather than
	// floating up under a short feed.
	for i := len(lines); i < height; i++ {
		if err := paneLine(b, p, inner, "", ""); err != nil {
			return err
		}
	}
	return nil
}

// line is a painted string and the plain one it measures as.
type line struct{ painted, plain string }

// bar assembles a line by measuring as it goes.
//
// The first version of this file computed each column's offset by hand — width minus
// the timestamp minus the gap minus the glyph — and every one of those subtractions
// was a chance to be one out, which at 48 columns it was. Nothing here counts
// columns twice: text is added, the plain form is measured, and fit() makes the
// result exactly as wide as it must be.
type bar struct {
	painted strings.Builder
	plain   strings.Builder
}

// add appends text in a role.
func (b *bar) add(text string, paint func(string) string) {
	if text == "" {
		return
	}
	b.plain.WriteString(text)
	if paint == nil {
		b.painted.WriteString(text)
		return
	}
	b.painted.WriteString(paint(text))
}

// width is how many columns have been used.
func (b *bar) width() int { return theme.Width(b.plain.String()) }

// gap pads to a column, or one space if that column has passed. Something always
// separates two fields, or they read as one.
func (b *bar) gap(column int) {
	n := column - b.width()
	if n < 1 {
		n = 1
	}
	b.add(strings.Repeat(" ", n), nil)
}

// take appends text cut to whatever room is left before reserve columns.
func (b *bar) take(text string, paint func(string) string, width, reserve int) {
	room := width - b.width() - reserve
	if room < 1 {
		return
	}
	b.add(truncate(text, room), paint)
}

// fit returns the line at exactly width columns, padding or cutting as needed.
func (b *bar) fit(width int) line {
	plain, painted := b.plain.String(), b.painted.String()

	switch got := theme.Width(plain); {
	case got < width:
		pad := strings.Repeat(" ", width-got)
		return line{painted: painted + pad, plain: plain + pad}
	case got > width:
		// Reached only if a caller added something unmeasured. Cutting the plain
		// form and dropping the paint keeps the frame closed, which matters more
		// than the colour of a line that should not have been this long.
		return line{painted: truncate(plain, width), plain: truncate(plain, width)}
	default:
		return line{painted: painted, plain: plain}
	}
}

// feedLines renders the event rows.
func feedLines(s Screen, p style.Palette, width int) []line {
	if !s.Session.Live() {
		return []line{{
			painted: "  " + p.Muted("no events yet — the session has not called a tool"),
			plain:   "  no events yet — the session has not called a tool",
		}}
	}

	var out []line
	if s.Session.Dropped > 0 {
		text := fmt.Sprintf("  … %d earlier events", s.Session.Dropped)
		out = append(out, line{painted: p.Muted(text), plain: text})
	}

	for _, r := range s.Session.Rows {
		out = append(out, rowLine(r, p, width))
		// A refusal's reason gets its own line: "denied" without the reason sends
		// the reader to the permission table to learn what they already needed.
		if r.Blocked() && strings.TrimSpace(r.Reason) != "" {
			text := "              " + truncate(r.Reason, width-14)
			out = append(out, line{painted: p.Muted(text), plain: text})
		}
	}
	return out
}

// rowLine draws one event: when, what kind, which tool, on what.
//
// The columns are fixed so the feed can be scanned down rather than read across, and
// the verdict sits hard right where a column of refusals stands out.
func rowLine(r view.Row, p style.Palette, width int) line {
	glyph, paint := glyphLifespan, p.Muted
	switch {
	case r.Blocked():
		glyph, paint = glyphBlocked, p.Alarm
	case r.Kind == view.Action:
		glyph, paint = glyphAction, p.Good
	case r.Kind == view.Waiting:
		glyph, paint = glyphWaiting, p.Warn
	case r.Kind == view.Prompt:
		glyph, paint = glyphPrompt, p.Command
	}

	tool := strings.ToLower(r.Tool)
	if tool == "" {
		tool = kindWord(r.Kind)
	}

	verdict := ""
	if r.Blocked() {
		verdict = "✗ denied"
	}

	var b bar
	b.add("  ", nil)
	b.add(r.At.Format("15:04:05"), p.Muted)
	b.gap(timeColumn)
	b.add(glyph, paint)
	b.gap(glyphColumn)
	b.add(tool, p.Value)
	b.gap(toolColumn)
	// The detail gives way to the verdict rather than the other way round: which
	// file was refused is worth less than knowing one was.
	b.take(r.Detail, nil, width, theme.Width(verdict)+1)

	if verdict != "" {
		b.gap(width - theme.Width(verdict))
		b.add(verdict, p.Alarm)
	}
	return b.fit(width)
}

// The feed's columns, in the order they are written.
const (
	timeColumn  = 2 + 8 + 2
	glyphColumn = timeColumn + 1 + 2
	toolColumn  = glyphColumn + 8 + 1
)

func kindWord(k view.Kind) string {
	switch k {
	case view.Prompt:
		return "prompt"
	case view.Waiting:
		return "waiting"
	case view.Lifecycle:
		return "session"
	default:
		return ""
	}
}

// proseLines renders what the transcript said, or says why it did not.
func proseLines(s Screen, p style.Palette, width int) []line {
	if !s.ProseAvailable {
		if s.Session.Transcript == "" {
			return nil
		}
		// The transcript was named and could not be read. Plan.md §6.2's honest
		// limit, said out loud rather than left as an empty band.
		text := "  the transcript could not be read — `orc attach --direct` shows the session itself"
		return []line{{painted: p.Muted(truncate(text, width)), plain: truncate(text, width)}}
	}
	if len(s.Prose) == 0 {
		return nil
	}

	// Only the last few: the pane is a status view, and `--direct` is the reader.
	const keep = 3
	said := s.Prose
	if len(said) > keep {
		said = said[len(said)-keep:]
	}

	out := make([]line, 0, len(said))
	for _, said := range said {
		text := "  " + glyphSaid + " " + truncate(said.Text, width-4)
		paint := p.Muted
		if said.Who == view.Human {
			paint = p.Value
		}
		out = append(out, line{painted: paint(text), plain: text})
	}
	return out
}

// paneCompose draws the divider and the unsent buffer.
func paneCompose(b *strings.Builder, s Screen, p style.Palette, inner int, compose []string) error {
	label := " compose "
	if s.ComposeFull {
		label = " compose (full) "
	}
	if s.Notice != "" {
		label = " " + s.Notice + " "
	}

	paint := p.Header
	if s.Alarm {
		paint = p.Alarm
	}
	if err := rule(b, p, leftBar{text: label, paint: paint}, "", inner+2, cardTeeLeft); err != nil {
		return err
	}

	for i, text := range compose {
		marker := "  "
		if i == 0 {
			marker = "› "
		}
		// The cursor is drawn as an underscore on the last line, because a pane
		// redrawn from scratch cannot move a real terminal cursor into it.
		if i == len(compose)-1 {
			text += "_"
		}
		body := " " + marker + truncate(text, inner-4)
		if err := paneLine(b, p, inner, p.Value(body), body); err != nil {
			return err
		}
	}
	return nil
}

// paneFooter draws the keys and what is waiting.
func paneFooter(b *strings.Builder, s Screen, p style.Palette, inner int) error {
	// Leaving comes first, and as the one key rather than the two. It is the thing
	// somebody needs when they are stuck, and a list that opened with "send" put the
	// way out third — behind a sequence that is hard to type on half of the world's
	// keyboards.
	keys := " ^Q leave · ^S send · ^] terminal · ^R refresh "

	right := ""
	if s.Facts.Mail >= 0 {
		right += fmt.Sprintf("mail %d ", s.Facts.Mail)
	}
	if s.Facts.Task != "" {
		if right != "" {
			right += "· "
		}
		right += "task " + s.Facts.Task + " "
	}

	return rule(b, p, leftBar{text: keys, paint: p.Muted}, right, inner+2, cardBottomRight)
}

// rule draws a bordered line: text on the left, optional text on the right, and
// horizontal fill between.
//
// Everything is measured from the plain form — escape sequences occupy no columns, so
// measuring the painted string would make a coloured bar a different length from an
// uncoloured one, which is the bug that leaves a frame that does not close. When
// there is no room for both texts the right one goes first: "turn 3" is worth less
// than the name of the agent it belongs to.
func rule(b *strings.Builder, p style.Palette, left leftBar, right string, width int, closer string) error {
	if right != "" {
		right = " " + strings.TrimSpace(right) + " "
	}

	var out bar
	out.add(cardTopFor(closer), p.Frame)
	out.add(horizontal, p.Frame)
	// The left text keeps at least the corner, a space, and the closer.
	out.take(" "+strings.TrimSpace(left.text)+" ", left.paint, width, 2)

	if theme.Width(right)+out.width()+2 > width {
		right = ""
	}
	fill := width - out.width() - theme.Width(right) - 2
	if fill < 0 {
		fill = 0
	}
	out.add(strings.Repeat(horizontal, fill), p.Frame)
	out.add(right, p.Muted)
	out.add(horizontal, p.Frame)
	out.add(closer, p.Frame)

	got := out.fit(width)
	b.WriteString(got.painted)
	b.WriteString("\n")
	return nil
}

// leftBar is a bar's left text and how it is painted.
type leftBar struct {
	text  string
	paint func(string) string
}

// cardTopFor picks the opening corner that goes with a closer, so no caller passes a
// mismatched pair.
func cardTopFor(closer string) string {
	switch closer {
	case cardTopRight:
		return cardTopLeft
	case cardBottomRight:
		return cardBottomLeft
	default:
		return cardTeeRight
	}
}

// paneLine draws one bordered row, padded to the inner width.
func paneLine(b *strings.Builder, p style.Palette, inner int, painted, plain string) error {
	var body bar
	body.add(plain, nil)
	if painted != plain {
		// The two forms are tracked separately all the way here, so the fit is
		// done on a bar carrying both rather than by padding one of them.
		body = bar{}
		body.plain.WriteString(plain)
		body.painted.WriteString(painted)
	}

	got := body.fit(inner)
	b.WriteString(p.Frame(vertical))
	b.WriteString(got.painted)
	b.WriteString(p.Frame(vertical))
	b.WriteString("\n")
	return nil
}

// truncate cuts text to a width, marking that it was cut.
func truncate(s string, width int) string {
	got, err := theme.Truncate(s, width)
	if err != nil {
		return s
	}
	return got
}

// pad extends text to a width.
func pad(s string, width int) string {
	got, err := theme.Pad(s, width, 'l')
	if err != nil {
		return s
	}
	return got
}
