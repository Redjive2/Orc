package server

import (
	"net/http"
	"strings"
	"time"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
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
		out = append(out, block{Machine: id, Identities: by})
	}
	s.ok(w, r, map[string]any{"window": window.String(), "machines": out})
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
