package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
	"orc/cq/internal/store"
	"orc/cq/internal/style"
)

// The queue, from the server machine.
//
// The website is where this normally happens — every failed action there has a
// retry beside it. This exists for when the site is not reachable, and for
// scripting, and because an operator debugging a stuck queue should not have to
// open a browser to see it.

func (a App) queue(args []string) error {
	fs := flag.NewFlagSet("queue", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateDir := fs.String("state", a.look("CQ_STATE", defaultStateDir()), "state directory")
	asJSON := fs.Bool("json", false, "print as JSON")
	if err := parseWithArgs(fs, args); err != nil {
		return err
	}
	rest := fs.Args()

	state, err := store.Open(*stateDir)
	if err != nil {
		return err
	}

	if len(rest) == 0 {
		return a.queueList(state, *asJSON)
	}

	switch rest[0] {
	case "retry":
		return a.queueRetry(state, rest[1:])
	case "drop":
		return a.queueDrop(state, rest[1:])
	default:
		return fault.Usage{Reason: fmt.Sprintf(
			"unknown queue subcommand %q; try `cq queue`, `cq queue retry <id>`, or `cq queue drop <id>`", rest[0])}
	}
}

// queueList prints every action and what became of it.
func (a App) queueList(state *store.Store, asJSON bool) error {
	entries, err := state.Queue()
	if err != nil {
		return err
	}
	if asJSON {
		if entries == nil {
			entries = []store.Entry{}
		}
		// Output for another program. Colour would be corruption in it, and the
		// entries are the store's own shape rather than a projection: the queue
		// is cq's on both sides, so there is no second format to translate to.
		body, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return fault.Internal{Where: "cli.queueList", Detail: err.Error()}
		}
		return a.say("%s", body)
	}

	if len(entries) == 0 {
		return a.say("%s", a.ink("the queue is empty", style.Quiet))
	}
	for _, e := range entries {
		if err := a.sayEntry(e); err != nil {
			return err
		}
	}
	return a.queueSummary(entries)
}

// sayEntry prints one action on one line, with its reason indented under it when
// there is one.
//
// Each column is painted and then padded from the *plain* text, which is the
// house rule: an escape sequence occupies no columns, so measuring a painted
// string counts the sequence as content and every row after the first drifts.
// (`pad` returns the spaces to append, not a padded string.)
func (a App) sayEntry(e store.Entry) error {
	id, state, op := string(e.Action.ID)[:8], string(e.State), string(e.Action.Op)

	if err := a.say("%s%s%s%s%s%s%s",
		a.ink(id, style.Setting), pad(id, 9),
		a.ink(state, stateInk(e.State)), pad(state, 10),
		a.ink(op, style.Value), pad(op, 9),
		describe(e.Action)); err != nil {
		return err
	}
	if e.Error != "" {
		for _, l := range strings.Split(strings.TrimRight(e.Error, "\n"), "\n") {
			if err := a.say("  %s", a.ink(strings.TrimSpace(l), style.Quiet)); err != nil {
				return err
			}
		}
	}
	return nil
}

// queueSummary says what can still be done, because a list of rows does not.
func (a App) queueSummary(entries []store.Entry) error {
	var waiting, stuck int
	for _, e := range entries {
		switch {
		case e.State.Pending():
			waiting++
		case e.State.Unresolved():
			stuck++
		}
	}
	if err := a.say(""); err != nil {
		return err
	}
	if err := a.say("%s", a.ink(fmt.Sprintf("%d waiting · %d unresolved · %d in all",
		waiting, stuck, len(entries)), style.Quiet)); err != nil {
		return err
	}
	if stuck > 0 {
		return a.say("%s", a.ink("  cq queue retry <id> to try again · cq queue drop <id> to forget it", style.Quiet))
	}
	return nil
}

func (a App) queueRetry(state *store.Store, args []string) error {
	id, err := resolve(state, args, "retry")
	if err != nil {
		return err
	}
	action, err := state.Retry(id, time.Now().UTC())
	if err != nil {
		return err
	}
	// The new identifier is named, not hidden. It is a different action — it has
	// to be, or the agent recognises the old one and skips it — and the operator
	// following a stuck queue needs to know which row to watch.
	if err := a.say("%s queued again as %s", a.ink("✓", style.Good),
		a.ink(string(action.ID)[:8], style.Setting)); err != nil {
		return err
	}
	return a.say("%s", a.ink("  it leaves on the next sync", style.Quiet))
}

func (a App) queueDrop(state *store.Store, args []string) error {
	id, err := resolve(state, args, "drop")
	if err != nil {
		return err
	}
	if err := state.Drop(id); err != nil {
		return err
	}
	return a.say("%s forgotten", a.ink("✓", style.Good))
}

// resolve turns what the operator typed into one action id.
//
// A prefix is accepted, because the listing prints eight characters and asking
// them to retype thirty-two from a screen is a way of making a command unusable.
// An ambiguous prefix lists the candidates in full, so every line of the refusal
// is itself a usable argument.
func resolve(state *store.Store, args []string, verb string) (protocol.ActionID, error) {
	if len(args) != 1 {
		return "", fault.Usage{Reason: fmt.Sprintf(
			"queue %s takes one action id, got %d arguments", verb, len(args))}
	}
	typed := strings.ToLower(strings.TrimSpace(args[0]))
	if typed == "" {
		return "", fault.Usage{Reason: "no action id given"}
	}

	// A full id needs no lookup, so a queue that cannot be read is still
	// actionable when the operator knows exactly what they want.
	if err := protocol.ActionID(typed).Validate(); err == nil {
		return protocol.ActionID(typed), nil
	}

	entries, err := state.Queue()
	if err != nil {
		return "", err
	}
	var found []string
	for _, e := range entries {
		if strings.HasPrefix(string(e.Action.ID), typed) {
			found = append(found, string(e.Action.ID))
		}
	}
	switch len(found) {
	case 1:
		return protocol.ActionID(found[0]), nil
	case 0:
		return "", fault.NotFound{What: "action", Name: typed}
	default:
		return "", fault.Ambiguous{Target: typed, Candidates: found}
	}
}

