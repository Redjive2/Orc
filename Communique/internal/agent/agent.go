// Package agent implements `cq sync`: the agent machine's side of the mirror.
//
// One round trip carries both directions — the snapshot and the previous
// batch's results go up, the next batch of actions comes down — and the five
// steps are ordered so that a crash between any two of them loses nothing:
//
//  1. collect the machine's state
//  2. read the results the server has not yet taken
//  3. post both, and receive the actions to apply
//  4. apply each, recording its outcome before starting the next
//  5. record that the results were taken
//
// Step 4 is the one that matters. An action is journalled as *applying* before
// it is attempted and *applied* after, so a process killed in between leaves a
// record saying exactly that: the action is in doubt, and cq says so rather
// than silently sending the user's message a second time.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"orc/common/sandbox"
	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
	"orc/cq/internal/source"
)

// Version identifies this build to the server, for the machine list.
const Version = "cq/0.1"

// Defaults for the knobs a caller rarely sets.
const (
	// RequestTimeout bounds one sync round trip.
	RequestTimeout = 2 * time.Minute
	// PruneAfter is how long a reported result is kept before the journal
	// forgets it.
	PruneAfter = 7 * 24 * time.Hour
)

// Options configure an agent.
type Options struct {
	// Source is the machine being mirrored. Required.
	Source source.Source
	// Server is the base URL of `cq serve`. Required.
	Server string
	// Token authenticates the sync. Required.
	Token string
	// Machine names this machine. Required.
	Machine protocol.MachineID
	// State is the directory holding the journal and cursor.
	State string
	// Admin includes the whole-Mailman view in the snapshot.
	Admin bool
	// AdminBodies includes other users' bodies in it.
	AdminBodies bool
	// Library is the repository to mirror for reading, empty for none.
	Library string
	// Logger receives diagnostics. Required.
	Logger *slog.Logger
	// HTTP is the client to use. Defaults to one with RequestTimeout.
	HTTP *http.Client
	// Now supplies the current time. Defaults to time.Now.
	Now func() time.Time
}

// Agent mirrors one machine to one server.
type Agent struct {
	source  source.Source
	server  string
	token   string
	machine protocol.MachineID
	admin   bool
	bodies  bool
	library string
	log     *slog.Logger
	client  *http.Client
	now     func() time.Time
	journal *journal
	state   string
}

// CheckSettings refuses the settings an agent cannot work without, before
// anything expensive has been done with them.
//
// It is separate from New because the caller needs the answer earlier than it can
// build a source: working out whose mailbox to mirror means asking Orc, and a
// machine with no `$CQ_SERVER` should be told that rather than sent off to run
// another tool first. New calls it too, so there is one copy of every message.
func CheckSettings(server, token string, machine protocol.MachineID) error {
	switch {
	case server == "":
		return fault.Usage{Reason: "no server address; set --server or $CQ_SERVER"}
	case token == "":
		return fault.Usage{Reason: "no sync token; set $CQ_TOKEN"}
	}
	if err := machine.Validate(); err != nil {
		return err
	}
	if !strings.HasPrefix(server, "http://") && !strings.HasPrefix(server, "https://") {
		return fault.Usage{Reason: fmt.Sprintf("server address %q must begin with http:// or https://", server)}
	}
	return nil
}

// New builds an agent, refusing anything it cannot work without.
func New(opts Options) (*Agent, error) {
	switch {
	case opts.Source == nil:
		return nil, fault.Internal{Where: "agent.New", Detail: "no source"}
	case opts.Logger == nil:
		return nil, fault.Internal{Where: "agent.New", Detail: "no logger"}
	}
	if err := CheckSettings(opts.Server, opts.Token, opts.Machine); err != nil {
		return nil, err
	}

	// A process inside an Orcprobe probe may only touch that probe's own state.
	// The probe's shims already refuse `cq sync` outright, so this is the layer
	// underneath: a cq invoked by its full path, bypassing the shims, still
	// cannot open the real agent state and sync a sandbox's mail to the real
	// server. Outside a probe it does nothing.
	if err := sandbox.Guard(sandbox.OSEnv, opts.State); err != nil {
		return nil, err
	}

	j, err := openJournal(opts.State)
	if err != nil {
		return nil, err
	}

	a := &Agent{
		source:  opts.Source,
		server:  strings.TrimSuffix(opts.Server, "/"),
		token:   opts.Token,
		machine: opts.Machine,
		admin:   opts.Admin,
		bodies:  opts.AdminBodies,
		library: opts.Library,
		log:     opts.Logger,
		client:  opts.HTTP,
		now:     opts.Now,
		journal: j,
		state:   opts.State,
	}
	if a.client == nil {
		a.client = &http.Client{Timeout: RequestTimeout}
	}
	if a.now == nil {
		a.now = func() time.Time { return time.Now().UTC() }
	}
	return a, nil
}

