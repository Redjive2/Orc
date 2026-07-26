// Package view builds the tables the omniscient commands draw.
//
// Every function here is a pure function of decoded state: it takes what
// internal/read produced and returns a render.Table. Nothing in this package
// touches a filesystem, so every view can be tested by handing it a world and
// comparing bytes.
//
// The house rules the tables follow, all of them from AGENTS.md and the tree's
// habits: box-drawn frames, aligned columns, colour as a layer that is always
// redundant with a word or a glyph, and no view that hides how much it is not
// showing.
package view

import (
	"fmt"
	"strings"
	"time"

	"orc/orcprobe/internal/clock"
	"orc/orcprobe/internal/read"
	"orc/orcprobe/internal/render"
	"orc/orcprobe/internal/style"
)

// World is the screen a god-agent opens first: every mailbox, the task pool,
// the sync state, and the probe's own liveness, on one page.
type World struct {
	Probe    string
	ID       string
	Age      string
	Liveness string
	Mail     read.Mail
	Tasks    read.Tasks
	Sync     read.Sync
}

// Draw renders the world as a stack of small tables.
func (w World) Draw(p style.Palette, width int) (string, error) {
	var b strings.Builder

	boxes, err := w.mailboxes(p, width)
	if err != nil {
		return "", err
	}
	b.WriteString(boxes)

	tasks, err := w.pool(p, width)
	if err != nil {
		return "", err
	}
	b.WriteString(tasks)

	b.WriteString(w.footer(p))
	return b.String(), nil
}

func (w World) mailboxes(p style.Palette, width int) (string, error) {
	rows := make([][]render.Cell, 0, len(w.Mail.Mailboxes))
	for _, box := range w.Mail.Mailboxes {
		unread := fmt.Sprintf("%d", box.Unread)
		paint := style.Palette.Muted
		if box.Unread > 0 {
			paint = style.Palette.Warn
		}
		rows = append(rows, []render.Cell{
			render.Painted(box.Name, style.Palette.User),
			render.Painted(unread, paint),
			render.Plain(fmt.Sprintf("%d", box.Total)),
			render.Painted(fmt.Sprintf("%d", box.Archived), style.Palette.Muted),
		})
	}

	return render.Draw(render.Table{
		Title: "mailboxes",
		Note:  fmt.Sprintf("%d messages", len(w.Mail.Messages)),
		Columns: []render.Column{
			{Header: "user", Align: render.Left, Weight: 1, Min: 6},
			{Header: "unread", Align: render.Right, Min: 6},
			{Header: "held", Align: render.Right, Min: 4},
			{Header: "archived", Align: render.Right, Min: 8},
		},
		Rows:  rows,
		Empty: "no mailboxes in this probe",
	}, p, width)
}

func (w World) pool(p style.Palette, width int) (string, error) {
	rows := make([][]render.Cell, 0, len(w.Tasks.Tasks))
	for _, task := range w.Tasks.Tasks {
		done, total := task.Done()
		owner, paint := "—", style.Palette.Muted
		if task.Owner != "" {
			// An owner in a neutered probe is the thing worth noticing on this
			// screen, so it is painted as a warning rather than as a fact.
			owner, paint = task.Owner, style.Palette.Warn
		}
		rows = append(rows, []render.Cell{
			render.Painted(task.Name, style.Palette.Probe),
			render.Painted(owner, paint),
			render.Plain(fmt.Sprintf("%d/%d", done, total)),
			render.Plain(statusText(task.Status)),
			render.Painted(strings.Join(task.Collaborators, " "), style.Palette.Muted),
		})
	}

	note := fmt.Sprintf("%d tasks", len(w.Tasks.Tasks))
	if n := len(w.Tasks.Tombstones); n > 0 {
		note += fmt.Sprintf(" · %d deleted", n)
	}

	return render.Draw(render.Table{
		Title: "tasks",
		Note:  note,
		Columns: []render.Column{
			{Header: "task", Align: render.Left, Weight: 1, Min: 6},
			{Header: "owner", Align: render.Left, Min: 5},
			{Header: "subs", Align: render.Right, Min: 4},
			{Header: "status", Align: render.Left, Min: 6},
			{Header: "collaborators", Align: render.Left, Weight: 2, Min: 6},
		},
		Rows:  rows,
		Empty: "no tasks in this probe",
	}, p, width)
}

