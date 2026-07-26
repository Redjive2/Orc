package render

import (
	"fmt"
	"strings"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/macmuffin/internal/style"
	"orc/macmuffin/internal/task"
	"orc/macmuffin/internal/view"
	"orc/theme"
)

// Meter is how progress is drawn: a bar that reads at a glance, and the numbers
// beside it for when colour and glyphs are gone.
const (
	meterFull  = "▓"
	meterEmpty = "░"
	meterWidth = 7
)

// Marks that carry state without colour. Each is shown beside a word, never
// instead of one, so a pipe through grep keeps the meaning.
const (
	markDraft = "draft"
	markNone  = "—"
)

// Board renders the pool: one row per task, sorted by view.Sort.
func Board(p view.Pool, scope view.Scope, palette style.Palette, width int) (string, error) {
	active, drafts, done := p.Counts()

	note := fmt.Sprintf("%d active", active)
	if drafts > 0 {
		note += fmt.Sprintf(" · %d draft", drafts)
	}
	if scope == view.All {
		note += fmt.Sprintf(" · %d done", done)
	}

	t := Table{
		Title: "pool · " + p.Viewer().String(),
		Note:  note,
		Columns: []Column{
			{Header: "task", Align: Left, Weight: 3, Min: 10},
			{Header: "P", Align: Right, Min: 1},
			{Header: "D", Align: Right, Min: 1},
			{Header: "status", Align: Left, Min: 9},
			{Header: "progress", Align: Left, Min: 8},
			{Header: "owner", Align: Left, Weight: 1, Min: 5},
			{Header: "with", Align: Left, Weight: 2, Min: 4},
		},
		Empty: "no tasks — `muff create <task> <priority> <difficulty>`",
	}

	for _, item := range p.Tasks() {
		t.Rows = append(t.Rows, []Cell{
			Painted(taskLabel(item), paintTask(item)),
			Painted(fmt.Sprint(item.Priority().Value()), style.Palette.Score),
			Painted(fmt.Sprint(item.Difficulty().Value()), style.Palette.Score),
			Painted(item.Status().Label(), paintStatus(item.Status())),
			Painted(Meter(item), style.Palette.Muted),
			Painted(ownerLabel(item), style.Palette.Agent),
			Painted(collaboratorLabel(item), style.Palette.Muted),
		})
	}
	return Draw(t, palette, width)
}

// taskLabel names a task, marking a draft so it is never mistaken for pooled
// work anyone can take.
func taskLabel(t task.Task) string {
	if t.Completed() {
		return t.Name().String() + "  (done)"
	}
	if !t.Pooled() {
		return t.Name().String() + "  (" + markDraft + ")"
	}
	return t.Name().String()
}

// paintTask dims what is finished or not yet published, so the eye lands on the
// work that is actually live.
func paintTask(t task.Task) func(style.Palette, string) string {
	if t.Completed() || !t.Pooled() {
		return style.Palette.Muted
	}
	return style.Palette.Task
}

func paintStatus(s task.Status) func(style.Palette, string) string {
	switch s {
	case task.StatusBroken:
		return style.Palette.Broken
	case task.StatusSlow:
		return style.Palette.Slow
	case task.StatusNominal:
		return style.Palette.Nominal
	case task.StatusDone:
		return style.Palette.Done
	default:
		return style.Palette.Muted
	}
}

func ownerLabel(t task.Task) string {
	if owner, owned := t.Owner(); owned {
		return owner.String()
	}
	return markNone
}

func collaboratorLabel(t task.Task) string {
	names := user.Names(t.Collaborators())
	if len(names) == 0 {
		return markNone
	}
	return strings.Join(names, ", ")
}

// Meter draws progress as a bar and its numbers.
//
// The bar is the thing that reads at a glance; the numbers are what survives a
// pipe, a dumb terminal, and a reader who wants to know whether "nearly done"
// means 7/8 or 70/80. Both, always.
func Meter(t task.Task) string {
	done, total := t.Progress()
	if total == 0 {
		return markNone
	}

	filled := done * meterWidth / total
	// A task with any progress at all shows at least one block, and one that is
	// not finished never shows a full bar — a meter that rounded to "complete"
	// while work remained would be the one thing it must not say.
	if done > 0 && filled == 0 {
		filled = 1
	}
	if done < total && filled == meterWidth {
		filled = meterWidth - 1
	}

	return strings.Repeat(meterFull, filled) +
		strings.Repeat(meterEmpty, meterWidth-filled) +
		fmt.Sprintf(" %d/%d", done, total)
}

// Card renders one task in full: metadata in a titled box, scope as an aligned
// list, and subtasks as a checklist — so the state of the work is visible
// without reading it.
// describedLabel says whether the task has a description, and where to read it.
func describedLabel(t task.Task) string {
	if !t.Described() {
		return "none yet — `muff describe " + t.Name().String() + " --edit`"
	}
	return "by " + t.DescribedBy().String() + " — `muff describe " + t.Name().String() + "` reads it"
}

