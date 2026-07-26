package render

import (
	"fmt"
	"strings"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/mailman/internal/style"
	"orc/mailman/internal/view"
)

// Marks for a row's state. Each is redundant with a colour, never replaced by
// one, so the listing survives a pipe through grep.
const (
	MarkUnread = "*"
	MarkRead   = " "
	MarkNotice = "+"
	// MarkHistory marks a message the reader can see through a conversation but
	// was never personally sent.
	MarkHistory = "·"
)

// Listing renders a mailbox table: inbox, archive, or a query's results.
func Listing(title string, rows []view.Row, p style.Palette, width int) (string, error) {
	unread := 0
	for _, r := range rows {
		if r.Unread() {
			unread++
		}
	}

	t := Table{
		Title: title,
		Note:  fmt.Sprintf("%d unread · %d shown", unread, len(rows)),
		Columns: []Column{
			{Header: "", Align: Left, Min: 1},
			{Header: "id", Align: Right, Min: 2},
			{Header: "sent", Align: Left, Min: 16},
			{Header: "from", Align: Left, Weight: 1, Min: 6},
			{Header: "subject", Align: Left, Weight: 3, Min: 10},
			{Header: "conversation", Align: Left, Weight: 2, Min: 6},
		},
		Empty: "no messages",
	}

	for _, r := range rows {
		mark, paintMark := MarkRead, style.Palette.Muted
		if r.Unread() {
			mark, paintMark = MarkUnread, style.Palette.Unread
		}
		if r.Message.Kind().String() == "cc" {
			mark = MarkNotice
		}

		subjectPaint := style.Palette.Subject
		if !r.Unread() {
			subjectPaint = style.Palette.Muted
		}

		t.Rows = append(t.Rows, []Cell{
			Painted(mark, paintMark),
			Painted(itoa(r.PUID()), style.Palette.ID),
			Painted(clock.Show(r.Sent()), style.Palette.Muted),
			Painted(r.Message.From().String(), style.Palette.User),
			Painted(r.Message.Subject(), subjectPaint),
			Painted(convoRef(r), style.Palette.Convo),
		})
	}
	return Draw(t, p, width)
}

// convoRef renders a message's place in its conversation: the title, a short
// identifier, and the index. The reference doc asks for all three, and each is
// optional in the sense that a standalone message has none of them.
func convoRef(r view.Row) string {
	id, threaded := r.Message.Convo()
	if !threaded {
		return "—"
	}
	title := r.Title
	if title == "" {
		title = "(untitled)"
	}
	return fmt.Sprintf("%s · %s #%d", title, id.Short(), r.Message.Index())
}

