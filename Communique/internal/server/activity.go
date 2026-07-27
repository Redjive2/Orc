package server

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
	"orc/cq/internal/store"
)

// The series, read back.
//
// It is the one thing here that is not in a snapshot. Everything else the browser
// draws arrives whole on every sync and is replaced whole; a rate is a fact about
// history, the mirror is where history accumulates, and history does not fit in a
// snapshot. So it has a route of its own.

// DefaultActivityWindow is what `GET /api/v1/activity` reports on when nobody says.
//
// Two days, which is the window a person opening a phone actually asks about —
// "what happened overnight" — and small enough that the ordinary request is a few
// hundred short objects rather than a year of them.
const DefaultActivityWindow = 48 * time.Hour

// MaxActivityWindow bounds what can be asked for at once, because the series is a
// year deep and a browser that asked for all of it would be handed all of it.
const MaxActivityWindow = 90 * 24 * time.Hour

// activity answers with one series per agent, per machine.
//
// Grouped by identity rather than returned flat: every screen that uses this draws
// one line per agent, and a flat list would make the browser do the grouping — which
// is the sort of arithmetic that ends up done differently in two places.
func (s *Server) activity(w http.ResponseWriter, r *http.Request) {
	window := DefaultActivityWindow
	if raw := r.URL.Query().Get("since"); raw != "" {
		got, err := time.ParseDuration(raw)
		if err != nil {
			s.fail(w, r, fault.Field("activity", "since", "%q is not a duration like 24h", raw))
			return
		}
		if got <= 0 || got > MaxActivityWindow {
			s.fail(w, r, fault.Field("activity", "since",
				"a window has to be more than nothing and no more than %s", MaxActivityWindow))
			return
		}
		window = got
	}

	period := periodFor(window)

	ids, err := s.machineIDs()
	if err != nil {
		s.fail(w, r, err)
		return
	}

	type block struct {
		Machine    protocol.MachineID                   `json:"machine"`
		Identities map[string][]protocol.ActivityBucket `json:"identities"`
	}
	out := []block{}
	since := time.Now().UTC().Add(-window)
	for _, id := range ids {
		by, err := s.state.ActivityByIdentity(id, since)
		if err != nil {
			// One unreadable machine is not a reason to answer with nothing: a
			// fleet of three should still draw the two that read.
			s.log.Warn("skipping an unreadable series", "machine", id, "error", err)
			continue
		}
		if len(by) == 0 {
			continue
		}
		for name, buckets := range by {
			by[name] = coarsen(buckets, period)
		}
		out = append(out, block{Machine: id, Identities: by})
	}
	s.ok(w, r, map[string]any{
		"window": window.String(),
		// The period the browser is being handed, so it can lay out a continuous
		// axis rather than infer one from the gaps. Two agents can go quiet for
		// different stretches, and a chart that inferred its spacing from the
		// buckets it happened to receive would draw a busy hour and a quiet
		// fortnight at the same width.
		"period":   period.String(),
		"machines": out,
	})
}

// TargetBars is how many columns a chart is aimed at.
//
// The period is chosen from the window to hit roughly this, rather than the window
// being drawn at whatever the data was recorded at. Recording resolution and display
// resolution are different questions: an hour of minutes is sixty honest bars, and a
// month of them is forty thousand values crushed into sixty pixels — the same chart
// asking two different questions.
const TargetBars = 80

// periods are the widths a chart is allowed to use, smallest first.
//
// Round numbers rather than window/TargetBars exactly, because the axis is read by a
// person: "every 5 minutes" is a spacing somebody can hold in their head while they
// look at it, and "every 4m37s" is arithmetic they have to do for every bar.
var periods = []time.Duration{
	time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute,
	15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour,
	3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour,
}

