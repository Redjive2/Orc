package cli

import (
	"fmt"
	"strings"
	"time"

	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/activity"
	"orc/orc/internal/render"
	"orc/orc/internal/store"
	"orc/orc/internal/style"
	"orc/orc/internal/view"
)

// What an agent is doing, in one word.
//
// Every screen that shows a fleet has been answering this from two booleans —
// employed, and a session id that may or may not be there — and getting it half
// right. "Employed and not running" is three different situations wearing one
// label: a start that is being paced after failing, a supervisor that was killed,
// and an agent nobody has employed at all. An operator reading a list needs them
// apart, and so does cq, which today infers what it can and shows the rest as a
// blank.
//
// So the fleet decides, once, here. It is a *reading* rather than a new fact:
// every input already exists and three commands already consult them — `orc wake`
// decides waiting and stuck on every pass, `orc tend` decides down, and `orc
// attach` draws the turn. What is new is that they agree, and that the answer
// travels.

// Activity is the state of one identity's session.
type Activity string

// The states, in the order a fleet is happiest to see them.
const (
	// Generating: a live session, mid-turn. It is working.
	Generating Activity = "generating"
	// Waiting: Claude has said its piece and is waiting to be spoken to. This is
	// the ordinary end of a turn and not a fault — it is what `orc wake` exists
	// to notice when it lasts.
	Waiting Activity = "waiting"
	// Stuck: waiting, and already woken once for this very silence. An agent that
	// does not move after a poke is not idle, and poking it again would bury the
	// problem under nudges.
	Stuck Activity = "stuck"
	// Down: employed, with no session. Something should be running and is not.
	Down Activity = "down"
	// Idle: not employed and not running, which is most identities most of the
	// time and is nothing to report.
	Idle Activity = "idle"
)

// Activities lists them, so a test can be total and a screen cannot invent one.
func Activities() []Activity {
	return []Activity{Generating, Waiting, Stuck, Down, Idle}
}

// Working reports whether the state is one where something is happening. It is
// what a summary counts, and what a colour keys off.
func (a Activity) Working() bool { return a == Generating }

// Wrong reports whether the state is one somebody has to do something about.
// Waiting is not: an agent that finished its turn is fine, and the wake cycle is
// what decides when that silence has gone on too long.
func (a Activity) Wrong() bool { return a == Stuck || a == Down }

// Doing is everything a screen needs about one identity, and no more.
type Doing struct {
	State Activity
	// Turn is the turn its session is on, zero when there is none.
	Turn int
	// Since is when it entered this state, as far as the feed can say. Zero when
	// nothing has happened yet — a session that has just started and said nothing
	// has been in this state since it started, and that is Started's job to say.
	Since time.Time
	// Rows are the last few things it did, oldest first.
	Rows []view.Row
	// Feed reports whether the event feed could be read at all. A screen showing
	// no rows for a session that is plainly running should say which of the two
	// it means.
	Feed bool
	// Why says what a state that needs explaining is about: the reason a start is
	// being paced, or how long a stuck agent has been silent.
	Why string
}

// MaxDoingRows is how much of the feed travels.
//
// Enough to see what an agent is up to — the current turn's tool calls — and few
// enough that a fleet of twenty does not put a megabyte through a sync. Anybody
// who wants the whole feed has `orc attach`, which is the tool for it.
const MaxDoingRows = 8

// doing reads one identity's state.
//
// Nothing here fails. Every input is optional in the honest sense — a feed that
// will not read, a session that ended between two lines of this function, a wake
// record that is not there — and the answer degrades a state at a time rather
// than refusing to describe the fleet.
func (s caller) doing(who user.Name) Doing {
	got := Doing{State: Idle}

	state, live, err := s.store.Session(who)
	if err != nil || !live {
		// Not running. Whether that is a problem is the worklist's answer, not
		// this one's.
		if identity, err := s.store.Identity(who); err == nil && identity.Employed() {
			got.State = Down
			if due, left, tried := s.store.StartDue(who); !due {
				got.Why = fmt.Sprintf("start failed %d time%s; the next attempt is in %s",
					tried.Failures, plural(tried.Failures), round(left))
			} else if tried.Failures > 0 {
				got.Why = tried.Why
			}
		}
		return got
	}

	feed, ferr := view.Load(s.store.EventsPath(who), who)
	got.Feed = ferr == nil
	got.Turn = feed.Turn
	if n := len(feed.Rows); n > 0 {
		if n > MaxDoingRows {
			got.Rows = feed.Rows[n-MaxDoingRows:]
		} else {
			got.Rows = feed.Rows
		}
		got.Since = feed.Rows[n-1].At
	} else if at, err := state.StartedAt(); err == nil {
		// Nothing has happened yet, so it has been in this state since it began.
		got.Since = at
	}

	// Through `silence`, which is the wake cycle's own reading, rather than beside
	// it. The mark it returns is the string a wake is recorded under, so asking it
	// here is what makes "stuck" mean the same thing on this screen as it does in
	// the cycle that put the agent there. Two functions computing that separately
	// would agree until the day one of them was fixed.
	started, _ := state.StartedAt()
	mark, quiet, waiting := silence(feed, started, s.store.Now())
	if !waiting {
		got.State = Generating
		return got
	}

	got.State = Waiting
	// Stuck is waiting that a wake has already been spent on. The record is keyed
	// by session, so a refresh starts the reckoning over — which is right: a new
	// conversation has not been woken.
	if was, marked := s.store.Woken(who, state.ID); marked && was == mark {
		got.State = Stuck
		got.Why = "silent for " + round(quiet) + " since it was woken"
	}
	return got
}