// Report describes one sync, for the caller to print.
type Report struct {
	Machine   protocol.MachineID
	Sent      int  // results reported to the server
	Received  int  // actions the server handed back
	Applied   int  // actions that succeeded
	Failed    int  // actions that were refused
	Skipped   int  // actions already applied on an earlier sync
	Truncated bool // the journal's last line was an interrupted append
	// Pace is how often the server would like to be synced, when it says. Empty
	// means it did not, which a watcher reads as "keep the interval you have".
	Pace string
	// Paced is what the fleet's own cycles were put back to, when they had drifted
	// from what the server intends. Empty on the ordinary sync where they agreed.
	Paced []string
}

// String renders the report in one line.
func (r Report) String() string {
	return fmt.Sprintf("%s: %d up, %d down (%d applied, %d failed, %d already done)",
		r.Machine, r.Sent, r.Received, r.Applied, r.Failed, r.Skipped)
}

// Sync performs one round trip.
func (a *Agent) Sync(ctx context.Context) (Report, error) {
	report := Report{Machine: a.machine}

	// 1. Collect.
	snap, err := a.source.Snapshot(ctx, source.Options{
		Machine: a.machine, Admin: a.admin, AdminBodies: a.bodies, Library: a.library,
	})
	if err != nil {
		a.note(err)
		return report, err
	}
	if snap.Machine != a.machine {
		return report, fault.Internal{
			Where:  "agent.Sync",
			Detail: fmt.Sprintf("the source reported machine %q, not %q", snap.Machine, a.machine),
		}
	}

	// 2. Report what the previous batch came to.
	prior, err := a.journal.replay()
	if err != nil {
		a.note(err)
		return report, err
	}
	if prior.Truncated {
		report.Truncated = true
		a.log.Warn("the journal's last line was incomplete; an interrupted append was dropped")
	}
	results := prior.Unreported()
	report.Sent = len(results)

	// 3. Post.
	resp, err := a.post(ctx, protocol.SyncRequest{
		Protocol: protocol.Version,
		Agent:    Version,
		SentAt:   a.now(),
		Results:  results,
		Snapshot: snap,
	})
	if err != nil {
		a.note(err)
		return report, err
	}
	report.Received = len(resp.Actions)
	report.Pace = resp.Pace

	// 5 before 4, deliberately: the server has already taken the results, so
	// marking them reported now means a crash before the next step re-sends
	// nothing. Complete is idempotent on the server, so a duplicate would be
	// harmless anyway — but a duplicate that is never sent is better still.
	if len(results) > 0 {
		ids := make([]protocol.ActionID, len(results))
		for i, r := range results {
			ids[i] = r.ActionID
		}
		if err := a.journal.append(event{Op: opReported, IDs: ids, At: a.now()}); err != nil {
			a.note(err)
			return report, err
		}
	}

	// 4. Apply, one at a time, recording each before starting the next.
	for _, action := range resp.Actions {
		switch outcome, err := a.applyOne(ctx, prior, action); {
		case err != nil:
			a.note(err)
			return report, err
		case outcome == skipped:
			report.Skipped++
		case outcome == failed:
			report.Failed++
		default:
			report.Applied++
		}
	}

	// 5. Put the fleet's cycles back, if they have drifted from what is intended.
	//
	// After the actions, so a pace queued this round is not immediately argued with
	// by a snapshot taken before it ran.
	report.Paced = a.reconcilePace(ctx, snap, resp.FleetPace)

	if err := a.journal.writeCursor(cursor{
		LastSync: a.now(), Machine: a.machine, Server: a.server,
	}); err != nil {
		return report, err
	}
	if err := a.journal.prune(a.now().Add(-PruneAfter)); err != nil {
		// A journal that could not be tidied is not a failed sync.
		a.log.Warn("could not prune the journal", "error", err)
	}

	a.log.Info("sync", "machine", a.machine, "up", report.Sent, "down", report.Received,
		"applied", report.Applied, "failed", report.Failed, "skipped", report.Skipped)
	return report, nil
}

type applyOutcome int

const (
	applied applyOutcome = iota
	failed
	skipped
)

