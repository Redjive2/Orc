package server

import (
	"net/http"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
)

// The fleet: reading Orc's store, and changing it.
//
// The read is one route because a fleet is one derived thing — authority already
// capped by the boss chain, permissions already intersected — and serving it in
// pieces would invite the browser to recombine them, which is a second opinion
// about who may do what.
//
// The writes are one route per Orc verb, for the reasons in tasks.go: a
// pass-through would make the queue a list of shell commands nobody can report on,
// and would put authority checking on the far side of a sync.
//
// cq holds no opinion about who may do what. Every command runs as the mirrored
// account and Orc's own rules apply exactly as they do at a terminal — an agent
// cannot raise its own budget from a browser any more than from a shell, and the
// refusal comes back word for word in the queue.

// fleetView is one machine's fleet, with the machine named so the browser can
// address its verbs.
type fleetView struct {
	protocol.Fleet
	Machine protocol.MachineID `json:"machine"`
}

// fleets serves every machine's fleet. Most machines have none — mirroring a
// mailbox is not running agents — and those are left out rather than listed as
// empty, which would read as a fleet that had lost its identities.
func (s *Server) fleets(w http.ResponseWriter, r *http.Request) {
	ids, err := s.machineIDs()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := []fleetView{}
	for _, id := range ids {
		snap, _, err := s.snapshot(id)
		if err != nil {
			s.log.Warn("skipping unreadable machine", "machine", id, "error", err)
			continue
		}
		if snap.Fleet == nil {
			continue
		}
		out = append(out, fleetView{Fleet: *snap.Fleet, Machine: id})
	}
	s.ok(w, r, map[string]any{"fleets": out})
}

// fleetBody is the operand set every fleet route reads. Each handler takes the
// fields its verb uses; the protocol refuses the rest.
type fleetBody struct {
	Machine     string   `json:"machine,omitempty"`
	Name        string   `json:"name,omitempty"`
	Identity    string   `json:"identity,omitempty"`
	Role        string   `json:"role,omitempty"`
	Permission  string   `json:"permission,omitempty"`
	Boss        string   `json:"boss,omitempty"`
	Authority   int      `json:"authority,omitempty"`
	Floor       int      `json:"floor,omitempty"`
	Patterns    []string `json:"patterns,omitempty"`
	Description string   `json:"description,omitempty"`
	Model       string   `json:"model,omitempty"`
	Effort      string   `json:"effort,omitempty"`
	Until       string   `json:"until,omitempty"`
	Message     string   `json:"message,omitempty"`
	// `orc workspace`'s operands. From is where the browser saw the identity
	// working, and is what makes a stale click refusable rather than silent.
	Workspace string `json:"workspace,omitempty"`
	From      string `json:"from,omitempty"`
	Adopt     bool   `json:"adopt,omitempty"`
	// Text is a standing instruction's whole new text. The layer it belongs to is
	// in the path, not here.
	Text string `json:"text,omitempty"`
	// `orc pace`'s operands. The layer is the identity or the role, and neither
	// means the fleet's own; the durations are the strings a person typed.
	// `orc tariff`'s: which weight. What it becomes travels in `load` below, which
	// the body already carries for a budget — the same kind of number.
	Setting string `json:"setting,omitempty"`
	Cycle   string `json:"cycle,omitempty"`
	After   string `json:"after,omitempty"`
	Every   string `json:"every,omitempty"`
	Watch   string `json:"watch,omitempty"`
	Off     bool   `json:"off,omitempty"`
	On      bool   `json:"on,omitempty"`
	Clear   bool   `json:"clear,omitempty"`
	// Load is a spawn budget, and zero is a real one — a budget of nothing refuses
	// every employ. It is a pointer so "not given" and "given as zero" stay apart:
	// without that, setting a budget to nothing would be indistinguishable from
	// forgetting to set one.
	Load *int `json:"load,omitempty"`
}

// Validate is empty for the same reason taskBody's is: the operand rules are
// protocol.argRules, checked by Action.Validate inside Enqueue before anything is
// written, and a second copy here is a second copy to keep in step.
func (b *fleetBody) Validate() error { return nil }

// fleetAction reads the body and queues one operation. Every route below is one
// line of arguments and this.
func (s *Server) fleetAction(w http.ResponseWriter, r *http.Request, op protocol.Op,
	fill func(body fleetBody, args *protocol.Args)) {
	s.fleetActionThen(w, r, op, fill, nil)
}

