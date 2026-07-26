// Package notify tells agents when their membership of a task changes.
//
// Macmuffin does not reimplement mail. It shells out to `mailman send`, which
// is the whole coupling between the two tools — one exec, behind one interface,
// so every test runs against a recorder rather than a real binary.
//
// Delivery is journaled rather than fired and forgotten. The task event is
// written first, because membership is the fact and the mail is only the
// announcement; the notice is then queued, and only then sent. A Mailman that
// is missing, misconfigured, or momentarily broken therefore delays a
// notification rather than losing one, and never fails a membership change that
// has already happened.
package notify

import (
	"fmt"
	"os/exec"
	"strings"

	"orc/common/fault"
	"orc/common/user"
	"orc/macmuffin/internal/store"
	"orc/macmuffin/internal/task"
)

// Binary is the mail tool Macmuffin calls.
const Binary = "mailman"

// Run sends one message. It is the only thing this package does to the outside
// world, and it is an interface so a test never execs anything.
type Run func(args []string, stdin string) error

// Exec runs the real `mailman` binary, passing the body on standard input.
//
// The body goes on stdin rather than in argv because a task's name and an
// agent's reasoning can be long, and argv is both size-limited and visible in
// `ps` to everyone on the machine.
func Exec(args []string, stdin string) error {
	cmd := exec.Command(Binary, args...)
	cmd.Stdin = strings.NewReader(stdin)

	out, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("%s %s: %s", Binary, strings.Join(args, " "), detail)
	}
	return nil
}

// Courier queues and delivers notices.
type Courier struct {
	store *store.Store
	run   Run
}

// New builds a courier. A nil Run uses the real binary.
func New(s *store.Store, run Run) (Courier, error) {
	if s == nil {
		return Courier{}, fault.Internal{Where: "notify.New", Detail: "no store given"}
	}
	if run == nil {
		run = Exec
	}
	return Courier{store: s, run: run}, nil
}

// Joined announces that an agent was added to a task.
func (c Courier) Joined(t task.Task, by, who user.Name) error {
	return c.send(t, by, who,
		fmt.Sprintf("you are on %s", t.Name()),
		joinedBody(t, by, who))
}

// Assigned announces that an agent has been given a task to own.
func (c Courier) Assigned(t task.Task, by, who user.Name) error {
	return c.send(t, by, who,
		fmt.Sprintf("you own %s", t.Name()),
		assignedBody(t, by, who))
}

// Removed announces that an agent was taken off a task.
func (c Courier) Removed(t task.Task, by, who user.Name) error {
	return c.send(t, by, who,
		fmt.Sprintf("you are off %s", t.Name()),
		removedBody(t, by, who))
}

// send queues a notice and tries to deliver it.
//
// The caller is a recipient rather than a true copy, because `mailman send`
// takes one recipient list and has no separate cc field — `mailman cc` operates
// on conversations, not on outgoing mail. Both people get the message, which is
// what the reference asks for; only the header distinction is missing, and
// inventing a cc that Mailman does not have would be worse than saying so.
func (c Courier) send(t task.Task, by, who user.Name, subject, body string) error {
	to := []user.Name{who}
	if by.String() != who.String() {
		to = append(to, by)
	}

	queued, err := c.store.Queue(to, subject, body)
	if err != nil {
		return err
	}
	return c.deliver(queued)
}

// deliver attempts one notice, recording the outcome either way.
func (c Courier) deliver(n store.Notice) error {
	args := append([]string{"send", n.Subject}, user.Names(n.To)...)
	args = append(args, "-")

	if err := c.run(args, n.Body); err != nil {
		// Recorded, not returned: the membership change already happened, and
		// failing the command now would tell the caller their invite did not
		// work when it did.
		if markErr := c.store.Undelivered(n, err); markErr != nil {
			return markErr
		}
		return Undeliverable{Notice: n, Err: err}
	}
	return c.store.Delivered(n.ID)
}

// Undeliverable reports that a notice was queued but could not be sent. It is
// deliberately *not* a fault: the caller reports it as a warning and carries on,
// because the thing the notice announces has already happened.
type Undeliverable struct {
	Notice store.Notice
	Err    error
}

func (e Undeliverable) Error() string {
	return fmt.Sprintf("could not notify %s: %v", strings.Join(user.Names(e.Notice.To), ", "), e.Err)
}

func (e Undeliverable) Unwrap() error { return e.Err }

// Drain retries queued notices, and reports how many are still waiting.
//
// Every command calls this before doing its own work, so a notice that failed
// once is retried by whichever agent next touches the store — no daemon, no
// timer, and no notice that waits for the process that queued it to run again.
//
// A notice past the attempt limit is left alone and counted: retrying it
// forever would bury the real problem under noise, and `verify` is what
// surfaces it.
func (c Courier) Drain() (sent, waiting, stuck int, err error) {
	pending, err := c.store.Pending()
	if err != nil {
		return 0, 0, 0, err
	}

	for _, n := range pending {
		if n.Exhausted() {
			stuck++
			continue
		}
		if err := c.deliver(n); err != nil {
			var undeliverable Undeliverable
			if asUndeliverable(err, &undeliverable) {
				waiting++
				continue
			}
			return sent, waiting, stuck, err
		}
		sent++
	}
	return sent, waiting, stuck, nil
}

func asUndeliverable(err error, out *Undeliverable) bool {
	if e, ok := err.(Undeliverable); ok {
		*out = e
		return true
	}
	return false
}

// The notice bodies. They are markdown, since that is what Mailman carries, and
// they say what changed, who changed it, and what the recipient can do next —
// an announcement that leaves the reader looking things up is one they will
// stop reading.

func joinedBody(t task.Task, by, who user.Name) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s added you to **%s**.\n\n", by, t.Name())
	writeSummary(&b, t)
	fmt.Fprintf(&b, "\nSee it with `muff info %s`.\n", t.Name())
	if t.Scoped() {
		fmt.Fprintf(&b, "Editing is limited to the scope above while the task is in force.\n")
	}
	return b.String()
}

func assignedBody(t task.Task, by, who user.Name) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s assigned you **%s**. You own it.\n\n", by, t.Name())
	writeSummary(&b, t)
	fmt.Fprintf(&b, "\nSee it with `muff info %s`, and say how it is going with "+
		"`muff status %s <1..4>`.\n", t.Name(), t.Name())
	if t.Scoped() {
		fmt.Fprintf(&b, "Editing is limited to the scope above while the task is in force.\n")
	}
	return b.String()
}

func removedBody(t task.Task, by, who user.Name) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s removed you from **%s**.\n\n", by, t.Name())
	writeSummary(&b, t)
	fmt.Fprintf(&b, "\nYou can still read it with `muff info %s`.\n", t.Name())
	return b.String()
}

// writeSummary describes a task the way the card does, so a notice and a
// `muff info` never disagree about what the task is.
func writeSummary(b *strings.Builder, t task.Task) {
	fmt.Fprintf(b, "- priority %s, difficulty %s\n", t.Priority().Label(), t.Difficulty().Label())
	fmt.Fprintf(b, "- status: %s\n", t.Status())

	if owner, owned := t.Owner(); owned {
		fmt.Fprintf(b, "- owner: %s\n", owner)
	} else {
		fmt.Fprintf(b, "- owner: nobody yet\n")
	}

	if done, total := t.Progress(); total > 0 {
		fmt.Fprintf(b, "- subtasks: %d of %d done\n", done, total)
	}
	if t.Scoped() {
		fmt.Fprintf(b, "- scope: %s\n", strings.Join(t.Scope(), ", "))
	}
}
