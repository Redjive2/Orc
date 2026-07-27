package cli

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/pty"
	"orc/orc/internal/render"
	"orc/orc/internal/session"
	"orc/orc/internal/view"
)

// This file is the attach surface: the raw proxy that exists now, and the seam the
// clean view from Plan.md §6.2 lands in.
//
// It is a file of its own so that the view can be built without touching the
// worklist verbs beside it — `dial`, `short`, and the rest live in liveness.go and
// are called from here, never edited here.

// attach connects to a session.
//
// Without --direct this is Orc's own view (Plan.md §6.2): a rendering of Orc's event
// journal with the transcript as the source of prose. It is a reader — nothing in it
// writes to the session except the composed buffer, and only when ^S says so — which
// is what makes it safe to watch a working agent with.
func (a App) attach(args []string) error {
	// `--direct` is the default, and `--view` asks for the composed pane.
	//
	// It was the other way round, and that was wrong about what somebody attaches
	// *for*. The composed pane is built from the session's transcript and the hook's
	// feed, so it shows what an agent has said and done — but not what its terminal
	// is drawing. Anything Claude renders in its own interface, and anything written
	// before the first event, is simply not there: the pane is often blank while the
	// session is perfectly busy, which reads as an attach that does not work.
	//
	// Attaching is the thing an operator reaches for when they want to *see*. The
	// mode that shows everything is the one that should need no flag.
	var direct, composed bool
	rest, err := flagged(args, options{switches: map[string]*bool{
		"--direct": &direct, "--view": &composed,
	}})
	if err != nil {
		return err
	}
	if err := exactly(rest, 1, "attach takes one identity"); err != nil {
		return err
	}
	s, err := a.begin()
	if err != nil {
		return err
	}
	if err := s.mayRunVerb("attach"); err != nil {
		return err
	}

	who, err := user.Parse(rest[0])
	if err != nil {
		return err
	}
	if who.String() != s.who.String() {
		if err := s.controls(who, "attach to"); err != nil {
			return err
		}
	}

	if composed && direct {
		return fault.Usage{Reason: "attach shows the terminal or the composed pane; name one"}
	}
	if composed {
		return a.cleanView(s, who)
	}
	return a.proxy(s, who)
}

// The detach sequence: Ctrl-\ then one of d, q, or `.`.
//
// A prefix is unavoidable here. Every single key worth pressing belongs to the
// session — ^C interrupts the agent, ^D ends its input, ^] is used by its own
// interface — so the only safe shape is a prefix nobody types by accident followed
// by a letter.
//
// Three letters rather than one because the prefix is already the hard part. `^\` is
// awkward on a US keyboard and worse on the layouts where a backslash needs AltGr:
// somebody who has managed to press it should not then also have to remember which
// letter. `d` for detach, `q` for the people whose fingers say quit, and `.` because
// that is how ssh's own escape ends a session and the muscle memory is already
// there.
const detachPrefix = 0x1c // ^\

// detachKeys are what may follow the prefix.
var detachKeys = []byte{'d', 'q', '.'}

func isDetachKey(b byte) bool {
	for _, k := range detachKeys {
		if b == k {
			return true
		}
	}
	return false
}