// fleetActionThen is fleetAction with something to record once the action is
// queued. Only `pace` uses it, because only pace is a setting rather than an event.
func (s *Server) fleetActionThen(w http.ResponseWriter, r *http.Request, op protocol.Op,
	fill func(body fleetBody, args *protocol.Args),
	after func(protocol.MachineID, protocol.Args) error) {
	var body fleetBody
	if err := decode(r, MaxRequestBytes, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	var args protocol.Args
	if fill != nil {
		fill(body, &args)
	}
	var then func(protocol.MachineID) error
	if after != nil {
		then = func(id protocol.MachineID) error { return after(id, args) }
	}
	s.enqueueThen(w, r, body.Machine, op, args, then)
}

// named is the subject of a verb: the path segment where there is one, else the
// body. Creating something is the case with no path — the thing does not exist
// yet — and every other verb addresses something that does.
func named(r *http.Request, body fleetBody) string {
	if got := r.PathValue("name"); got != "" {
		return got
	}
	return body.Name
}

func (s *Server) newIdentity(w http.ResponseWriter, r *http.Request) {
	s.fleetAction(w, r, protocol.OpOrcNewIdentity, func(b fleetBody, a *protocol.Args) {
		a.Identity = named(r, b)
	})
}

func (s *Server) newRole(w http.ResponseWriter, r *http.Request) {
	s.fleetAction(w, r, protocol.OpOrcNewRole, func(b fleetBody, a *protocol.Args) {
		a.Role, a.Authority, a.Description = named(r, b), b.Authority, b.Description
	})
}

func (s *Server) newPermission(w http.ResponseWriter, r *http.Request) {
	s.fleetAction(w, r, protocol.OpOrcNewPermission, func(b fleetBody, a *protocol.Args) {
		a.Permission, a.Floor, a.Patterns = named(r, b), b.Floor, b.Patterns
	})
}

// editPermission replaces a permission's floor and clauses.
//
// PATCH rather than PUT: the permission's name is not in the body and cannot be
// changed by this, so what arrives is a modification of the thing at that URL
// rather than a replacement for it.
func (s *Server) editPermission(w http.ResponseWriter, r *http.Request) {
	s.fleetAction(w, r, protocol.OpOrcEditPermission, func(b fleetBody, a *protocol.Args) {
		a.Permission, a.Floor, a.Patterns = named(r, b), b.Floor, b.Patterns
	})
}

func (s *Server) assignRole(w http.ResponseWriter, r *http.Request) {
	s.fleetAction(w, r, protocol.OpOrcAssignRole, func(b fleetBody, a *protocol.Args) {
		a.Identity, a.Role = named(r, b), b.Role
	})
}

func (s *Server) assignAuthority(w http.ResponseWriter, r *http.Request) {
	s.fleetAction(w, r, protocol.OpOrcAssignAuthority, func(b fleetBody, a *protocol.Args) {
		a.Role, a.Authority = named(r, b), b.Authority
	})
}

func (s *Server) assignPermission(w http.ResponseWriter, r *http.Request) {
	s.fleetAction(w, r, protocol.OpOrcAssignPerm, func(b fleetBody, a *protocol.Args) {
		a.Role, a.Permission = named(r, b), b.Permission
	})
}

func (s *Server) removeIdentity(w http.ResponseWriter, r *http.Request) {
	s.fleetAction(w, r, protocol.OpOrcRemoveIdentity, func(b fleetBody, a *protocol.Args) {
		a.Identity = named(r, b)
	})
}

func (s *Server) removeRole(w http.ResponseWriter, r *http.Request) {
	s.fleetAction(w, r, protocol.OpOrcRemoveRole, func(b fleetBody, a *protocol.Args) {
		a.Role = named(r, b)
	})
}

func (s *Server) removePermission(w http.ResponseWriter, r *http.Request) {
	s.fleetAction(w, r, protocol.OpOrcRemovePerm, func(b fleetBody, a *protocol.Args) {
		// With a role it narrows that one role; without, it deletes the permission
		// outright.
		a.Permission, a.Role = named(r, b), b.Role
	})
}

func (s *Server) grantPermission(w http.ResponseWriter, r *http.Request) {
	s.fleetAction(w, r, protocol.OpOrcGrant, func(b fleetBody, a *protocol.Args) {
		a.Identity, a.Permission, a.Until = named(r, b), b.Permission, b.Until
	})
}

func (s *Server) revokePermission(w http.ResponseWriter, r *http.Request) {
	s.fleetAction(w, r, protocol.OpOrcRevoke, func(b fleetBody, a *protocol.Args) {
		a.Identity, a.Permission = named(r, b), b.Permission
	})
}

func (s *Server) moveIdentity(w http.ResponseWriter, r *http.Request) {
	s.fleetAction(w, r, protocol.OpOrcMove, func(b fleetBody, a *protocol.Args) {
		a.Identity, a.Boss = named(r, b), b.Boss
	})
}

func (s *Server) employIdentity(w http.ResponseWriter, r *http.Request) {
	s.fleetAction(w, r, protocol.OpOrcEmploy, func(b fleetBody, a *protocol.Args) {
		a.Identity, a.Model, a.Effort = named(r, b), b.Model, b.Effort
	})
}

func (s *Server) fireIdentity(w http.ResponseWriter, r *http.Request) {
	s.fleetAction(w, r, protocol.OpOrcFire, func(b fleetBody, a *protocol.Args) {
		a.Identity = named(r, b)
	})
}

func (s *Server) setBudget(w http.ResponseWriter, r *http.Request) {
	var body fleetBody
	if err := decode(r, MaxRequestBytes, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	if body.Load == nil {
		// Said here rather than left to the protocol, because the protocol cannot
		// tell an absent load from a deliberate zero and both are legitimate.
		s.fail(w, r, fault.Usage{Reason: "a budget needs a load; 0 is a budget that refuses every employ"})
		return
	}
	s.enqueue(w, r, body.Machine, protocol.OpOrcBudget,
		protocol.Args{Role: named(r, body), Load: *body.Load})
}

func (s *Server) pokeIdentity(w http.ResponseWriter, r *http.Request) {
	s.fleetAction(w, r, protocol.OpOrcPoke, func(b fleetBody, a *protocol.Args) {
		a.Identity, a.Message = named(r, b), b.Message
	})
}

func (s *Server) refreshIdentity(w http.ResponseWriter, r *http.Request) {
	s.fleetAction(w, r, protocol.OpOrcRefresh, func(b fleetBody, a *protocol.Args) {
		a.Identity = named(r, b)
	})
}

// setWorkspace changes where an identity works.
//
// `from` is required rather than optional, and the reason is the whole point of the
// route: a snapshot is minutes old by the time somebody clicks, and a client that
// cannot say what it was looking at is one that cannot be protected from acting on a
// stale view. `GET /api/v1/fleet` carries the workspace, so filling it in costs
// nothing.
func (s *Server) setWorkspace(w http.ResponseWriter, r *http.Request) {
	s.fleetAction(w, r, protocol.OpOrcWorkspace, func(b fleetBody, a *protocol.Args) {
		a.Identity, a.Workspace, a.From, a.Adopt = named(r, b), b.Workspace, b.From, b.Adopt
	})
}

// The standing instructions.
//
// One route per layer rather than one taking a kind, because the kind is *in* the
// path: `/instruct/system`, `/instruct/roles/{name}`, `/instruct/identities/{name}`,
// and `/wake` on any of them. A client that has to pass a discriminator in a body is
// one that can pass a discriminator that disagrees with the path it posted to.
func (s *Server) setInstruct(kind string, wake bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.fleetAction(w, r, protocol.OpOrcInstructSet, func(b fleetBody, a *protocol.Args) {
			a.Prompt, a.PromptName, a.Wake, a.Text = kind, r.PathValue("name"), wake, b.Text
		})
	}
}

func (s *Server) clearInstruct(kind string, wake bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.fleetAction(w, r, protocol.OpOrcInstructClear, func(b fleetBody, a *protocol.Args) {
			a.Prompt, a.PromptName, a.Wake = kind, r.PathValue("name"), wake
		})
	}
}