// Card renders one message: a framed header and then the body verbatim.
//
// The body is emitted exactly as it was sent, outside the frame. It is markdown
// meant to be read or piped onward, and boxing it would mean wrapping it, which
// would corrupt code blocks and make the output useless as input to anything
// else.
func Card(r view.Row, p style.Palette, width int) (string, error) {
	width = clampWidth(width)
	msg := r.Message

	fields := [][2]string{
		{"from", msg.From().String()},
		{"to", strings.Join(user.Names(msg.To()), ", ")},
	}
	if cc := msg.CC(); len(cc) > 0 {
		fields = append(fields, [2]string{"cc", strings.Join(user.Names(cc), ", ")})
	}
	fields = append(fields,
		[2]string{"subject", msg.Subject()},
		[2]string{"sent", clock.Show(msg.Sent()) + " UTC"},
	)
	if id, threaded := msg.Convo(); threaded {
		title := r.Title
		if title == "" {
			title = "(untitled)"
		}
		fields = append(fields, [2]string{"thread", fmt.Sprintf("%s · %s #%d", title, id.String(), msg.Index())})
	}
	fields = append(fields, [2]string{"id", msg.ID().String()})

	label := 0
	for _, f := range fields {
		if w := style.Width(f[0]); w > label {
			label = w
		}
	}

	state := "read"
	statePaint := style.Palette.Muted
	if r.Unread() {
		state, statePaint = "unread", style.Palette.Unread
	}
	heading := fmt.Sprintf("message %d", r.PUID())

	// content is the writable width between the two verticals and their
	// padding spaces. Every line is padded to exactly it, so the right-hand
	// border is a straight column whatever the fields contain.
	content := width - 4

	var b strings.Builder

	// The top bar carries the heading on the left and the read state on the
	// right, with the rule stretched between them.
	fill := width - style.Width(heading) - style.Width(state) - 8
	if fill < 1 {
		fill = 1
	}
	b.WriteString(p.Frame(cardTopLeft + horizontal + " "))
	b.WriteString(p.Title(heading))
	b.WriteString(" ")
	b.WriteString(p.Frame(strings.Repeat(horizontal, fill)))
	b.WriteString(" ")
	b.WriteString(statePaint(p, state))
	b.WriteString(p.Frame(" " + horizontal + cardTopRight))
	b.WriteString("\n")

	for _, f := range fields {
		name, err := style.Pad(f[0], label, 'l')
		if err != nil {
			return "", err
		}
		value, err := style.Pad(style.Sanitise(f[1]), content-label-2, 'l')
		if err != nil {
			return "", err
		}
		b.WriteString(p.Frame(vertical))
		b.WriteString(" ")
		b.WriteString(p.Muted(name))
		b.WriteString("  ")
		b.WriteString(paintField(p, f[0], strings.TrimRight(value, " ")))
		b.WriteString(strings.Repeat(" ", style.Width(value)-style.Width(strings.TrimRight(value, " "))))
		b.WriteString(" ")
		b.WriteString(p.Frame(vertical))
		b.WriteString("\n")
	}

	b.WriteString(p.Frame(cardBottomLeft + strings.Repeat(horizontal, width-2) + cardBottomRight))
	b.WriteString("\n\n")

	body := msg.BodyString()
	b.WriteString(body)
	if body != "" && !strings.HasSuffix(body, "\n") {
		b.WriteString("\n")
	}
	return b.String(), nil
}

func paintField(p style.Palette, name, value string) string {
	switch name {
	case "from", "to", "cc":
		return p.User(value)
	case "subject":
		return p.Subject(value)
	case "thread":
		return p.Convo(value)
	case "id":
		return p.Muted(value)
	default:
		return value
	}
}