// periodFor picks the width to draw a window at.
//
// The smallest round period that keeps the chart near TargetBars, floored at what the
// data can actually support: past FineWindow the mirror holds hours, and a window
// reaching further drawn at ten minutes would put a whole hour's work in the first
// slot of each hour and nothing in the other five — a picture of the storage rather
// than of the work.
func periodFor(window time.Duration) time.Duration {
	floor := time.Duration(0)
	if window > store.FineWindow {
		floor = time.Hour
	}
	want := window / TargetBars
	for _, p := range periods {
		if p >= want && p >= floor {
			return p
		}
	}
	return periods[len(periods)-1]
}

// coarsen folds buckets into a wider period.
//
// The same fold the mirror performs when it compacts and the agent machine performs
// before it sends: re-key the time and add. Totals only ever grow and adding is how
// they merge, so a chart at five minutes and a chart at an hour are two views of one
// number rather than two measurements.
func coarsen(buckets []protocol.ActivityBucket, period time.Duration) []protocol.ActivityBucket {
	if period <= 0 {
		return buckets
	}
	byKey := map[string]*protocol.ActivityBucket{}
	order := []string{}
	for _, b := range buckets {
		at, err := time.Parse(time.RFC3339Nano, b.At)
		if err != nil {
			// A time this build cannot read is carried through rather than
			// dropped: one bar in the wrong slot beats a hole nobody can explain.
			at = time.Time{}
		} else {
			b.At = at.UTC().Truncate(period).Format(time.RFC3339Nano)
		}
		key := b.Key()
		into, ok := byKey[key]
		if !ok {
			into = &protocol.ActivityBucket{At: b.At, Model: b.Model, Effort: b.Effort}
			byKey[key] = into
			order = append(order, key)
		}
		into.Turns += b.Turns
		into.Tokens.Add(b.Tokens)
		into.Files.Add(b.Files)
	}
	sort.Strings(order)
	out := make([]protocol.ActivityBucket, 0, len(order))
	for _, key := range order {
		out = append(out, *byKey[key])
	}
	return out
}

// paceBody is what the form posts. Its own type because `decode` validates what it
// decodes, and the fleet's body carries operands this route has no use for.
type paceBody struct {
	Watch string `json:"watch"`
}

// Validate is the shape check; the value is checked below, where the floor and the
// ceiling can be explained rather than only enforced.
func (b *paceBody) Validate() error { return nil }

// syncPace reads and writes how often the mirror is synced.
//
// Not a queued action, unlike everything else the fleet tab changes — and the
// difference is the point. A queued action is a thing an *agent machine* does; this
// is a setting the server holds, and queueing it would send it to the machine that
// cannot act on it. It takes effect on each watcher's next round, when it asks.
func (s *Server) syncPace(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.ok(w, r, map[string]any{"watch": s.state.SyncPace(), "floor": protocol.MinSyncPace.String()})
		return
	}

	var body paceBody
	if err := decode(r, MaxRequestBytes, &body); err != nil {
		s.fail(w, r, err)
		return
	}

	watch := strings.TrimSpace(body.Watch)
	if watch != "" {
		got, err := time.ParseDuration(watch)
		if err != nil {
			s.fail(w, r, fault.Field("pace", "watch", "%q is not a duration like 30s", body.Watch))
			return
		}
		if got < protocol.MinSyncPace {
			s.fail(w, r, fault.Field("pace", "watch",
				"%s is under %s, which is a busy-wait rather than a mirror", watch, protocol.MinSyncPace))
			return
		}
		if got > protocol.MaxPace {
			s.fail(w, r, fault.Field("pace", "watch",
				"%s is longer than %s, which is not a cycle", watch, protocol.MaxPace))
			return
		}
		watch = got.String()
	}

	if err := s.state.SetSyncPace(watch); err != nil {
		s.fail(w, r, err)
		return
	}
	// Said rather than implied: nothing changes on any machine until its watcher
	// next asks, and a form that reported "done" would be claiming otherwise.
	s.ok(w, r, map[string]any{
		"watch": watch,
		"note":  "each machine picks this up on its next sync",
	})
}