// proxy is `attach --direct`: the operator's terminal, wired to the session.
func (a App) proxy(s caller, who user.Name) error {
	// The terminal is checked before the session is dialled. Both orders work, but
	// this one gives the more useful error: "this needs a terminal" is about the
	// caller's own invocation, and hearing about an unreachable session first sends
	// somebody to debug the fleet when the problem is that they piped the command.
	stdin, ok := a.Stdin.(*os.File)
	if !ok || !a.Terminal {
		return fault.Usage{Reason: "attach --direct needs a terminal; it hands yours to the session"}
	}

	client, err := a.dial(s, who)
	if err != nil {
		return err
	}

	size, err := pty.Size(stdin)
	if err != nil {
		size = pty.Sane()
	}
	conn, err := client.Attach(size)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	// Raw mode from here, and restored on *every* path out — including a signal.
	// A command that exited without restoring would leave the operator's shell with
	// no echo, which looks exactly like a hung machine.
	restore, err := pty.MakeRaw(stdin)
	if err != nil {
		return err
	}
	defer func() { _ = restore.Restore() }()

	signals := make(chan os.Signal, 4)
	signal.Notify(signals, attachSignals()...)
	defer signal.Stop(signals)

	// Said in full, and said as two keystrokes rather than as a glyph pair: "^\ d"
	// reads as one thing to press, and somebody who tries to press it as one thing
	// concludes the detach does not work. The session keeps running either way, and
	// saying so is what stops an operator sitting in an attach they do not want to
	// be in because they are afraid leaving will stop the agent.
	if _, err := io.WriteString(a.Stdout, fmt.Sprintf(
		"\r\n[orc: attached to %s — to leave, press Ctrl-\\ and then d. the session keeps running.]\r\n",
		who)); err != nil {
		return err
	}

	done := make(chan error, 2)

	// The session's output to the terminal.
	go func() {
		_, err := io.Copy(a.Stdout, conn)
		done <- err
	}()

	// The terminal's keystrokes to the session, watching for the detach sequence.
	go func() {
		buf := make([]byte, 4<<10)
		armed := false
		for {
			n, err := stdin.Read(buf)
			if n > 0 {
				out, detach := scanDetach(buf[:n], &armed)
				if len(out) > 0 {
					if _, err := conn.Write(out); err != nil {
						done <- err
						return
					}
				}
				if detach {
					done <- nil
					return
				}
			}
			if err != nil {
				done <- err
				return
			}
		}
	}()

	for {
		select {
		case err := <-done:
			_ = restore.Restore()
			_, _ = io.WriteString(a.Stdout, fmt.Sprintf("\r\n[orc: detached from %s — it is still running]\r\n", who))
			if err != nil && err != io.EOF {
				return nil // a closed stream is how detaching looks; it is not a failure
			}
			return nil

		case sig := <-signals:
			if !resized(sig) {
				_ = restore.Restore()
				return nil
			}
			// The size travels on its own connection rather than inside the
			// stream: a resize arriving mid-escape-sequence would corrupt the
			// screen it was trying to fix.
			if size, err := pty.Size(stdin); err == nil {
				_ = client.Resize(size)
			}
		}
	}
}

// scanDetach forwards keystrokes and reports the detach sequence.
//
// The prefix is held rather than forwarded until the next key decides what it was,
// so `^\` followed by anything else reaches the session intact — a caller that
// dropped it would make one key silently unusable inside a session.
func scanDetach(in []byte, armed *bool) (out []byte, detach bool) {
	for _, b := range in {
		if *armed {
			*armed = false
			if isDetachKey(b) {
				return out, true
			}
			out = append(out, detachPrefix, b)
			continue
		}
		if b == detachPrefix {
			*armed = true
			continue
		}
		out = append(out, b)
	}
	return out, false
}

// The clean view's refresh rate.
//
// The feed is a file an agent appends to, so this polls rather than watches: a
// fifth of a second is under the threshold where a screen feels stale and far above
// the cost of stat-ing one file. Watching the inode would mean a platform-specific
// notify API for no gain at this rate.
const (
	watchInterval = 200 * time.Millisecond
	factsInterval = 15 * time.Second
)

