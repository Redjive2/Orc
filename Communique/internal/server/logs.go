package server

import (
	"net/http"
	"sort"

	"orc/cq/internal/logbook"
	"orc/cq/internal/protocol"
)

// logsView is what `tooling › logs` reads.
//
// The server's own lines and every machine's cycles in one answer, because they
// are one question — "what has been happening?" — and a screen that had to make
// four calls to draw four folds would show them arriving one at a time.
type logsView struct {
	// Server is this process's own recent log, from memory. Lost on a restart,
	// which the screen says rather than leaving somebody to wonder why a server
	// that has been up for a week has four lines.
	Server []logbook.Line `json:"server"`
	// Machines is what each agent machine's cycles have said, as of its last sync.
	// As stale as the mirror, and labelled with that age by the caller.
	Machines []machineLogs `json:"machines,omitempty"`
}

type machineLogs struct {
	Machine protocol.MachineID `json:"machine"`
	Tails   []protocol.LogTail `json:"tails,omitempty"`
	// Unreachable carries why a machine has nothing, when there is a reason worth
	// telling apart from "this one has never run a cycle".
	Unreachable string `json:"unreachable,omitempty"`
}

// logs answers with everything there is to read.
//
// A machine whose snapshot will not load is skipped rather than failing the
// request: one broken mirror must not take away the logs of every other machine,
// and the logs are what somebody opens when something is broken.
func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	out := logsView{Server: s.ring.tail()}

	machines, err := s.state.Machines()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	sort.Slice(machines, func(i, j int) bool { return machines[i] < machines[j] })

	for _, id := range machines {
		snap, _, err := s.state.Snapshot(id)
		if err != nil {
			out.Machines = append(out.Machines, machineLogs{
				Machine: id, Unreachable: "this machine's mirror could not be read",
			})
			continue
		}
		out.Machines = append(out.Machines, machineLogs{Machine: id, Tails: snap.Logs})
	}

	s.write(w, r, http.StatusOK, out)
}
