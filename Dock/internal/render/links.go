package render

import (
	"fmt"
	"strings"

	"orc/dock/internal/link"
	"orc/dock/internal/style"
)

// Links draws one section's edges as a diagram.
//
// A link list is a graph, and a graph drawn as a list is harder to read than one
// drawn as a graph: the gutters make it obvious at a glance which way each edge
// points and where the outbound set ends. Outbound comes first because it is
// what the section says; inbound is what others say about it.
func Links(base string, at link.Node, title string, out, in []link.Arrow, pal style.Palette) string {
	var b strings.Builder

	head := at.Rel(base).String()
	if title != "" {
		head += "  " + title
	}
	b.WriteString(pal.Paint(head, style.Number))
	b.WriteByte('\n')

	if len(out) == 0 && len(in) == 0 {
		b.WriteString(pal.Paint("  (no links)", style.Quiet))
		b.WriteByte('\n')
		return b.String()
	}

	// Measure the endpoint column so the labels line up in one scannable stripe.
	width := 0
	for _, a := range out {
		width = max(width, style.Width(endpoint(base, a, false)))
	}
	for _, a := range in {
		width = max(width, style.Width(endpoint(base, a, true)))
	}
	width = min(width, 52)

	b.WriteString(pal.Paint("  │", style.Frame))
	b.WriteByte('\n')

	rows := len(out) + len(in)
	drawn := 0
	for _, a := range out {
		drawn++
		b.WriteString(row(base, a, "→", drawn == rows, width, pal))
	}
	if len(out) > 0 && len(in) > 0 {
		b.WriteString(pal.Paint("  │", style.Frame))
		b.WriteByte('\n')
	}
	for _, a := range in {
		drawn++
		b.WriteString(row(base, a, "←", drawn == rows, width, pal))
	}
	return b.String()
}

// row draws one edge of the diagram.
func row(base string, a link.Arrow, direction string, last bool, width int, pal style.Palette) string {
	gutter := "  ├─"
	if last {
		gutter = "  └─"
	}

	inbound := direction == "←"
	ink := style.Out
	if inbound {
		ink = style.In
	}

	var b strings.Builder
	b.WriteString(pal.Paint(gutter, style.Frame))
	b.WriteString(pal.Paint(direction, ink))
	b.WriteByte(' ')

	tgt := endpoint(base, a, inbound)
	switch a.State {
	case link.Dangling:
		b.WriteString(pal.Paint("✗ "+style.Pad(tgt, width), style.Alarm))
	case link.Unchecked:
		b.WriteString(pal.Paint("? "+style.Pad(tgt, width), style.Foreign))
	default:
		b.WriteString("  " + pal.Paint(style.Pad(tgt, width), style.Number))
	}

	if label := a.Edge.Label(); label != "" {
		b.WriteString("  ")
		b.WriteString(pal.Paint(style.Truncate(label, 40), style.Label))
	}
	if a.State != link.Resolved && a.Why != "" {
		b.WriteString(pal.Paint("  ("+a.Why+")", style.Quiet))
	}
	b.WriteByte('\n')
	return b.String()
}

// endpoint is the end of an arrow that is *not* the section being asked about.
//
// For an outbound arrow that is what it points at; for an inbound one it is
// where it came from. Rendering the target in both directions would print the
// section's own name back at it once per backlink, which says nothing — the
// question a backlink answers is "who cites this".
func endpoint(base string, a link.Arrow, inbound bool) string {
	if inbound {
		return a.From.Rel(base).String()
	}
	if a.State == link.Resolved {
		return a.To.Rel(base).String()
	}
	// A broken link still says what it was trying to reach.
	return a.Edge.To().String()
}

// Check renders a corpus report: every link that does not resolve, with its
// position, and a summary line.
//
// Every reported line carries file:line:col, so an agent fixing a corpus can
// jump straight to each one, and the summary states what was *not* verified
// alongside what was — a report that hid the unchecked links would overstate
// its own thoroughness.
func Check(base string, g link.Graph, faults []error, pal style.Palette) string {
	var b strings.Builder

	dangling := g.Dangling()
	for _, a := range dangling {
		b.WriteString(pal.Paint("✗ ", style.Alarm))
		b.WriteString(pal.Paint(position(base, a), style.Span))
		b.WriteByte(' ')
		b.WriteString(pal.Paint(a.Edge.To().String(), style.Number))
		if a.Why != "" {
			b.WriteString(pal.Paint("  "+a.Why, style.Quiet))
		}
		if label := a.Edge.Label(); label != "" {
			b.WriteString(pal.Paint("  ["+style.Truncate(label, 40)+"]", style.Label))
		}
		b.WriteByte('\n')
	}

	for _, err := range faults {
		b.WriteString(pal.Paint("✗ ", style.Alarm))
		b.WriteString(pal.Paint(err.Error(), style.Quiet))
		b.WriteByte('\n')
	}

	unchecked := 0
	for _, a := range g.Faults() {
		if a.State == link.Unchecked {
			unchecked++
		}
	}

	b.WriteString(pal.Paint(summary(len(g.Documents()), len(g.Arrows()), len(dangling), len(faults), unchecked), style.Quiet))
	b.WriteByte('\n')
	return b.String()
}

func position(base string, a link.Arrow) string {
	return fmt.Sprintf("%s:%d:%d", a.From.Rel(base).Path, a.Edge.Line(), a.Edge.Col())
}

func summary(docs, links, dangling, faults, unchecked int) string {
	parts := []string{
		fmt.Sprintf("%s checked", plural2(docs, "document", "documents")),
		fmt.Sprintf("%s", plural2(links, "link", "links")),
	}
	if dangling > 0 {
		parts = append(parts, fmt.Sprintf("%d dangling", dangling))
	}
	if faults > 0 {
		parts = append(parts, fmt.Sprintf("%s", plural2(faults, "unreadable document", "unreadable documents")))
	}
	if unchecked > 0 {
		parts = append(parts, fmt.Sprintf("%d left to anno", unchecked))
	}
	if dangling == 0 && faults == 0 {
		parts = append(parts, "nothing broken")
	}
	return strings.Join(parts, ", ")
}

func plural2(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// FollowHeader names a section that --follow pulled in.
//
// The header is one line and is itself a target, so a reader who wants the
// section on its own can paste it straight back into read. The section that was
// asked for gets no header: prefixing it would spend a line telling the caller
// the name they just typed.
func FollowHeader(base string, n link.Node, name string, pal style.Palette) string {
	head := n.Rel(base).String()
	if name != "" {
		head += "   " + name
	}
	return "\n" + pal.Paint(head, style.Number) + "\n"
}

// FollowCodeHeader names an annotation anno supplied. It is tagged so a reader
// can tell prose from code without looking at the content.
func FollowCodeHeader(target string, pal style.Palette) string {
	return "\n" + pal.Paint(target, style.Foreign) + pal.Paint("   (anno)", style.Quiet) + "\n"
}

// FollowNote reports something --follow did *not* emit: a section already shown,
// or one the budget stopped.
//
// It is a line rather than silence because a reader who cannot tell "there was
// nothing more" from "there was more and you did not get it" has been misled by
// the tool. The marker is a dash and the text says which, so the note carries
// its meaning without colour.
func FollowNote(text string, pal style.Palette) string {
	return pal.Paint("\n— "+text, style.Quiet) + "\n"
}
