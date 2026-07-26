package server

import (
	"context"
	"net/http"
	"time"

	"orc/cq/internal/protocol"
)

// Upgrading the whole fleet, from one request.
//
// Two halves, because the two machines are reachable in opposite directions:
//
//   - **This machine** — the one serving the site — upgrades itself. That is a
//     local git pull, a local build, and a restart, and it is the half that needs
//     a supervisor: a process cannot exec its own replacement and still be there
//     to report on it.
//   - **Every agent machine** gets a queued action. The server cannot reach them —
//     that is the whole architecture — so each does the work on its next sync.
//
// The queue is what makes this survive the restart in the middle. The actions are
// on disk before the server goes down, so an agent that synced during the gap
// simply fails and retries, and one that had not synced yet finds its action
// waiting. Nothing is lost by the server being away; the mirror is minutes stale
// by design and a minute more is not a new kind of stale.
//
// The order is deliberate: queue the agents *first*, answer the caller, and only
// then restart. A server that restarted first would come back to a queue it had
// not written yet.

// restartGrace is how long the server waits after answering before it goes down.
//
// Long enough for the response to reach the browser and for the queue write to
// have been fsynced, short enough that nobody wonders whether the button worked.
// It is a delay rather than a synchronisation because the alternative — holding
// the request open across a restart — is a request that can never be answered.
const restartGrace = 2 * time.Second

type upgradeBody struct {
	// Machines names the agent machines to upgrade. Empty means every machine
	// that has ever synced, which is what the button in the browser sends.
	Machines []string `json:"machines,omitempty"`
	// Server says whether to upgrade the machine serving the site. Default true;
	// `false` is for upgrading the fleet without taking the site down.
	Server *bool `json:"server,omitempty"`
}

func (b *upgradeBody) Validate() error { return nil }

type upgradeView struct {
	Queued []protocol.MachineID `json:"queued"`
	// Server reports what the serving machine will do, which is a different thing
	// from what it has done: the restart happens after this answer is sent.
	Server     string `json:"server"`
	Restarting bool   `json:"restarting"`
}

// upgradeAll is `POST /api/v1/upgrade`.
func (s *Server) upgradeAll(w http.ResponseWriter, r *http.Request) {
	var body upgradeBody
	if err := decode(r, MaxRequestBytes, &body); err != nil {
		s.fail(w, r, err)
		return
	}

	// The agents first, so the queue is on disk before anything restarts.
	ids, err := s.machineIDs()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(body.Machines) > 0 {
		wanted := map[string]bool{}
		for _, m := range body.Machines {
			wanted[m] = true
		}
		var kept []protocol.MachineID
		for _, id := range ids {
			if wanted[string(id)] {
				kept = append(kept, id)
			}
		}
		ids = kept
	}

	view := upgradeView{Queued: []protocol.MachineID{}}
	for _, id := range ids {
		if _, err := s.state.Enqueue(id, protocol.OpUpgrade, protocol.Args{}, s.now()); err != nil {
			s.fail(w, r, serverSide(err))
			return
		}
		view.Queued = append(view.Queued, id)
		s.log.Info("upgrade queued", "machine", id)
	}
	s.events.Publish()

	// Then this machine. Whether it *can* is answered now rather than after the
	// caller has gone: a server with no checkout to build from should say so in
	// the response, not in a log nobody is reading.
	wantServer := body.Server == nil || *body.Server
	switch {
	case !wantServer:
		view.Server = "not asked for"
	case s.restart == nil:
		view.Server = "this server is not supervised, so it cannot restart itself; " +
			"run `cq serve` under a supervisor, or restart it by hand after the build"
	case s.upgrade.Source == "":
		view.Server = "this server has no checkout to build from; set $CQ_SOURCE"
	default:
		view.Server = "pulling, building, and restarting"
		view.Restarting = true
	}

	s.write(w, r, http.StatusAccepted, view)

	if view.Restarting {
		// Detached from the request. The response is already written, and this
		// outlives the handler on purpose — the whole point is that it ends with
		// the process going away.
		go s.upgradeSelf()
	}
}

// upgradeSelf pulls, builds, and asks the supervisor for a new process.
//
// It runs on its own goroutine, after the response, and it does the build *before*
// the restart. The other order looks equivalent and is not: restarting first would
// bring the old binary back up, and restarting after a failed build would bring
// nothing up at all. So a build that fails leaves the running server exactly as it
// was, which is the outcome an operator wants from a failed upgrade.
func (s *Server) upgradeSelf() {
	report, err := s.upgrade.Upgrade(context.Background())
	if err != nil {
		// Loudly, and then carry on serving. There is no way to reach the caller —
		// they have their 202 — so the log and the next `cq status` are where this
		// has to be visible.
		s.log.Error("upgrade failed; the server is still on the old build", "error", err)
		return
	}
	s.log.Info("upgraded", "before", report.Before, "after", report.After,
		"built", report.Built, "changed", report.Changed)

	// The grace is for the response, not for the build: by here the reply is long
	// gone, but a browser that is mid-poll deserves to finish rather than see a
	// connection drop it will report as an error.
	time.Sleep(restartGrace)
	s.log.Info("restarting into the new build")
	s.restart()
}
