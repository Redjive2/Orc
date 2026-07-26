package read

import (
	"sort"
	"strings"
	"time"
)

// Moment is one thing that happened, in one tool, at one time.
//
// The timeline is the view that justifies decoding all of this: mail, task
// events, and sync actions are three separate stores with three separate
// shapes, and the question an operator actually has — "a task was claimed, what
// did that agent do next?" — crosses all of them. Reconstructing it by hand
// from three tools is possible; that is exactly why it should not have to be.
type Moment struct {
	At   time.Time
	Tool string
	Who  string
	What string
	// Detail is the human-readable rest of it.
	Detail string
	// Subject is what the moment is about — a message id, a task name — so a
	// reader can chase it in another view.
	Subject string
}

// Tools a moment can come from.
const (
	ToolMailman   = "mailman"
	ToolMacmuffin = "muff"
	ToolCQ        = "cq"
)

// Timeline merges every decoded store into one time-ordered sequence.
//
// Moments with no usable timestamp are dropped rather than sorted to the epoch,
// where they would sit at the top of every timeline pretending to be the oldest
// thing that ever happened.
func Timeline(mail Mail, tasks Tasks) []Moment {
	var out []Moment

	for _, msg := range mail.Messages {
		if msg.Sent.IsZero() {
			continue
		}
		to := strings.Join(msg.Recipients(), ", ")
		what := "sent"
		if msg.Kind != "" && msg.Kind != "mail" {
			what = msg.Kind
		}
		out = append(out, Moment{
			At: msg.Sent, Tool: ToolMailman, Who: msg.From, What: what,
			Detail: subjectOf(msg) + " → " + to, Subject: msg.MID,
		})
	}

	for _, ev := range mail.Events {
		if ev.At.IsZero() || ev.Op == "deliver" {
			// A delivery is the same instant as the send, from the other side.
			// Showing both doubles every line in the timeline and says nothing.
			continue
		}
		// Named by its subject where the message is still in the store, because
		// "alice read 0006576489b38288" answers nothing a reader was asking.
		detail := "message " + short(ev.MID)
		if msg, ok := mail.Find(ev.MID); ok {
			detail = subjectOf(msg) + " · from " + msg.From
		}
		out = append(out, Moment{
			At: ev.At, Tool: ToolMailman, Who: ev.User, What: ev.Op,
			Detail: detail, Subject: ev.MID,
		})
	}

	for _, task := range tasks.Tasks {
		for _, ev := range task.Events {
			at := ev.When()
			if at.IsZero() {
				continue
			}
			out = append(out, Moment{
				At: at, Tool: ToolMacmuffin, Who: ev.By, What: ev.Op,
				Detail: taskDetail(task.Name, ev), Subject: task.Name,
			})
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

func subjectOf(m Message) string {
	if strings.TrimSpace(m.Subject) == "" {
		return "(no subject)"
	}
	return m.Subject
}

func taskDetail(name string, ev TaskEvent) string {
	detail := name
	switch {
	case ev.Agent != "":
		detail += " · " + ev.Agent
	case ev.Sub != "":
		detail += " · " + ev.Sub
	case ev.Status != 0:
		detail += " · status " + itoa(ev.Status)
	case len(ev.Paths) > 0:
		detail += " · " + strings.Join(ev.Paths, " ")
	case ev.Path != "":
		detail += " · " + ev.Path
	}
	return detail
}

// short trims an identifier to its leading segment, which is enough to
// recognise one in a table and short enough not to own the column.
func short(id string) string {
	if i := strings.IndexByte(id, '-'); i > 0 {
		return id[:i]
	}
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