// watch is `orc attach` without --direct: Orc's own view.
//
// The whole screen is drawn from a model built by internal/view, which is a pure
// function of the feed's bytes. This function is the impure half — the terminal, the
// clock, and the one socket write that a send is — and it is deliberately thin, so
// that what is worth testing is testable without a pty.
func (a App) cleanView(s caller, who user.Name) error {
	stdin, ok := a.Stdin.(*os.File)
	if !ok || !a.Terminal {
		// Both alternatives are named here rather than left to the help screen.
		// A usage error no longer drags the whole screen behind it, so a refusal
		// that does not say what to do instead does not say it anywhere.
		return fault.Usage{Reason: fmt.Sprintf(
			"attach needs a terminal to draw in; %s prints the same facts once and exits, and %s hands the terminal straight to the session",
			a.err.Command("orc status "+who.String()), a.err.Flag("--direct"))}
	}

	// Dialled before the screen is cleared: an unreachable session should leave the
	// operator's terminal exactly as it found it, with the reason on it.
	client, err := a.dial(s, who)
	if err != nil {
		return err
	}

	restore, err := pty.MakeRaw(stdin)
	if err != nil {
		return err
	}
	defer func() { _ = restore.Restore() }()

	signals := make(chan os.Signal, 4)
	signal.Notify(signals, attachSignals()...)
	defer signal.Stop(signals)

	screen := &watcher{
		app:    a,
		who:    who,
		feed:   s.store.EventsPath(who),
		facts:  a.factsFor(s, who),
		width:  a.Width,
		height: render.PaneHeight,
	}
	if size, err := pty.Size(stdin); err == nil {
		screen.resize(size)
	}

	keys := make(chan []byte, 8)
	go readKeys(stdin, keys)

	tick := time.NewTicker(watchInterval)
	defer tick.Stop()
	refreshFacts := time.NewTicker(factsInterval)
	defer refreshFacts.Stop()

	if err := screen.draw(); err != nil {
		return err
	}

	for {
		select {
		case <-tick.C:
			if err := screen.draw(); err != nil {
				return err
			}

		case <-refreshFacts.C:
			screen.facts = a.factsFor(s, who)

		case in, ok := <-keys:
			if !ok {
				return screen.leave(restore)
			}
			switch screen.compose.Feed(in) {
			case view.Send:
				screen.send(client)
			case view.Detach:
				if screen.confirmLeave() {
					return screen.leave(restore)
				}
			case view.Direct:
				// Hand over the real terminal. The clean view's own raw mode is
				// released first: two things owning one terminal is how a shell
				// ends up with no echo.
				_ = restore.Restore()
				screen.clear()
				return a.proxy(s, who)
			case view.Refresh:
				screen.notice, screen.alarm = "", false
			}
			if err := screen.draw(); err != nil {
				return err
			}

		case sig := <-signals:
			if !resized(sig) {
				return screen.leave(restore)
			}
			if size, err := pty.Size(stdin); err == nil {
				screen.resize(size)
			}
			if err := screen.draw(); err != nil {
				return err
			}
		}
	}
}

// watcher is the view's mutable state: what is on the screen, and what has been
// typed and not sent.
type watcher struct {
	app    App
	who    user.Name
	feed   string
	facts  view.Facts
	width  int
	height int

	compose view.Composer
	notice  string
	alarm   bool
	warned  bool // a detach with unsent text has been warned about once
	// waiting is what the last draw saw: whether the session was at its prompt or
	// part-way through a turn. `send` reads it to say what it sent into.
	waiting bool
}

func (w *watcher) resize(size pty.WinSize) {
	if size.Cols > 0 {
		w.width = int(size.Cols)
	}
	if size.Rows > 0 {
		w.height = int(size.Rows)
	}
}

// draw renders the whole screen and puts it on the terminal.
//
// The screen is redrawn in full rather than patched. A pane is small, a redraw is one
// write, and diffing a screen against its previous state is where TUIs acquire the
// bugs that show half of one frame and half of another.
func (w *watcher) draw() error {
	session, err := view.Load(w.feed, w.who)
	if err != nil {
		// A feed that will not read is worth saying on the screen rather than
		// tearing the view down: the session is still running, and the operator
		// may want --direct.
		session = view.Session{Identity: w.who}
		w.notice, w.alarm = "the event feed will not read — ^] for the session itself", true
	}

	// Remembered for `send`, which has to say what it sent *into*: typing at a
	// waiting prompt and typing at an agent that is mid-turn are the same bytes and
	// two different outcomes.
	w.waiting = session.Waiting

	prose, available := view.ReadProse(session.Transcript)

	out, err := render.DrawPane(render.Screen{
		Session:        session,
		Facts:          w.facts,
		Prose:          prose,
		ProseAvailable: available,
		Compose:        w.compose.Lines(),
		ComposeFull:    w.compose.Full(),
		Notice:         w.notice,
		Alarm:          w.alarm,
	}, w.app.out, w.width, w.height)
	if err != nil {
		return err
	}

	// Home the cursor and clear, then draw. Every line ends \r\n because the
	// terminal is raw: without the carriage return each row would start where the
	// last one ended.
	//
	// Except the last one, which ends nothing. The pane is exactly as tall as the
	// terminal, so a newline after its final row asks for a row that is not there
	// and the terminal makes one by scrolling everything up — taking the header off
	// the top of the screen. The next draw then homes the cursor to a screen that
	// has already moved, and the top of the pane is a line of the previous frame.
	// One byte, and it is the difference between a stable screen and one that eats
	// its own header.
	body := strings.ReplaceAll(strings.TrimSuffix(out, "\n"), "\n", "\r\n")
	_, err = io.WriteString(w.app.Stdout, "\x1b[H\x1b[2J"+body)
	return err
}