// applyOne performs a single action, or recognises that it has already been.
//
// The journal is written before and after the attempt. An action that was
// started and never finished is left in doubt rather than retried: `read` twice
// is harmless, but `send` twice is a second message to a real person, and cq
// does not know which of the two it is holding.
func (a *Agent) applyOne(ctx context.Context, prior state, action protocol.Action) (applyOutcome, error) {
	if o, done := prior.Applied(action.ID); done {
		a.log.Debug("action already dealt with", "id", action.ID, "in_doubt", o.InDoubt)
		return skipped, nil
	}
	if action.Machine != a.machine {
		// The server addressed another machine's mailbox. Refusing loudly beats
		// applying it here, where the puid means something else entirely.
		return failed, a.record(action.ID, false,
			fmt.Sprintf("addressed to machine %q, but this is %q", action.Machine, a.machine))
	}

	if err := a.journal.append(event{
		Op: opApplying, ID: action.ID, At: a.now(), Machine: a.machine,
	}); err != nil {
		return failed, err
	}

	if err := a.source.Apply(ctx, action); err != nil {
		a.log.Warn("action refused", "id", action.ID, "op", action.Op, "error", err)
		return failed, a.record(action.ID, false, err.Error())
	}
	a.log.Info("action applied", "id", action.ID, "op", action.Op)
	return applied, a.record(action.ID, true, "")
}

func (a *Agent) record(id protocol.ActionID, ok bool, reason string) error {
	return a.journal.append(event{Op: opApplied, ID: id, OK: ok, Error: reason, At: a.now()})
}

// note records a failure in the cursor, so `cq status` can say what went wrong
// without the operator reading a log.
func (a *Agent) note(err error) {
	c, readErr := a.journal.readCursor()
	if readErr != nil {
		return
	}
	c.Machine, c.Server, c.LastError = a.machine, a.server, err.Error()
	if writeErr := a.journal.writeCursor(c); writeErr != nil {
		a.log.Warn("could not record the failure", "error", writeErr)
	}
}

// Status returns what the last sync came to, without touching the network.
func (a *Agent) Status() (cursor, state, error) {
	c, err := a.journal.readCursor()
	if err != nil {
		return cursor{}, state{}, err
	}
	s, err := a.journal.replay()
	return c, s, err
}

// post sends one sync request and reads the response.
func (a *Agent) post(ctx context.Context, req protocol.SyncRequest) (protocol.SyncResponse, error) {
	var body bytes.Buffer
	if err := protocol.Encode(&body, &req); err != nil {
		return protocol.SyncResponse{}, err
	}

	url := a.server + "/api/v1/sync"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return protocol.SyncResponse{}, fault.Usage{Reason: fmt.Sprintf("bad server address %q: %v", a.server, err)}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+a.token)
	httpReq.Header.Set("User-Agent", Version)

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return protocol.SyncResponse{}, fault.Unavailable{Peer: a.server, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return protocol.SyncResponse{}, a.serverError(resp)
	}

	var out protocol.SyncResponse
	if err := protocol.Decode(resp.Body, protocol.MaxSnapshotBytes, &out); err != nil {
		return protocol.SyncResponse{}, err
	}
	return out, nil
}

// serverError turns a non-200 into a fault of the right kind, so `cq sync`
// exits with a status a script can branch on.
func (a *Agent) serverError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxRequestBytes))

	var doc protocol.APIError
	if err := json.Unmarshal(bytes.TrimSpace(data), &doc); err == nil && doc.Error.Code.Valid() {
		switch doc.Error.Code {
		case fault.CodeUnauthenticated:
			return fault.Unauthenticated{Reason: "the server rejected the sync token"}
		case fault.CodeParse, fault.CodeUsage:
			return fault.Parse{Where: a.server, Reason: doc.Error.Message}
		case fault.CodeConflict:
			return fault.Conflict{Subject: a.server, Reason: doc.Error.Message}
		default:
			return fault.Unavailable{Peer: a.server,
				Err: fmt.Errorf("%s: %s", doc.Error.Code, doc.Error.Message)}
		}
	}
	return fault.Unavailable{Peer: a.server,
		Err: fmt.Errorf("unexpected status %s", resp.Status)}
}

// UseLibrary points the agent at a different repository for its next round.
//
// It exists because the library root is the one collection setting that can move
// while a watcher is running: an operator changes it from the website, the change
// arrives down the queue, and the loop applies it between rounds. Everything else
// an agent is configured with is fixed for the life of the process.
//
// It is deliberately not safe to call during a round. `Sync` reads the value once
// when it builds its options, and the caller is the watch loop, which is between
// rounds by construction — the alternative is a lock around a field written once
// an hour by the same goroutine that reads it.
func (a *Agent) UseLibrary(root string) { a.library = root }