// footer is the part of the world screen that is about the probe rather than
// about what is in it.
func (w World) footer(p style.Palette) string {
	liveness := p.Good(w.Liveness)
	switch w.Liveness {
	case "verbatim":
		liveness = p.Bad(w.Liveness)
	case "partial":
		liveness = p.Warn(w.Liveness)
	}

	lines := []string{
		fmt.Sprintf("  %s %s · taken %s · %s",
			p.Muted("probe"), p.Probe(w.Probe), w.Age, liveness),
	}

	if w.Sync.Present {
		cursor := p.Good("no sync cursor")
		if w.Sync.Cursor {
			cursor = p.Bad("a sync cursor is still here")
		}
		lines = append(lines, fmt.Sprintf("  %s %d actions applied · %s",
			p.Muted("cq"), w.Sync.Applied, cursor))
	}
	if held := w.Tasks.Held(); len(held) > 0 {
		names := make([]string, 0, len(held))
		for _, task := range held {
			names = append(names, task.Name)
		}
		lines = append(lines, fmt.Sprintf("  %s %s still %s",
			p.Warn("!"), strings.Join(names, ", "), heldWord(len(held))))
	}
	if n := len(w.Mail.Damage) + len(w.Tasks.Damage) + len(w.Sync.Damage); n > 0 {
		lines = append(lines, fmt.Sprintf("  %s %d file(s) could not be read — see `orcprobe journal`", p.Bad("✗"), n))
	}
	return strings.Join(lines, "\n") + "\n"
}

func heldWord(n int) string {
	if n == 1 {
		return "has an owner or a collaborator"
	}
	return "have owners or collaborators"
}

// Mail is every mailbox at once: the view no single identity can produce.
type Mail struct {
	Query    string
	Messages []read.Message
	Total    int
}

// Draw renders the cross-user mail table.
func (m Mail) Draw(p style.Palette, width int) (string, error) {
	rows := make([][]render.Cell, 0, len(m.Messages))
	for _, msg := range m.Messages {
		mark, paint := " ", style.Palette.Muted
		if msg.UnreadBy() {
			mark, paint = "*", style.Palette.Warn
		}
		rows = append(rows, []render.Cell{
			render.Painted(mark, paint),
			render.Painted(clock.Show(msg.Sent), style.Palette.Muted),
			render.Painted(msg.From, style.Palette.User),
			render.Plain(strings.Join(msg.Recipients(), " ")),
			render.Painted(subject(msg), style.Palette.Subject),
			render.Painted(readers(msg), style.Palette.Muted),
		})
	}

	note := fmt.Sprintf("%d of %d", len(m.Messages), m.Total)
	title := "all mail"
	if strings.TrimSpace(m.Query) != "" {
		title = "mail · " + m.Query
	}

	return render.Draw(render.Table{
		Title: title,
		Note:  note,
		Columns: []render.Column{
			{Header: " ", Align: render.Centre, Min: 1},
			{Header: "sent", Align: render.Left, Min: 16},
			{Header: "from", Align: render.Left, Min: 5},
			{Header: "to", Align: render.Left, Weight: 2, Min: 5},
			{Header: "subject", Align: render.Left, Weight: 3, Min: 8},
			{Header: "read by", Align: render.Left, Weight: 1, Min: 7},
		},
		Rows:  rows,
		Empty: "nothing matches",
	}, p, width)
}

func subject(m read.Message) string {
	if strings.TrimSpace(m.Subject) == "" {
		return "(no subject)"
	}
	return m.Subject
}

// readers renders who has read a message — the cross-user fact a single
// mailbox cannot show, and the reason `read by` is a column here and not in
// `mailman inbox`.
func readers(m read.Message) string {
	if len(m.Readers) == 0 {
		return "nobody"
	}
	return strings.Join(m.Readers, " ")
}

// Tasks is the whole pool, including what `muff pool` hides.
type Tasks struct {
	Tasks      []read.Task
	Tombstones []string
}