// --- the rollup -----------------------------------------------------------

// advanceActivity reads whatever is new in an identity's transcript into its
// rollup, and reports whether it found anything.
//
// Incremental by construction: the cursor says where the last read stopped, and a
// transcript that has not grown costs a stat and nothing else. That is what lets
// `tend` do this on every pass — and `tend` runs under almost every command, which
// is what makes the measurement continuous without a daemon.
//
// Nothing here fails a caller. A transcript that will not read, a session that has
// no feed, a rollup that will not write: each costs the measurement and none of
// them costs the command that happened to be running.
func (s caller) advanceActivity(who user.Name) (activity.Reading, bool) {
	feed, err := view.Load(s.store.EventsPath(who), who)
	if err != nil || feed.Transcript == "" || feed.ID == "" {
		// No feed, or a feed that never named a transcript. There is nothing to
		// read and that is not a fault: a session Claude has not written to yet is
		// an ordinary state a second old.
		return activity.Reading{}, false
	}

	from, _ := s.store.ActivityRollup(who)
	if from.Session != feed.ID {
		// A new conversation: its own file, read from the beginning. Starting at
		// the previous session's offset would skip exactly as many bytes as the
		// last conversation was long.
		from = store.Rollup{Session: feed.ID}
	}

	got, err := activity.Read(feed.Transcript, feed.ID, from.Cursor)
	if err != nil {
		return activity.Reading{}, false
	}
	if len(got.Buckets) == 0 && got.Cursor.Offset == from.Cursor.Offset {
		return got, false
	}
	if err := s.store.RecordActivity(who, got.Buckets, store.Rollup{Session: feed.ID, Cursor: got.Cursor}); err != nil {
		return got, false
	}
	return got, len(got.Buckets) > 0
}

// activityReport is `orc activity [<identity>] [--since <dur>] [--json]`.
//
// A read that also advances the rollup, for the reason `orc spend` was given in the
// plan: the numbers somebody is asking for are the numbers worth bringing up to
// date first, and a report that showed yesterday's figures because nothing had run
// `tend` would be answering a question nobody asked.
//
// Not gated. Reading what a fleet you are in has done is not a privilege, and the
// rollup it writes is a measurement rather than a policy.
func (a App) activity(args []string) error {
	var window string
	var asJSON bool
	rest, err := flagged(args, options{
		values:   map[string]*string{"--since": &window},
		switches: map[string]*bool{"--json": &asJSON},
	})
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return fault.Usage{Reason: "activity takes one identity, or none for the whole fleet"}
	}

	since := DefaultWindow
	if strings.TrimSpace(window) != "" {
		got, err := time.ParseDuration(window)
		if err != nil {
			return fault.Usage{Reason: fmt.Sprintf("--since takes a duration like 24h: %v", err)}
		}
		if got <= 0 {
			return fault.Usage{Reason: "--since takes a window with something in it, like 24h"}
		}
		since = got
	}

	s, err := a.begin()
	if err != nil {
		return err
	}

	targets := s.fleet.Subtree(s.who)
	if len(rest) == 1 {
		who, err := user.Parse(rest[0])
		if err != nil {
			return err
		}
		if who.String() != s.who.String() && !s.fleet.Controls(s.who, who) {
			// The roster rule, as everywhere: somebody outside your branch is
			// reported as missing rather than as forbidden.
			return fault.NotFound{Target: who.String()}
		}
		targets = []user.Name{who}
	}

	from := s.store.Now().Add(-since)
	rows := make([]activityRow, 0, len(targets))
	for _, who := range targets {
		s.advanceActivity(who)

		buckets, err := s.store.Activity(who, from)
		if err != nil {
			a.note("%s: its activity could not be read: %v", who, err)
			continue
		}
		row := activityRow{Name: who, Doing: s.doing(who)}
		for _, b := range buckets {
			row.Turns += b.Turns
			row.Tokens.Add(b.Tokens)
			row.Files.Add(b.Files)
		}
		rows = append(rows, row)
	}

	if asJSON {
		return a.emitJSON(activityJSON(rows, since))
	}
	return a.drawActivity(rows, since)
}