// send delivers the composed buffer, on the same path `poke` uses.
//
// One way for text to reach a session means one thing to test and one place for
// bracketed paste to be got right, which is what keeps a multi-line message from
// being submitted a line at a time by the TUI on the other end.
func (w *watcher) send(client *session.Client) {
	if w.compose.Empty() {
		w.notice, w.alarm = "nothing to send", false
		return
	}

	text := w.compose.Text()
	if err := client.Poke(text); err != nil {
		// The buffer is kept: a failed send that also lost the message would be
		// the worst outcome of the two.
		w.notice, w.alarm = "could not send — the text is still here", true
		return
	}
	w.compose.Clear()
	w.warned = false

	// What "sent" means depends on what it was sent into, and the difference is the
	// one an operator cannot see from here.
	//
	// The bytes are typed into the session's terminal either way. If it was waiting,
	// that is a message it takes now. If it was mid-turn, the text sits in its input
	// until the turn ends — so nothing appears to happen, and somebody who did not
	// know that concludes the send was lost and tries again, which is how two copies
	// of a message end up queued.
	if w.waiting {
		w.notice, w.alarm = "sent", false
		return
	}
	w.notice, w.alarm = "sent — it is mid-turn, so it will be read when that finishes", false
}

// confirmLeave reports whether detaching should go ahead.
//
// Unsent text warns once and detaches on the second try. Discarding it silently would
// throw away the thing the composed buffer exists to protect; refusing outright would
// make ^\ d unreliable, which is worse for a key somebody presses to get out.
func (w *watcher) confirmLeave() bool {
	if w.compose.Empty() || w.warned {
		return true
	}
	w.warned = true
	w.notice, w.alarm = "unsent text — ^\\ d again to discard it, ^S to send", true
	return false
}

func (w *watcher) clear() {
	_, _ = io.WriteString(w.app.Stdout, "\x1b[H\x1b[2J")
}

// leave restores the terminal and says what happened to the session.
//
// A nil restorer means there is nothing to put back, which is the case in a test and
// would otherwise mean handing a zero Restorer to tcsetattr on whatever fd 0 happens
// to be.
func (w *watcher) leave(restore *pty.Restorer) error {
	if restore != nil {
		_ = restore.Restore()
	}
	w.clear()

	if !w.compose.Empty() {
		w.app.note("what you had typed was not sent: %s", quoteShort(w.compose.Text()))
	}
	return w.app.say(fmt.Sprintf("detached from %s — it is still running", w.app.out.Identity(w.who.String())))
}

// factsFor gathers what the pane draws around the feed.
//
// The fleet's own numbers come from the fleet, which is already open. Mail and tasks
// come from the other tools, run as the identity — Orc mints those credentials, so it
// is the one tool that can ask on somebody else's behalf without being handed a
// secret it did not already hold. Every part of it degrades: a footer is not worth
// failing an attach for.
func (a App) factsFor(s caller, who user.Name) view.Facts {
	got := view.NoFacts()

	if target, err := s.fleet.Identity(who); err == nil {
		got.Role = target.Role().String()
		got.Model = target.Model().String()
		got.Effort = target.Effort().String()
		got.Load = target.Load()
	}
	// The effective authority, not the asked one: a role that wants 80 under a boss
	// with 60 has 60, and the header should say what the agent can actually do.
	effective, _ := s.fleet.Authority(who)
	got.Authority = effective.Int()

	key, err := s.store.Key(who)
	if err != nil {
		return got
	}
	return view.Ask(view.Exec([]string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"ORC_USER=" + who.String(),
		"ORC_KEY=" + key,
		"ORC_AGENT=1", // no colour: this output is parsed, not read
	}), got)
}

// readKeys pumps the terminal into a channel, closing it when the terminal ends.
//
// It is a goroutine because a blocking read cannot be selected on, and the loop must
// stay responsive to the clock and to a resize while nobody is typing.
func readKeys(in *os.File, out chan<- []byte) {
	defer close(out)

	buf := make([]byte, 4<<10)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			out <- chunk
		}
		if err != nil {
			return
		}
	}
}