func Card(t task.Task, palette style.Palette, width int) (string, error) {
	if t.Name().Zero() {
		return "", fault.Internal{Where: "render.Card", Detail: "no task given"}
	}
	width = clampWidth(width)
	inner := width - 4

	var b strings.Builder

	// The head bar carries the name on the left and the scores, status, and
	// progress on the right — the four things a reader wants before deciding
	// whether to read anything else.
	done, total := t.Progress()
	right := fmt.Sprintf("P%d  D%d  %s", t.Priority().Value(), t.Difficulty().Value(), t.Status().Label())
	if total > 0 {
		right += fmt.Sprintf("  %d/%d", done, total)
	}
	if err := headBar(&b, palette, width, t.Name().String(), right); err != nil {
		return "", err
	}

	fields := [][2]string{
		{"owner", ownerLabel(t)},
		{"author", t.Author().String()},
		{"created", clock.Show(t.Created())},
		{"state", stateLabel(t)},
	}
	if with := collaboratorLabel(t); with != markNone {
		fields = append(fields, [2]string{"with", with})
	}
	if wt, bound := t.Worktree(); bound {
		fields = append(fields, [2]string{"worktree", wt})
	}
	// The description is a field rather than a section: the card is a summary, and
	// the prose can be pages. What belongs here is that there *is* one and how to
	// read it — a card that quietly omitted a task's specification would be a card
	// somebody reads instead of the spec.
	fields = append(fields, [2]string{"described", describedLabel(t)})

	label := 0
	for _, f := range fields {
		if w := theme.Width(f[0]); w > label {
			label = w
		}
	}
	for _, f := range fields {
		if err := fieldLine(&b, palette, inner, label, f[0], f[1]); err != nil {
			return "", err
		}
	}

	// Scope and subtasks are sections rather than fields: they are lists, and a
	// list crammed into a field would wrap, which the whole layout refuses to
	// do.
	if t.Scoped() {
		if err := section(&b, palette, width, "scope"); err != nil {
			return "", err
		}
		for _, path := range t.Scope() {
			if err := listLine(&b, palette, inner, " ", path, style.Palette.Path); err != nil {
				return "", err
			}
		}
	} else {
		if err := section(&b, palette, width, "scope"); err != nil {
			return "", err
		}
		if err := listLine(&b, palette, inner, " ",
			"none yet — `muff scope "+t.Name().String()+" <paths...>`", style.Palette.Muted); err != nil {
			return "", err
		}
	}

	if total > 0 {
		if err := section(&b, palette, width, "subtasks"); err != nil {
			return "", err
		}
		for _, sub := range t.Subtasks() {
			paint := style.Palette.Muted
			if !sub.Done() {
				paint = style.Palette.Task
			}
			if err := listLine(&b, palette, inner, sub.Mark(), sub.Name().String(), paint); err != nil {
				return "", err
			}
		}
	}

	b.WriteString(palette.Frame(cardBottomLeft + strings.Repeat(horizontal, width-2) + cardBottomRight))
	b.WriteString("\n")
	return b.String(), nil
}

func stateLabel(t task.Task) string {
	switch {
	case t.Completed():
		return "completed " + clock.Show(t.CompletedAt())
	case !t.Pooled():
		return "draft — not in the pool"
	default:
		return "in the pool"
	}
}

// headBar draws the card's top rule with a title and a right-hand summary.
func headBar(b *strings.Builder, p style.Palette, width int, title, right string) error {
	fill := width - theme.Width(title) - theme.Width(right) - 8
	if fill < 1 {
		fill = 1
	}
	b.WriteString(p.Frame(cardTopLeft + horizontal + " "))
	b.WriteString(p.Title(theme.Sanitise(title)))
	b.WriteString(" ")
	b.WriteString(p.Frame(strings.Repeat(horizontal, fill)))
	b.WriteString(" ")
	b.WriteString(p.Muted(theme.Sanitise(right)))
	b.WriteString(p.Frame(" " + horizontal + cardTopRight))
	b.WriteString("\n")
	return nil
}

// section draws a labelled divider inside the card.
func section(b *strings.Builder, p style.Palette, width int, label string) error {
	fill := width - theme.Width(label) - 5
	if fill < 1 {
		fill = 1
	}
	b.WriteString(p.Frame(teeRight + horizontal + " "))
	b.WriteString(p.Header(label))
	b.WriteString(" ")
	b.WriteString(p.Frame(strings.Repeat(horizontal, fill) + teeLeft))
	b.WriteString("\n")
	return nil
}

// fieldLine draws one `name  value` row, padded so the right border is straight.
func fieldLine(b *strings.Builder, p style.Palette, inner, label int, name, value string) error {
	padded, err := theme.Pad(name, label, 'l')
	if err != nil {
		return err
	}
	return contentLine(b, p, inner, p.Muted(padded)+"  ", value, paintField(name))
}

// listLine draws one `mark  value` row of a section.
func listLine(b *strings.Builder, p style.Palette, inner int, mark, value string, paint func(style.Palette, string) string) error {
	return contentLine(b, p, inner, p.Muted(mark)+" ", value, paint)
}

// contentLine writes a framed line whose visible width is exactly inner.
//
// The padding is measured on the plain text and the colour applied afterwards,
// so escape sequences can never change how wide a line is — which is the rule
// that keeps a coloured card identical in shape to a plain one.
func contentLine(b *strings.Builder, p style.Palette, inner int, prefix, value string, paint func(style.Palette, string) string) error {
	room := inner - theme.Width(stripped(prefix))
	if room < 1 {
		room = 1
	}
	padded, err := theme.Pad(theme.Sanitise(value), room, 'l')
	if err != nil {
		return err
	}
	trimmed := strings.TrimRight(padded, " ")
	gap := theme.Width(padded) - theme.Width(trimmed)

	b.WriteString(p.Frame(vertical))
	b.WriteString(" ")
	b.WriteString(prefix)
	b.WriteString(paint(p, trimmed))
	b.WriteString(strings.Repeat(" ", gap))
	b.WriteString(" ")
	b.WriteString(p.Frame(vertical))
	b.WriteString("\n")
	return nil
}

// stripped removes escape sequences, so a prefix that has already been painted
// still measures as the text it draws.
func stripped(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			i = j + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func paintField(name string) func(style.Palette, string) string {
	switch name {
	case "owner", "author":
		return style.Palette.Agent
	case "worktree":
		return style.Palette.Path
	case "with":
		return style.Palette.Muted
	default:
		return style.Palette.Muted
	}
}
