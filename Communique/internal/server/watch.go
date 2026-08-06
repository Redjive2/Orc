package server

import (
	"net/http"
	"time"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
	"orc/cq/internal/store"
)

// Watching an agent from the browser.
//
// These two are not queued actions and that is the whole distinction worth
// drawing. Everything else the fleet screens do — employ, poke, refresh — is a
// thing that must happen *on the agent machine*, so it is queued, carried out on
// the next sync, and reported back. A watch changes nothing there. It is the
// server writing down that somebody is looking, and the agent reading that off
// the next response it happens to get.
//
// Which means it cannot fail on the machine, cannot be in doubt, and must not sit
// in a queue behind a poke: a lease that arrived four minutes late would be a
// lease for a pane that had already been closed.

// WatchEvery is how often a watched machine sends the session back.
//
// Three seconds. Fast enough that answering an agent feels like a conversation
// rather than a form, slow enough that the round trip — the narrow sync out, the
// poke applied, the reply mirrored back on the round after — stays under ten. It
// is not a terminal and no interval would make it one; what this buys is the
// difference between watching something happen and refreshing a page to find out
// whether it did.
const WatchEvery = 3 * time.Second

// takeWatch takes or renews the lease on one agent's session.
//
// The same call either way. A browser renews by asking again, so there is no
// state it has to remember and nothing to get wrong after a reload — see
// store.Watch.
func (s *Server) takeWatch(w http.ResponseWriter, r *http.Request) {
	var body fleetBody
	if err := decode(r, MaxRequestBytes, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	machine, err := s.machineOf(body.Machine)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The interval is the server's to choose, not the browser's. A client asking
	// for one would be a client that can ask for 50ms, and the floor would then be
	// the only thing between a page and a machine in a spin — worth having, but
	// not worth being the only defence.
	if err := s.state.Watch(machine, named(r, body), WatchEvery.String(), s.now()); err != nil {
		s.fail(w, r, err)
		return
	}
	s.write(w, r, http.StatusOK, watchView{
		Machine: machine, Identity: named(r, body),
		Every: WatchEvery.String(), Lease: store.WatchLease.String(),
	})
}

// dropWatch ends it, rather than waiting for the lease to lapse.
func (s *Server) dropWatch(w http.ResponseWriter, r *http.Request) {
	var body fleetBody
	if err := decode(r, MaxRequestBytes, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	machine, err := s.machineOf(body.Machine)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.state.Unwatch(machine); err != nil {
		s.fail(w, r, err)
		return
	}
	s.write(w, r, http.StatusOK, watchView{Machine: machine})
}

// watchView is what the browser is told back.
//
// The interval and the lease are in it because the pane has to say both: how
// often what it shows can change, and how long it has before it must renew. A
// client that had to hard-code either would be a client that goes quietly wrong
// the first time the server changes its mind.
type watchView struct {
	Machine  protocol.MachineID `json:"machine"`
	Identity string             `json:"identity,omitempty"`
	Every    string             `json:"every,omitempty"`
	Lease    string             `json:"lease,omitempty"`
}

// machineOf resolves the machine a request names, or the only one there is.
//
// A fleet on one machine is the ordinary case and naming it every time would be
// ceremony; two machines and no name is a genuine ambiguity, and guessing which
// one somebody meant is how a watch lands on the wrong fleet.
func (s *Server) machineOf(named string) (protocol.MachineID, error) {
	if named != "" {
		id := protocol.MachineID(named)
		if err := id.Validate(); err != nil {
			return "", fault.Usage{Reason: err.Error()}
		}
		return id, nil
	}
	machines, err := s.state.Machines()
	if err != nil {
		return "", err
	}
	if len(machines) != 1 {
		return "", fault.Usage{Reason: "name the machine: more than one has synced"}
	}
	return machines[0], nil
}