// DefaultWindow is what `orc activity` reports on when nobody says.
//
// A day, because the question is almost always "what happened while I was not
// looking" and almost never "what happened this minute" — the live half of the
// screen answers that already.
const DefaultWindow = 24 * time.Hour

// activityRow is one identity's line.
type activityRow struct {
	Name   user.Name
	Doing  Doing
	Turns  int
	Tokens activity.Tokens
	Files  activity.Files
}

// drawActivity is the table.
//
// Six columns and no more: what it is doing now, then what it has done — turns,
// what it cost, and what it touched. The two token figures are kept apart for the
// reason the reader keeps them apart: on a real session they differ by five orders
// of magnitude, and one "tokens" column would be a cache-read column wearing a
// general name.
func (a App) drawActivity(rows []activityRow, since time.Duration) error {
	drawn := make([][]render.Cell, 0, len(rows))
	var turns int
	var tokens activity.Tokens
	var files activity.Files

	for _, row := range rows {
		turns += row.Turns
		tokens.Add(row.Tokens)
		files.Add(row.Files)

		drawn = append(drawn, []render.Cell{
			render.Painted(row.Name.String(), style.Palette.Identity),
			a.activityCell(row.Doing.State),
			render.Text(count(row.Turns)),
			render.Painted(count(row.Tokens.New()), style.Palette.Authority),
			render.Painted(count(row.Tokens.CacheRead), style.Palette.Muted),
			render.Text(fmt.Sprintf("%d read · %d written", row.Files.Reads, row.Files.Edits+row.Files.Writes)),
			render.Text(fmt.Sprintf("+%s/-%s", count(row.Files.Added+row.Files.WriteLines), count(row.Files.Removed))),
		})
	}

	table := render.Table{
		Title: "activity",
		Note: fmt.Sprintf("the last %s · %s turns · %s new tokens · %s cached",
			round(since), count(turns), count(tokens.New()), count(tokens.CacheRead)),
		Columns: []render.Column{
			{Header: "identity", Align: render.Left, Grow: true, Min: 12},
			{Header: "doing", Align: render.Left, Min: 10},
			{Header: "turns", Align: render.Right},
			{Header: "new", Align: render.Right, Min: 9},
			{Header: "cached", Align: render.Right, Min: 9},
			{Header: "files", Align: render.Left, Min: 16},
			{Header: "lines", Align: render.Right, Min: 12},
		},
		Rows:  drawn,
		Empty: "nobody below you",
		// Said rather than implied: a fleet whose figures are all zero should read
		// as one nobody has measured, not as one that has done nothing.
		Footer: []string{
			"new = input + output + cache writes; cached is what was read back",
			"lines come from claude's transcript and are missing where it could not be read",
		},
	}

	out, err := render.DrawTable(table, a.out, a.width())
	if err != nil {
		return err
	}
	return a.write(out)
}

// activityCell paints a state by what it demands of a reader.
func (a App) activityCell(state Activity) render.Cell {
	switch {
	case state.Wrong():
		return render.Painted(string(state), style.Palette.Warn)
	case state.Working():
		return render.Painted(string(state), style.Palette.Good)
	default:
		return render.Painted(string(state), style.Palette.Muted)
	}
}

// count renders a number with thin separators, because these are the figures
// nobody can read at a glance otherwise: 879281631 and 87928163 look alike.
func count[N int | int64](n N) string {
	digits := fmt.Sprintf("%d", n)
	if len(digits) < 5 {
		return digits
	}
	var out []byte
	for i, d := range []byte(digits) {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out = append(out, ' ')
		}
		out = append(out, d)
	}
	return string(out)
}

// activityJSON is the shape `--json` prints: the same figures, unrounded and
// unpainted, for cq and for anything else that would rather have the numbers.
type jsonActivity struct {
	Window     string            `json:"window"`
	Identities []jsonActivityRow `json:"identities"`
}

type jsonActivityRow struct {
	Name   string          `json:"name"`
	State  string          `json:"state"`
	Turn   int             `json:"turn,omitempty"`
	Turns  int             `json:"turns"`
	Tokens activity.Tokens `json:"tokens,omitzero"`
	Files  activity.Files  `json:"files,omitzero"`
}

func activityJSON(rows []activityRow, since time.Duration) jsonActivity {
	out := jsonActivity{Window: since.String(), Identities: []jsonActivityRow{}}
	for _, row := range rows {
		out.Identities = append(out.Identities, jsonActivityRow{
			Name:   row.Name.String(),
			State:  string(row.Doing.State),
			Turn:   row.Doing.Turn,
			Turns:  row.Turns,
			Tokens: row.Tokens,
			Files:  row.Files,
		})
	}
	return out
}
