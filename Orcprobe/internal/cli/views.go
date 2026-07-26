package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"orc/orcprobe/internal/clock"
	"orc/orcprobe/internal/fault"
	"orc/orcprobe/internal/probe"
	"orc/orcprobe/internal/query"
	"orc/orcprobe/internal/read"
	"orc/orcprobe/internal/source"
	"orc/orcprobe/internal/view"
)

// The omniscient views.
//
// Every command here reads the copied stores directly, with no identity and no
// permission check. That is the half of god mode `shell` cannot provide: `as
// alice` shows what alice can see, and these show what nobody can.
//
// None of them runs another tool, so they work in a probe whose tools are not
// even installed — which is also what makes them the right thing to reach for
// when a tool is what you are debugging.

// stores opens a probe's copied state for reading.
type stores struct {
	probe *probe.Probe
	mail  read.Mail
	tasks read.Tasks
	sync  read.Sync
}

// open resolves a probe and decodes as much of it as the caller asks for.
//
// bodies is separate because it is expensive: a store with ten thousand
// messages should not be pulled into memory to draw a table of subjects. Only
// a query that mentions the body needs them.
func (a App) open(name string, bodies bool) (stores, error) {
	store, err := a.store()
	if err != nil {
		return stores{}, err
	}
	p, err := store.Resolve(name)
	if err != nil {
		return stores{}, err
	}

	// The stamp is checked before anything is read, so a directory that lost it
	// is never treated as a probe.
	if _, err := probe.ReadStamp(p.Dir()); err != nil {
		return stores{}, err
	}

	out := stores{probe: p}

	if out.mail, err = read.Mailman(p.Path(filepath.FromSlash(source.Of(source.Mailman).Dir)), bodies); err != nil {
		return stores{}, err
	}
	if out.tasks, err = read.Macmuffin(p.Path(filepath.FromSlash(source.Of(source.Macmuffin).Dir))); err != nil {
		return stores{}, err
	}
	if out.sync, err = read.CQ(p.Path(filepath.FromSlash(source.Of(source.CQ).Dir))); err != nil {
		return stores{}, err
	}
	return out, nil
}

// world draws the whole probe on one screen.
func (a App) world(args []string, f flags) error {
	if len(args) != 0 {
		return fault.Usage{Reason: "world takes no arguments"}
	}
	s, err := a.open(f.probe, false)
	if err != nil {
		return err
	}

	age := "unknown"
	if at, err := s.probe.CreatedAt(); err == nil {
		age = clock.Since(a.Clock.Now(), at)
	}

	text, err := view.World{
		Probe:    s.probe.Name,
		ID:       s.probe.ID,
		Age:      age,
		Liveness: s.probe.Liveness(),
		Mail:     s.mail,
		Tasks:    s.tasks,
		Sync:     s.sync,
	}.Draw(a.out, a.Width)
	if err != nil {
		return err
	}
	return a.write(text)
}

// mail draws every mailbox at once, filtered by Mailman's own query language.
func (a App) mail(args []string, f flags) error {
	text := strings.Join(args, " ")

	q, err := query.Parse(text)
	if err != nil {
		return err
	}
	// Bodies are only decoded when the query actually asks about them.
	s, err := a.open(f.probe, strings.Contains(strings.ToLower(text), "body"))
	if err != nil {
		return err
	}

	matched := q.Select(s.mail.Messages, a.Clock.Now())
	drawn, err := view.Mail{
		Query:    q.String(),
		Messages: matched,
		Total:    len(s.mail.Messages),
	}.Draw(a.out, a.Width)
	if err != nil {
		return err
	}
	if err := a.write(drawn); err != nil {
		return err
	}
	a.reportDamage(s.mail.Damage)
	return nil
}

// tasks draws the whole pool, including what `muff pool` hides.
func (a App) tasks(args []string, f flags) error {
	if len(args) != 0 {
		return fault.Usage{Reason: "tasks takes no arguments"}
	}
	s, err := a.open(f.probe, false)
	if err != nil {
		return err
	}

	text, err := view.Tasks{Tasks: s.tasks.Tasks, Tombstones: s.tasks.Tombstones}.Draw(a.out, a.Width)
	if err != nil {
		return err
	}
	if err := a.write(text); err != nil {
		return err
	}
	a.reportDamage(s.tasks.Damage)
	return nil
}

