package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
	"orc/cq/internal/store"
	"orc/cq/internal/style"
)

// `cq workspace` — moving an agent's working directory from a terminal.
//
// cq has no other per-verb command: the browser queues and `cq queue` inspects. This
// one exists because it is the fleet change somebody makes *while sitting at the
// machine*, having just moved a directory — and reaching for a browser to tell the
// fleet what you did with your own filesystem is the wrong shape.
//
// It behaves differently on each side and says which side it is on, because the two
// are genuinely different operations:
//
//   - **agent side** runs `orc workspace` directly. The fleet is right here; there is
//     nothing to wait for and no snapshot to be stale against.
//   - **server side** enqueues exactly as the API does, and says the change leaves on
//     the next sync.
//
// With both configured it refuses and asks which was meant. A command that silently
// picks one of two machines to change is one nobody can script — and the two answers
// differ in when they take effect, which is the part somebody would not notice.
func (a App) workspace(args []string) error {
	fs := flag.NewFlagSet("workspace", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	adopt := fs.Bool("adopt", false, "work in what is already there")
	from := fs.String("from", "", "where you believe it works now")
	stateDir := fs.String("state", "", "state directory (server side)")
	machine := fs.String("machine", "", "which machine's fleet (server side)")
	if err := parseWithArgs(fs, args); err != nil {
		return err
	}
	// An explicit --state is the operator saying which side they mean, which is the
	// whole point of offering it on a machine that is configured as both.
	chose := *stateDir != ""

	rest := fs.Args()
	if len(rest) != 2 {
		return fault.Usage{Reason: "workspace takes an identity and a path"}
	}
	identity, path := rest[0], rest[1]

	server := a.look("CQ_SERVER", "")
	state := *stateDir
	if state == "" {
		state = a.look("CQ_STATE", "")
	}

	switch {
	case chose:
		// Named explicitly, it is the queue regardless of what else is configured.
		return a.workspaceQueued(state, *machine, identity, path, *adopt, *from)
	case server != "" && state != "":
		return fault.Usage{Reason: "this machine is configured as both an agent " +
			"($CQ_SERVER) and a server ($CQ_STATE); say which with --state for the " +
			"queue, or unset one"}
	case server != "":
		return a.workspaceHere(identity, path, *adopt, *from)
	case state != "":
		return a.workspaceQueued(state, *machine, identity, path, *adopt, *from)
	default:
		return fault.Usage{Reason: "neither $CQ_SERVER nor $CQ_STATE is set, so " +
			"there is no fleet to change and no queue to put this in"}
	}
}

// workspaceHere is the agent side: Orc is on this machine, so ask it.
//
// `--from` is still honoured, because a person who typed it meant it — but it is
// checked here against what Orc says now, which is the same check the apply side of
// a queued action makes.
func (a App) workspaceHere(identity, path string, adopt bool, from string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	orc := a.orc()
	if from != "" {
		if err := orc.WorkspaceUnchanged(ctx, identity, from); err != nil {
			return err
		}
	}

	args := []string{"workspace", identity, path}
	if adopt {
		args = append(args, "--adopt")
	}
	out, err := orc.Output(ctx, args...)
	if err != nil {
		return err
	}
	// Orc's own words, verbatim: it says what it copied, what it left behind, and
	// which worktree bindings followed. Summarising that would drop the parts a
	// person needs and keep the part they already knew.
	_, err = fmt.Fprint(a.Stdout, string(out))
	return err
}

// workspaceQueued is the server side: the same action the browser makes.
func (a App) workspaceQueued(stateDir, machine, identity, path string, adopt bool, from string) error {
	state, err := store.Open(stateDir)
	if err != nil {
		return err
	}

	id, err := a.oneMachine(state, machine)
	if err != nil {
		return err
	}

	// `from` is required: the agent machine refuses a move made against a view that
	// has moved on, and a queued action with no view to check would be one that
	// could silently overturn a move made on the machine in between.
	//
	// Unstated, it is where the mirror says the identity works — which is exactly
	// what the browser sends, and what the operator was looking at when they typed
	// this. Guessing is not the risk; the check is against what orc says *now*.
	if from == "" {
		if from, err = a.mirroredWorkspace(state, id, identity); err != nil {
			return err
		}
	}

	action, err := state.Enqueue(id, protocol.OpOrcWorkspace, protocol.Args{
		Identity:  identity,
		Workspace: path,
		Adopt:     adopt,
		From:      from,
	}, time.Now())
	if err != nil {
		return err
	}

	if err := a.say("queued %s   %s → %s", a.ink(string(action.ID), style.Value), identity, path); err != nil {
		return err
	}
	// When it happens, not whether: the queue is not a promise, and the agent
	// machine can refuse. `cq queue` is where the answer turns up.
	return a.say("%s", a.ink(fmt.Sprintf(
		"it leaves on %s's next sync; `cq queue` says what became of it", id), style.Quiet))
}

// mirroredWorkspace is where the last sync said an identity works.
func (a App) mirroredWorkspace(state *store.Store, id protocol.MachineID, identity string) (string, error) {
	snap, _, err := state.Snapshot(id)
	if err != nil {
		return "", err
	}
	if snap.Fleet != nil {
		for _, i := range snap.Fleet.Identities {
			if i.Name == identity {
				if i.Workspace == "" {
					// Orc derives one when none is stored, and the mirror carries
					// what orc reported — so an empty field means the mirror is
					// older than the field, not that the agent works nowhere.
					break
				}
				return i.Workspace, nil
			}
		}
	}
	return "", fault.Usage{Reason: fmt.Sprintf(
		"the mirror does not say where %s works, so there is nothing to check the move "+
			"against; give --from with the directory you believe it uses", identity)}
}

// oneMachine resolves which fleet is meant.
//
// A server usually mirrors one machine, and making somebody name it every time would
// be ceremony. Two is where it stops guessing: picking one of them would change a
// fleet the operator was not thinking about.
func (a App) oneMachine(state *store.Store, want string) (protocol.MachineID, error) {
	if want != "" {
		return protocol.MachineID(want), nil
	}

	machines, err := state.Machines()
	if err != nil {
		return "", err
	}
	switch len(machines) {
	case 0:
		return "", fault.Usage{Reason: "no agent machine has ever synced with this " +
			"server, so there is nothing to queue against"}
	case 1:
		return machines[0], nil
	default:
		return "", fault.Ambiguous{Target: "machine", Candidates: names(machines)}
	}
}

func names(ids []protocol.MachineID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}