// Thread renders a conversation as a diagram, so the shape of a reply chain is
// visible without reading it.
func Thread(title string, id string, rows []view.Row, p style.Palette, width int) (string, error) {
	if err := fault.Check(title != "", "render.Thread", "conversation has no title"); err != nil {
		return "", err
	}
	width = clampWidth(width)

	var b strings.Builder
	b.WriteString(p.Title(title))
	b.WriteString(p.Muted(fmt.Sprintf("  ·  %s  ·  %d messages", id, len(rows))))
	b.WriteString("\n")

	if len(rows) == 0 {
		b.WriteString(p.Muted("  (no messages you can see)"))
		b.WriteString("\n")
		return b.String(), nil
	}

	for i, r := range rows {
		branch := threadBranch
		if i == len(rows)-1 {
			branch = threadLast
		}

		mark, markPaint := MarkRead, style.Palette.Muted
		if r.Unread() {
			mark, markPaint = MarkUnread, style.Palette.Unread
		}
		// History the reader was never sent has no puid, because nothing ever
		// delivered it to them. Saying so is better than printing a zero that
		// would not open.
		ref := fmt.Sprintf("  [%d]", r.PUID())
		if !r.Filed {
			mark, markPaint = MarkHistory, style.Palette.Muted
			ref = "  [—]"
		}

		head := fmt.Sprintf("#%d  %s  %s", r.Message.Index(), clock.Show(r.Sent()), r.Message.From())
		b.WriteString(p.Frame(branch))
		b.WriteString(markPaint(p, mark))
		b.WriteString(" ")
		b.WriteString(p.Muted(fmt.Sprintf("#%d", r.Message.Index())))
		b.WriteString(" ")
		b.WriteString(p.Muted(clock.Show(r.Sent())))
		b.WriteString("  ")
		b.WriteString(p.User(r.Message.From().String()))
		b.WriteString("  ")

		room := width - style.Width(head) - 10
		if room < 10 {
			room = 10
		}
		subject, err := style.Truncate(style.Sanitise(r.Message.Subject()), room)
		if err != nil {
			return "", err
		}
		b.WriteString(p.Subject(subject))
		b.WriteString(p.Muted(ref))
		b.WriteString("\n")

		if i < len(rows)-1 {
			b.WriteString(p.Frame(threadStem))
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}

// Receipts renders who has and has not read a message.
func Receipts(subject string, statuses []view.Status, p style.Palette, width int) (string, error) {
	read := 0
	for _, s := range statuses {
		if s.Read() {
			read++
		}
	}

	t := Table{
		Title: "read by · " + subject,
		Note:  fmt.Sprintf("%d of %d", read, len(statuses)),
		Columns: []Column{
			{Header: "recipient", Align: Left, Weight: 1, Min: 6},
			{Header: "status", Align: Left, Min: 6},
			{Header: "read at", Align: Left, Min: 16},
		},
		Empty: "this message has no recipients",
	}

	for _, s := range statuses {
		mark, when, paint := "· unread", "—", style.Palette.Muted
		if s.Read() {
			mark, when, paint = "✓ read", clock.Show(s.ReadAt), style.Palette.Good
		}
		t.Rows = append(t.Rows, []Cell{
			Painted(s.User.String(), style.Palette.User),
			Painted(mark, paint),
			Painted(when, style.Palette.Muted),
		})
	}
	return Draw(t, p, width)
}

// Users renders the mailbox list, for the provisioning command.
func Users(names []user.Name, p style.Palette, width int) (string, error) {
	t := Table{
		Title:   "mailboxes",
		Note:    fmt.Sprintf("%d", len(names)),
		Columns: []Column{{Header: "user", Align: Left, Weight: 1, Min: 6}},
		Empty:   "no mailboxes; orc provisions these",
	}
	for _, n := range names {
		t.Rows = append(t.Rows, []Cell{Painted(n.String(), style.Palette.User)})
	}
	return Draw(t, p, width)
}

// WholeStore draws every message in the store, with who holds it and who has
// read it.
//
// One row per message, not per mailbox: the question this view answers is "who
// has this and have they read it", and a row per holder would make the reader
// do that join by eye.
func WholeStore(whole []view.Whole, p style.Palette, width int) (string, error) {
	t := Table{
		Title: "the whole store",
		Note:  fmt.Sprintf("%d", len(whole)),
		Columns: []Column{
			{Header: "sent", Align: Left, Min: 5},
			{Header: "from", Align: Left, Weight: 1, Min: 6},
			{Header: "subject", Align: Left, Weight: 3, Min: 10},
			{Header: "held by", Align: Left, Weight: 2, Min: 8},
		},
		Empty: "no mail",
	}
	for _, w := range whole {
		t.Rows = append(t.Rows, []Cell{
			Plain(w.Message.Sent().Format("15:04")),
			Painted(w.Message.From().String(), style.Palette.User),
			Plain(w.Message.Subject()),
			Plain(holders(w)),
		})
	}
	return Draw(t, p, width)
}

// holders names each mailbox and marks the ones that have not read it, so the
// column answers "who has not seen this" at a glance rather than only "who has
// it". A sender's own copy is marked as such: it was never a delivery.
func holders(w view.Whole) string {
	seen := map[string]bool{}
	for _, r := range w.Receipts {
		seen[r.User.String()] = true
	}
	out := make([]string, 0, len(w.Holders))
	for _, h := range w.Holders {
		switch {
		case h.Mine:
			out = append(out, h.User.String()+" (sender)")
		case h.Read || seen[h.User.String()]:
			out = append(out, h.User.String())
		default:
			out = append(out, h.User.String()+"*")
		}
	}
	if len(out) == 0 {
		return "—"
	}
	return strings.Join(out, ", ")
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