// journal draws one append-only journal, decoded.
//
// The argument is a task name or a mailbox name. They cannot collide in
// practice, but when they do the task wins and the note says so — silently
// picking one would make the command lie about what it is showing.
func (a App) journal(args []string, f flags) error {
	if len(args) != 1 {
		return fault.Usage{Reason: "journal takes one name: orcprobe journal <task|mailbox>"}
	}
	name := args[0]

	s, err := a.open(f.probe, false)
	if err != nil {
		return err
	}

	if task, ok := s.tasks.Find(name); ok {
		if _, alsoMailbox := findMailbox(s.mail, name); alsoMailbox {
			a.note("%q is both a task and a mailbox; showing the task", name)
		}
		lines := make([]view.JournalLine, 0, len(task.Events))
		for _, ev := range task.Events {
			lines = append(lines, view.JournalLine{
				At: ev.When(), Who: ev.By, Op: ev.Op, Detail: taskEventDetail(ev),
			})
		}
		text, err := view.Journal{Title: "task " + name, Events: lines}.Draw(a.out, a.Width)
		if err != nil {
			return err
		}
		return a.write(text)
	}

	if _, ok := findMailbox(s.mail, name); ok {
		var lines []view.JournalLine
		for _, ev := range s.mail.Events {
			if ev.User != name {
				continue
			}
			lines = append(lines, view.JournalLine{
				At: ev.At, Who: name, Op: ev.Op, Detail: mailEventDetail(s.mail, ev),
			})
		}
		text, err := view.Journal{Title: "mailbox " + name, Events: lines}.Draw(a.out, a.Width)
		if err != nil {
			return err
		}
		return a.write(text)
	}

	return fault.NotFound{Target: name, Near: append(taskNames(s.tasks), mailboxNames(s.mail)...)}
}

// timeline draws every tool's events in one sequence.
func (a App) timeline(args []string, f flags) error {
	if len(args) != 0 {
		return fault.Usage{Reason: "timeline takes no arguments; use --since to narrow it"}
	}
	s, err := a.open(f.probe, false)
	if err != nil {
		return err
	}

	moments := read.Timeline(s.mail, s.tasks)
	if f.since != "" {
		cut, err := sinceTime(f.since, a.Clock.Now())
		if err != nil {
			return err
		}
		kept := moments[:0]
		for _, m := range moments {
			if !m.At.Before(cut) {
				kept = append(kept, m)
			}
		}
		moments = kept
	}
	if f.tool != "" {
		kept := moments[:0]
		for _, m := range moments {
			if m.Tool == f.tool {
				kept = append(kept, m)
			}
		}
		moments = kept
	}

	text, err := view.Timeline{Moments: moments, Since: f.since}.Draw(a.out, a.Width)
	if err != nil {
		return err
	}
	return a.write(text)
}

// sinceTime reads --since: an instant, a date, or a span like "2h".
func sinceTime(text string, now time.Time) (time.Time, error) {
	if at, err := clock.Parse(text); err == nil {
		return at, nil
	}
	if at, err := time.Parse("2006-01-02", text); err == nil {
		return at.UTC(), nil
	}
	if d, err := time.ParseDuration(text); err == nil {
		return now.Add(-d), nil
	}
	return time.Time{}, fault.Usage{Reason: "--since " + text + " is not a timestamp, a date, or a span like 2h"}
}

func findMailbox(m read.Mail, name string) (read.Mailbox, bool) {
	for _, box := range m.Mailboxes {
		if box.Name == name {
			return box, true
		}
	}
	return read.Mailbox{}, false
}

func taskNames(t read.Tasks) []string {
	out := make([]string, 0, len(t.Tasks))
	for _, task := range t.Tasks {
		out = append(out, task.Name)
	}
	return out
}

func mailboxNames(m read.Mail) []string {
	out := make([]string, 0, len(m.Mailboxes))
	for _, box := range m.Mailboxes {
		out = append(out, box.Name)
	}
	return out
}

func taskEventDetail(ev read.TaskEvent) string {
	switch {
	case ev.Agent != "":
		return ev.Agent
	case ev.Sub != "":
		return ev.Sub
	case ev.Status != 0:
		return fmt.Sprintf("status %d", ev.Status)
	case len(ev.Paths) > 0:
		return strings.Join(ev.Paths, " ")
	case ev.Path != "":
		return ev.Path
	default:
		return ""
	}
}

func mailEventDetail(m read.Mail, ev read.MailMoment) string {
	if msg, ok := m.Find(ev.MID); ok {
		subject := msg.Subject
		if strings.TrimSpace(subject) == "" {
			subject = "(no subject)"
		}
		return subject + " · from " + msg.From
	}
	return "message " + ev.MID
}

// reportDamage says what could not be read. It goes to stderr so stdout stays
// pipeable, and it is never silent: a view that quietly skips a broken file is
// a view that lies about the store.
func (a App) reportDamage(damage []read.Damage) {
	for _, d := range damage {
		a.note("%s: %s", d.Path, d.Why)
	}
}