// installToolkit adds the permissions every fleet is made with, to a fleet that does
// not have all of them.
//
// The operator's name comes from the body rather than being inferred on the agent
// machine, so the queued action says exactly what it will run.
func (s *Server) installToolkit(w http.ResponseWriter, r *http.Request) {
	s.fleetAction(w, r, protocol.OpOrcToolkit, func(b fleetBody, a *protocol.Args) {
		a.Identity = named(r, b)
	})
}

// tariff changes one weight, for every budget on that machine at once.
func (s *Server) tariff(w http.ResponseWriter, r *http.Request) {
	s.fleetAction(w, r, protocol.OpOrcTariff, func(b fleetBody, a *protocol.Args) {
		a.Setting = b.Setting
		if b.Load != nil {
			a.Load = *b.Load
		}
		// A missing weight leaves Load at zero, which the protocol refuses with
		// "a weight of 0 is a session no budget can refuse" — the right message,
		// and better than one this route would have to invent.
	})
}

func (s *Server) tendFleet(w http.ResponseWriter, r *http.Request) {
	s.fleetAction(w, r, protocol.OpOrcTend, nil)
}

// pace sets how often a cycle runs, for the fleet, a role, or one agent.
//
// One route for all three, because it is one command with operands rather than
// three commands: the body says which cycle and whose layer, and a route per layer
// would be three that differ only in what they name.
func (s *Server) pace(w http.ResponseWriter, r *http.Request) {
	s.fleetActionThen(w, r, protocol.OpOrcPace, func(b fleetBody, a *protocol.Args) {
		a.Cycle = b.Cycle
		a.Identity = b.Identity
		a.Role = b.Role
		a.After = b.After
		a.Every = b.Every
		a.Watch = b.Watch
		a.PaceOff = b.Off
		a.PaceOn = b.On
		a.PaceClear = b.Clear
	}, func(id protocol.MachineID, a protocol.Args) error {
		// The fleet layer only. A role's or an identity's pace is that layer's
		// business and orc resolves the three together; a server that asserted all
		// of them every sync would overwrite what somebody set on the machine with
		// whatever was last typed in a browser.
		if a.Identity != "" || a.Role != "" {
			return nil
		}
		_, err := s.state.IntendFleetPace(id, a)
		return err
	})
}