// Draw renders the task table.
func (t Tasks) Draw(p style.Palette, width int) (string, error) {
	rows := make([][]render.Cell, 0, len(t.Tasks))
	for _, task := range t.Tasks {
		done, total := task.Done()
		owner, paint := "—", style.Palette.Muted
		if task.Owner != "" {
			owner, paint = task.Owner, style.Palette.Warn
		}
		state := "draft"
		switch {
		case task.Complete:
			state = "complete"
		case task.Pushed:
			state = "pooled"
		}
		rows = append(rows, []render.Cell{
			render.Painted(task.Name, style.Palette.Probe),
			render.Painted(owner, paint),
			render.Plain(fmt.Sprintf("%d/%d", done, total)),
			render.Plain(statusText(task.Status)),
			render.Painted(state, style.Palette.Muted),
			render.Plain(fmt.Sprintf("%d/%d", task.Priority, task.Difficulty)),
			render.Painted(strings.Join(task.Scope, " "), style.Palette.Path),
		})
	}

	note := fmt.Sprintf("%d", len(t.Tasks))
	if n := len(t.Tombstones); n > 0 {
		note += fmt.Sprintf(" · %d deleted: %s", n, strings.Join(t.Tombstones, ", "))
	}

	return render.Draw(render.Table{
		Title: "tasks",
		Note:  note,
		Columns: []render.Column{
			{Header: "task", Align: render.Left, Weight: 1, Min: 6},
			{Header: "owner", Align: render.Left, Min: 5},
			{Header: "subs", Align: render.Right, Min: 4},
			{Header: "status", Align: render.Left, Min: 6},
			{Header: "state", Align: render.Left, Min: 5},
			{Header: "p/d", Align: render.Right, Min: 3},
			{Header: "scope", Align: render.Left, Weight: 3, Min: 5},
		},
		Rows:  rows,
		Empty: "no tasks in this probe",
	}, p, width)
}

// statusText renders Macmuffin's health signal as a word. The numbers are its
// vocabulary; nobody reads a bare 2 and knows what it means.
func statusText(status int) string {
	switch status {
	case 1:
		return "stuck"
	case 2:
		return "slow"
	case 3:
		return "nominal"
	case 4:
		return "done"
	default:
		return "—"
	}
}

// Journal is one append-only journal, decoded — the debugging primitive the
// other tools deliberately do not expose.
type Journal struct {
	Title  string
	Events []JournalLine
}

// JournalLine is one decoded event.
type JournalLine struct {
	At     time.Time
	Who    string
	Op     string
	Detail string
}

// Draw renders the journal.
func (j Journal) Draw(p style.Palette, width int) (string, error) {
	rows := make([][]render.Cell, 0, len(j.Events))
	for _, ev := range j.Events {
		rows = append(rows, []render.Cell{
			render.Painted(clock.Show(ev.At), style.Palette.Muted),
			render.Painted(ev.Who, style.Palette.User),
			render.Painted(ev.Op, style.Palette.ID),
			render.Plain(ev.Detail),
		})
	}

	return render.Draw(render.Table{
		Title: "journal · " + j.Title,
		Note:  fmt.Sprintf("%d events", len(j.Events)),
		Columns: []render.Column{
			{Header: "at", Align: render.Left, Min: 16},
			{Header: "by", Align: render.Left, Min: 5},
			{Header: "op", Align: render.Left, Min: 5},
			{Header: "detail", Align: render.Left, Weight: 3, Min: 8},
		},
		Rows:  rows,
		Empty: "nothing recorded",
	}, p, width)
}

// Timeline is every tool's events merged into one sequence.
type Timeline struct {
	Moments []read.Moment
	Since   string
}

// Draw renders the timeline.
func (t Timeline) Draw(p style.Palette, width int) (string, error) {
	rows := make([][]render.Cell, 0, len(t.Moments))
	for _, m := range t.Moments {
		paint := style.Palette.ID
		if m.Tool == read.ToolMacmuffin {
			paint = style.Palette.Probe
		}
		rows = append(rows, []render.Cell{
			render.Painted(clock.Show(m.At), style.Palette.Muted),
			render.Painted(m.Tool, paint),
			render.Painted(m.Who, style.Palette.User),
			render.Painted(m.What, style.Palette.ID),
			render.Plain(m.Detail),
		})
	}

	title := "timeline"
	if t.Since != "" {
		title += " · since " + t.Since
	}

	return render.Draw(render.Table{
		Title: title,
		Note:  fmt.Sprintf("%d moments", len(t.Moments)),
		Columns: []render.Column{
			{Header: "at", Align: render.Left, Min: 16},
			{Header: "tool", Align: render.Left, Min: 4},
			{Header: "who", Align: render.Left, Min: 4},
			{Header: "what", Align: render.Left, Min: 5},
			{Header: "detail", Align: render.Left, Weight: 3, Min: 8},
		},
		Rows:  rows,
		Empty: "nothing happened in this window",
	}, p, width)
}