// reconcilePace puts the fleet's own cycles back to what the server intends.
//
// This is the difference between a setting and an event. A pace set in the browser
// used to be one queued action: if it failed, or the queue was cleared, or the
// machine was rebuilt from a checkout, the setting was gone and neither end knew.
// The server now says what it intends on every response, and this compares that
// against what orc actually resolved and corrects the difference.
//
// Consequences worth being plain about:
//
//   - A pace changed by hand on the machine is put back on the next sync. That is
//     what "the server intends this" means, and it is the reason the correction is
//     reported rather than made silently — somebody who ran `orc pace` and finds it
//     reverted should be able to see why in the sync's own output.
//   - Only the fleet layer. A role's or an identity's pace is that layer's, and orc
//     resolves the three; the fleet layer is the one the browser's panel sets.
//   - The comparison is against the snapshot taken at the start of this sync, which
//     is one round stale where an action changed things in between. That costs at
//     most one redundant `orc pace` with the values it already has, and converges.
//
// It never fails a sync. A machine that could not be corrected is a machine running
// at the wrong pace, which is worth reporting and is not worth throwing away a
// mirror that otherwise worked.
func (a *Agent) reconcilePace(ctx context.Context, snap protocol.Snapshot,
	want *protocol.DesiredPace) []string {
	if want == nil || want.Zero() || snap.Fleet == nil {
		return nil
	}

	var changed []string
	for _, cycle := range []struct {
		name string
		args protocol.Args
		says []string
	}{
		{"wake", paceArgs("wake", *want, snap.Fleet.Pace), paceSays("wake", *want, snap.Fleet.Pace)},
		{"tend", paceArgs("tend", *want, snap.Fleet.Pace), paceSays("tend", *want, snap.Fleet.Pace)},
	} {
		if len(cycle.says) == 0 {
			continue
		}
		err := a.source.Apply(ctx, protocol.Action{
			Machine: a.machine, Op: protocol.OpOrcPace, Args: cycle.args,
		})
		if err != nil {
			a.log.Warn("could not put the fleet's pace back",
				"cycle", cycle.name, "error", err)
			continue
		}
		a.log.Info("fleet pace put back", "cycle", cycle.name, "to", cycle.says)
		changed = append(changed, cycle.says...)
	}
	return changed
}

// paceArgs is the `orc pace` that would make one cycle match what is intended.
func paceArgs(cycle string, want protocol.DesiredPace, got protocol.FleetPace) protocol.Args {
	args := protocol.Args{Cycle: cycle}
	if cycle == "wake" {
		if want.WakeAfter != "" && want.WakeAfter != got.WakeAfter {
			args.After = want.WakeAfter
		}
		if want.WakeEvery != "" && want.WakeEvery != got.WakeEvery {
			args.Every = want.WakeEvery
		}
		args.PaceOff, args.PaceOn = switchTo(want.WakeOff, got.WakeOff)
		return args
	}
	if want.TendWatch != "" && want.TendWatch != got.TendWatch {
		args.Watch = want.TendWatch
	}
	args.PaceOff, args.PaceOn = switchTo(want.TendOff, got.TendOff)
	return args
}

// paceSays describes the correction in the words the report prints, and is empty
// when there is nothing to correct — which is how the caller decides whether to run
// anything at all.
func paceSays(cycle string, want protocol.DesiredPace, got protocol.FleetPace) []string {
	args := paceArgs(cycle, want, got)
	var out []string
	for _, f := range []struct{ flag, value string }{
		{"--after", args.After}, {"--every", args.Every}, {"--watch", args.Watch},
	} {
		if f.value != "" {
			out = append(out, fmt.Sprintf("%s %s %s", cycle, f.flag, f.value))
		}
	}
	switch {
	case args.PaceOff:
		out = append(out, cycle+" off")
	case args.PaceOn:
		out = append(out, cycle+" on")
	}
	return out
}

// switchTo turns "the server wants this cycle off/on" and "it is currently off/on"
// into the pair of flags that closes the gap, or neither where there is none.
//
// An absent intention is no opinion rather than "on": a server nobody has asked
// about tend must not start turning tend on every sync.
func switchTo(want string, off bool) (turnOff, turnOn bool) {
	switch {
	case want == "yes" && !off:
		return true, false
	case want == "no" && off:
		return false, true
	default:
		return false, false
	}
}