// stateInk paints a state by what it means: something waiting is not a problem,
// and something unresolved is.
func stateInk(s store.State) style.Ink {
	switch {
	case s == store.Done:
		return style.Good
	case s.Unresolved():
		return style.Alarm
	default:
		return style.Value
	}
}

// describe says what an action was for, in the terms the operator used.
func describe(action protocol.Action) string {
	switch action.Op {
	case protocol.OpSend:
		return fmt.Sprintf("to %s — %s", strings.Join(action.Args.To, ", "), action.Args.Subject)
	case protocol.OpReply:
		return fmt.Sprintf("#%d — %s", action.Args.PUID, action.Args.Subject)
	case protocol.OpCC:
		return fmt.Sprintf("%s into %s", action.Args.User, action.Args.ConvoUID)
	case protocol.OpWrite, protocol.OpCreate, protocol.OpDelete,
		protocol.OpMakeDir, protocol.OpRemoveDir:
		// The path, because that is the whole of what a library verb is about.
		// Without it the queue read "write #0", which names nothing.
		return action.Args.Path
	case protocol.OpTaskAssign, protocol.OpTaskInvite, protocol.OpTaskKick:
		return fmt.Sprintf("%s — %s", action.Args.Task, action.Args.User)
	case protocol.OpTaskStatus:
		return fmt.Sprintf("%s — status %d", action.Args.Task, action.Args.Status)
	case protocol.OpTaskCreate:
		return fmt.Sprintf("%s — priority %d, difficulty %d",
			action.Args.Task, action.Args.Priority, action.Args.Difficulty)
	case protocol.OpTaskScope:
		return fmt.Sprintf("%s — %s", action.Args.Task, strings.Join(action.Args.Paths, " "))
	case protocol.OpTaskWorktree:
		return fmt.Sprintf("%s — %s", action.Args.Task, action.Args.Path)
	case protocol.OpTaskSubtask:
		return fmt.Sprintf("%s — %s", action.Args.Task, action.Args.Sub)
	case protocol.OpTaskComplete, protocol.OpTaskDelete:
		// The subtask when there is one, because "delete fix-the-parser" and
		// "delete one step of fix-the-parser" are very different things to read in
		// a queue you are deciding whether to retry.
		if action.Args.Sub != "" {
			return fmt.Sprintf("%s — %s", action.Args.Task, action.Args.Sub)
		}
		return action.Args.Task
	case protocol.OpTaskPush, protocol.OpTaskClaim, protocol.OpTaskLeave:
		return action.Args.Task

	case protocol.OpOrcNewRole, protocol.OpOrcAssignAuthority:
		return fmt.Sprintf("%s — authority %d", action.Args.Role, action.Args.Authority)
	case protocol.OpOrcNewPermission:
		return fmt.Sprintf("%s — floor %d, %s", action.Args.Permission, action.Args.Floor,
			strings.Join(action.Args.Patterns, " "))
	case protocol.OpOrcAssignRole:
		return fmt.Sprintf("%s — %s", action.Args.Identity, action.Args.Role)
	case protocol.OpOrcAssignPerm:
		return fmt.Sprintf("%s — %s", action.Args.Role, action.Args.Permission)
	case protocol.OpOrcRemovePerm:
		// The role when there is one: taking a permission off one role and deleting
		// it outright are very different things to read in a queue you are deciding
		// whether to retry.
		if action.Args.Role != "" {
			return fmt.Sprintf("%s — from %s", action.Args.Permission, action.Args.Role)
		}
		return action.Args.Permission
	case protocol.OpOrcGrant, protocol.OpOrcRevoke:
		out := fmt.Sprintf("%s — %s", action.Args.Identity, action.Args.Permission)
		if action.Args.Until != "" {
			out += " until " + action.Args.Until
		}
		return out
	case protocol.OpOrcMove:
		return fmt.Sprintf("%s — under %s", action.Args.Identity, action.Args.Boss)
	case protocol.OpOrcEmploy:
		if action.Args.Model != "" || action.Args.Effort != "" {
			return fmt.Sprintf("%s — %s/%s", action.Args.Identity, action.Args.Model, action.Args.Effort)
		}
		return action.Args.Identity
	case protocol.OpOrcBudget:
		return fmt.Sprintf("%s — %d", action.Args.Role, action.Args.Load)
	case protocol.OpOrcPoke:
		if action.Args.Message != "" {
			return fmt.Sprintf("%s — %s", action.Args.Identity, action.Args.Message)
		}
		return action.Args.Identity
	case protocol.OpOrcNewIdentity, protocol.OpOrcRemoveIdentity,
		protocol.OpOrcFire, protocol.OpOrcRefresh:
		return action.Args.Identity
	case protocol.OpOrcRemoveRole:
		return action.Args.Role
	case protocol.OpOrcTend:
		// It takes no operand: what it is about is the whole fleet.
		return "the whole work list"
	default:
		return fmt.Sprintf("#%d", action.Args.PUID)
	}
}
