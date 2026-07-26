package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"orc/cq/internal/auth"
	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
	"orc/cq/internal/store"
)

// --- request and response plumbing ---------------------------------------

func withSession(r *http.Request, s auth.Session) context.Context {
	return context.WithValue(r.Context(), sessionKey{}, s)
}

func sessionFrom(r *http.Request) (auth.Session, bool) {
	s, ok := r.Context().Value(sessionKey{}).(auth.Session)
	return s, ok
}

// write sends a JSON document with a status.
func (s *Server) write(w http.ResponseWriter, r *http.Request, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		s.log.Error("could not encode response", "path", r.URL.Path, "error", err)
		http.Error(w, `{"error":{"code":"internal","message":"internal error"}}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if _, err := w.Write(append(body, '\n')); err != nil {
		s.log.Warn("could not write response", "path", r.URL.Path, "error", err)
	}
}

func (s *Server) ok(w http.ResponseWriter, r *http.Request, v any) {
	s.write(w, r, http.StatusOK, v)
}

// fail reports an error to the caller and, at a level matching its severity, to
// the log. The body carries only what a caller may see: fault.Public has
// already reduced internal detail to a neutral message.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	code := fault.Classify(err)
	if code == fault.CodeInternal {
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	} else {
		s.log.Info("request refused", "method", r.Method, "path", r.URL.Path, "code", code, "error", err)
	}
	if code == fault.CodeUnauthenticated {
		w.Header().Set("WWW-Authenticate", `Bearer realm="cq"`)
	}
	s.write(w, r, fault.Status(err), protocol.NewAPIError(err))
}

// serverSide reclassifies a failure that came from cq's own storage.
//
// A caller asked for their mail; that the server could not read a directory is
// not their business, and the path it could not read is certainly not. The full
// text still reaches the log, where the operator needs it — this only decides
// what crosses the wire.
//
// Ambiguity survives, because its candidates are the user's own machine names
// and choosing between them is the caller's job. Everything else becomes an
// internal fault, whose public rendering is the word "internal error".
func serverSide(err error) error {
	if err == nil {
		return nil
	}
	switch fault.Classify(err) {
	case fault.CodeAmbiguous:
		return err
	case fault.CodeNotFound:
		return fault.NotFound{What: "machine"}
	default:
		return fault.Internal{Where: "server.storage", Detail: err.Error()}
	}
}

// queueSide is serverSide for the queue operations, whose refusals are meant to
// be read.
//
// serverSide exists to stop a filesystem path reaching a client, and it does
// that by flattening everything it does not recognise into an internal error.
// That is right for storage plumbing and wrong here: `Retry` and `Drop` refuse
// deliberately, with sentences written for the operator — "this send was
// interrupted and may already have been delivered" — and flattening those to
// "internal error" would hide the one thing the reader needs. serverSide also
// rewrites every not-found as a missing *machine*, which is the wrong noun for a
// missing action.
//
// So a conflict and a not-found pass through, because the queue authors both and
// neither mentions a path; anything else is still internalised.
func queueSide(err error) error {
	switch fault.Classify(err) {
	case fault.CodeConflict, fault.CodeNotFound:
		return err
	default:
		return serverSide(err)
	}
}

// The accessors below are the only way a handler reaches storage, so a raw
// store error cannot escape to a client by being forgotten at one call site.

func (s *Server) machineIDs() ([]protocol.MachineID, error) {
	ids, err := s.state.Machines()
	return ids, serverSide(err)
}

func (s *Server) snapshot(id protocol.MachineID) (protocol.Snapshot, store.Meta, error) {
	snap, meta, err := s.state.Snapshot(id)
	return snap, meta, serverSide(err)
}

// agentSide is serverSide for the one endpoint whose caller is cq's own agent.
// A malformed snapshot or result is the agent's doing and is reported as such,
// so a misbehaving sync can be diagnosed; a storage failure is still the
// server's own business.
func agentSide(err error) error {
	switch fault.Classify(err) {
	case fault.CodeParse, fault.CodeUsage:
		return err
	default:
		return serverSide(err)
	}
}

func (s *Server) queueEntries() ([]store.Entry, error) {
	entries, err := s.state.Queue()
	return entries, serverSide(err)
}

// decode reads a JSON request body, refusing anything oversized or unexpected.
func decode(r *http.Request, limit int64, v protocol.Validator) error {
	return protocol.Decode(r.Body, limit, v)
}

// --- the public three ----------------------------------------------------

type healthView struct {
	OK   bool      `json:"ok"`
	Time time.Time `json:"time"`
}

// health says the process is alive and nothing else. It is the only endpoint
// that answers a stranger with a body.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	s.ok(w, r, healthView{OK: true, Time: s.now()})
}

// --- session -------------------------------------------------------------

type sessionView struct {
	User     string        `json:"user"`
	CSRF     string        `json:"csrf"`
	Expires  time.Time     `json:"expires"`
	Machines []machineView `json:"machines"`
	Admin    bool          `json:"admin"`
	Now      time.Time     `json:"now"`
}

type machineView struct {
	Machine  protocol.MachineID `json:"machine"`
	User     string             `json:"user"`
	LastSync time.Time          `json:"last_sync"`
	Agent    string             `json:"agent,omitempty"`
	Unread   int                `json:"unread"`
	Total    int                `json:"total"`
}

// session tells the browser who it is and how stale the mirror is. The CSRF
// token is delivered here rather than in a readable cookie, so it exists only
// where a script that already has the session can reach it.
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	sess, ok := sessionFrom(r)
	if !ok {
		s.fail(w, r, fault.Internal{Where: "server.session", Detail: "no session on an authenticated request"})
		return
	}
	views, err := s.machineViews()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	user := ""
	if len(views) > 0 {
		user = views[0].User
	}
	s.ok(w, r, sessionView{
		User: user, CSRF: sess.CSRF, Expires: sess.Expires,
		Machines: views, Admin: s.admin, Now: s.now(),
	})
}

func (s *Server) machines(w http.ResponseWriter, r *http.Request) {
	views, err := s.machineViews()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.ok(w, r, map[string]any{"machines": views})
}

// machineViews summarises every machine that has synced.
func (s *Server) machineViews() ([]machineView, error) {
	ids, err := s.machineIDs()
	if err != nil {
		return nil, err
	}
	out := make([]machineView, 0, len(ids))
	for _, id := range ids {
		snap, meta, err := s.snapshot(id)
		if err != nil {
			// A machine whose directory exists but whose snapshot is
			// unreadable is reported and skipped: one bad machine must not
			// blank the whole view.
			s.log.Warn("skipping unreadable machine", "machine", id, "error", err)
			continue
		}
		unread := 0
		for _, m := range snap.Inbox {
			if !m.Read {
				unread++
			}
		}
		out = append(out, machineView{
			Machine: id, User: snap.User, LastSync: meta.LastSync, Agent: meta.Agent,
			Unread: unread, Total: len(snap.Inbox),
		})
	}
	return out, nil
}

// --- reading mail --------------------------------------------------------

// messageView is a message plus the machine whose mailbox it lives in. With one
// machine the field is noise; with two it is the difference between two inboxes
// and one confusing list.
type messageView struct {
	protocol.Message
	Machine protocol.MachineID `json:"machine"`
}

type listView struct {
	Messages []messageView `json:"messages"`
	Machines []machineView `json:"machines"`
}

// box names one of the three lists a mirrored mailbox keeps. They are separate
// because Mailman keeps them separate: mail you were sent, mail you filed away,
// and mail you wrote are three different questions.
type box int

const (
	boxInbox box = iota
	boxArchive
	boxSent
)

func (s *Server) inbox(w http.ResponseWriter, r *http.Request)   { s.mailbox(w, r, boxInbox) }
func (s *Server) archive(w http.ResponseWriter, r *http.Request) { s.mailbox(w, r, boxArchive) }
func (s *Server) sent(w http.ResponseWriter, r *http.Request)    { s.mailbox(w, r, boxSent) }

// mailbox lists mail across every machine, newest first, optionally filtered to
// one machine.
func (s *Server) mailbox(w http.ResponseWriter, r *http.Request, which box) {
	only := r.URL.Query().Get("machine")
	if only != "" {
		if err := protocol.MachineID(only).Validate(); err != nil {
			s.fail(w, r, err)
			return
		}
	}

	ids, err := s.machineIDs()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := []messageView{}
	for _, id := range ids {
		if only != "" && string(id) != only {
			continue
		}
		snap, _, err := s.snapshot(id)
		if err != nil {
			s.log.Warn("skipping unreadable machine", "machine", id, "error", err)
			continue
		}
		var source []protocol.Message
		switch which {
		case boxArchive:
			source = snap.Archive
		case boxSent:
			source = snap.Sent
		default:
			source = snap.Inbox
		}
		for _, m := range source {
			out = append(out, messageView{Message: m, Machine: id})
		}
	}
	slices.SortStableFunc(out, func(a, b messageView) int { return b.Sent.Compare(a.Sent) })

	views, err := s.machineViews()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.ok(w, r, listView{Messages: out, Machines: views})
}

type messageDetail struct {
	Message messageView   `json:"message"`
	Thread  []messageView `json:"thread"`
}

// message returns one message and the conversation it belongs to.
func (s *Server) message(w http.ResponseWriter, r *http.Request) {
	found, err := s.find(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	thread, err := s.thread(found.Machine, found.Convo.UID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.ok(w, r, messageDetail{Message: found, Thread: thread})
}

// convo returns every message in one conversation.
func (s *Server) convo(w http.ResponseWriter, r *http.Request) {
	cuid := r.PathValue("cuid")
	if cuid == "" {
		s.fail(w, r, fault.Usage{Reason: "no conversation given"})
		return
	}
	id, err := s.machineFor(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	thread, err := s.thread(id, cuid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(thread) == 0 {
		s.fail(w, r, fault.NotFound{What: "conversation", Name: cuid})
		return
	}
	s.ok(w, r, listView{Messages: thread})
}

// thread collects a conversation's messages from one machine, oldest first.
func (s *Server) thread(id protocol.MachineID, cuid string) ([]messageView, error) {
	if cuid == "" {
		return nil, nil
	}
	snap, _, err := s.snapshot(id)
	if err != nil {
		return nil, err
	}
	out := []messageView{}
	for _, m := range slices.Concat(snap.Inbox, snap.Archive, snap.Sent) {
		if m.Convo.UID == cuid {
			out = append(out, messageView{Message: m, Machine: id})
		}
	}
	slices.SortStableFunc(out, func(a, b messageView) int { return a.Sent.Compare(b.Sent) })
	return out, nil
}

// check reports who has read a message, from the admin snapshot's receipts.
func (s *Server) check(w http.ResponseWriter, r *http.Request) {
	found, err := s.find(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	snap, _, err := s.snapshot(found.Machine)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	receipts := []protocol.Receipt{}
	if snap.Admin != nil {
		for _, rec := range snap.Admin.Receipts {
			if rec.MID == found.MID {
				receipts = append(receipts, rec)
			}
		}
	}
	s.ok(w, r, map[string]any{"mid": found.MID, "receipts": receipts})
}

// find locates the message a request addresses, by puid and machine.
func (s *Server) find(r *http.Request) (messageView, error) {
	raw := r.PathValue("puid")
	puid, err := strconv.Atoi(raw)
	if err != nil || puid < 0 {
		return messageView{}, fault.Usage{Reason: "message id must be a non-negative number, got " + strconv.Quote(raw)}
	}
	id, err := s.machineFor(r)
	if err != nil {
		return messageView{}, err
	}
	snap, _, err := s.snapshot(id)
	if err != nil {
		return messageView{}, err
	}
	for _, m := range slices.Concat(snap.Inbox, snap.Archive, snap.Sent) {
		if m.PUID == puid {
			return messageView{Message: m, Machine: id}, nil
		}
	}
	return messageView{}, fault.NotFound{What: "message", Name: raw}
}

// machineFor decides which machine a request means.
//
// With one machine the parameter is unnecessary, so it is optional. With
// several, guessing would be picking a mailbox at random, so it is required and
// the error names the choices.
func (s *Server) machineFor(r *http.Request) (protocol.MachineID, error) {
	if given := r.URL.Query().Get("machine"); given != "" {
		id := protocol.MachineID(given)
		if err := id.Validate(); err != nil {
			return "", err
		}
		return id, nil
	}
	ids, err := s.machineIDs()
	if err != nil {
		return "", err
	}
	switch len(ids) {
	case 0:
		return "", fault.NotFound{What: "machine"}
	case 1:
		return ids[0], nil
	default:
		names := make([]string, len(ids))
		for i, id := range ids {
			names[i] = string(id)
		}
		return "", fault.Ambiguous{Target: "machine", Candidates: names}
	}
}

// --- tasks and admin -----------------------------------------------------

type taskView struct {
	protocol.Task
	Machine protocol.MachineID `json:"machine"`
}

func (s *Server) tasks(w http.ResponseWriter, r *http.Request) {
	ids, err := s.machineIDs()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := []taskView{}
	for _, id := range ids {
		snap, _, err := s.snapshot(id)
		if err != nil {
			s.log.Warn("skipping unreadable machine", "machine", id, "error", err)
			continue
		}
		for _, t := range snap.Tasks {
			out = append(out, taskView{Task: t, Machine: id})
		}
	}
	slices.SortStableFunc(out, func(a, b taskView) int {
		if d := b.Priority - a.Priority; d != 0 {
			return d
		}
		if d := b.Difficulty - a.Difficulty; d != 0 {
			return d
		}
		return strings.Compare(a.Name, b.Name)
	})
	s.ok(w, r, map[string]any{"tasks": out})
}

func (s *Server) task(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ids, err := s.machineIDs()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, id := range ids {
		snap, _, err := s.snapshot(id)
		if err != nil {
			continue
		}
		for _, t := range snap.Tasks {
			if t.Name == name {
				s.ok(w, r, taskView{Task: t, Machine: id})
				return
			}
		}
	}
	s.fail(w, r, fault.NotFound{What: "task", Name: name})
}

// adminState returns the whole Mailman state, machine by machine.
func (s *Server) adminState(w http.ResponseWriter, r *http.Request) {
	if !s.admin {
		s.fail(w, r, fault.NotFound{What: "admin panel"})
		return
	}
	ids, err := s.machineIDs()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	type block struct {
		Machine  protocol.MachineID   `json:"machine"`
		LastSync time.Time            `json:"last_sync"`
		State    *protocol.AdminState `json:"state"`
	}
	out := []block{}
	for _, id := range ids {
		snap, meta, err := s.snapshot(id)
		if err != nil {
			s.log.Warn("skipping unreadable machine", "machine", id, "error", err)
			continue
		}
		out = append(out, block{Machine: id, LastSync: meta.LastSync, State: snap.Admin})
	}
	s.ok(w, r, map[string]any{"machines": out})
}

// queue shows what the user has asked for and what became of it.
func (s *Server) queue(w http.ResponseWriter, r *http.Request) {
	entries, err := s.queueEntries()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if entries == nil {
		entries = []store.Entry{}
	}
	s.ok(w, r, map[string]any{"queue": entries})
}

// --- writing mail --------------------------------------------------------

// queuedView is what a state-changing request answers with. It reports the
// action's place in the queue and nothing about it having happened, because it
// has not: it leaves on the next sync.
type queuedView struct {
	ActionID protocol.ActionID  `json:"action_id"`
	Seq      uint64             `json:"seq"`
	Machine  protocol.MachineID `json:"machine"`
	State    store.State        `json:"state"`
	Queued   time.Time          `json:"queued"`
}

type sendBody struct {
	Machine string   `json:"machine,omitempty"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	Body    string   `json:"body"`
}

func (b *sendBody) Validate() error {
	if len(b.To) == 0 {
		return fault.Usage{Reason: "send needs at least one recipient"}
	}
	if b.Subject == "" {
		return fault.Usage{Reason: "send needs a subject"}
	}
	if b.Body == "" {
		return fault.Usage{Reason: "send needs a body"}
	}
	return nil
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	var body sendBody
	if err := decode(r, MaxRequestBytes, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	s.enqueue(w, r, body.Machine, protocol.OpSend, protocol.Args{
		To: body.To, Subject: body.Subject, Body: body.Body,
	})
}

type replyBody struct {
	Machine string `json:"machine,omitempty"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func (b *replyBody) Validate() error {
	if b.Subject == "" {
		return fault.Usage{Reason: "reply needs a subject"}
	}
	if b.Body == "" {
		return fault.Usage{Reason: "reply needs a body"}
	}
	return nil
}

func (s *Server) reply(w http.ResponseWriter, r *http.Request) {
	var body replyBody
	if err := decode(r, MaxRequestBytes, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	found, err := s.find(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.enqueue(w, r, string(found.Machine), protocol.OpReply, protocol.Args{
		PUID: found.PUID, Subject: body.Subject, Body: body.Body,
	})
}

func (s *Server) markRead(w http.ResponseWriter, r *http.Request) {
	s.simple(w, r, protocol.OpRead)
}

func (s *Server) archiveMessage(w http.ResponseWriter, r *http.Request) {
	s.simple(w, r, protocol.OpArchive)
}

// simple handles the two actions whose only operand is the message itself.
func (s *Server) simple(w http.ResponseWriter, r *http.Request, op protocol.Op) {
	found, err := s.find(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.enqueue(w, r, string(found.Machine), op, protocol.Args{PUID: found.PUID})
}

type ccBody struct {
	Machine string `json:"machine,omitempty"`
	User    string `json:"user"`
}

func (b *ccBody) Validate() error {
	if b.User == "" {
		return fault.Usage{Reason: "cc needs a user"}
	}
	return nil
}

func (s *Server) cc(w http.ResponseWriter, r *http.Request) {
	var body ccBody
	if err := decode(r, MaxRequestBytes, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	cuid := r.PathValue("cuid")
	if cuid == "" {
		s.fail(w, r, fault.Usage{Reason: "no conversation given"})
		return
	}
	s.enqueue(w, r, body.Machine, protocol.OpCC, protocol.Args{ConvoUID: cuid, User: body.User})
}

// enqueue records an action and answers 202. The status is the honest one: the
// work is accepted, not done.
// retryAction queues a fresh copy of an action that did not work.
//
// A new action, not a revived one: the identifier has to change or the agent
// will recognise it and skip it. The response therefore names the *new* action,
// so the caller can follow the thing that is actually going to happen.
func (s *Server) retryAction(w http.ResponseWriter, r *http.Request) {
	id, err := actionID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	action, err := s.state.Retry(id, s.now())
	if err != nil {
		s.fail(w, r, queueSide(err))
		return
	}
	s.log.Info("action retried", "was", id, "now", action.ID, "op", action.Op)
	s.events.Publish()
	s.write(w, r, http.StatusAccepted, queuedView{
		ActionID: action.ID, Seq: action.Seq, Machine: action.Machine,
		State: store.Queued, Queued: action.Queued,
	})
}

// dropAction forgets an action that will not be tried again.
func (s *Server) dropAction(w http.ResponseWriter, r *http.Request) {
	id, err := actionID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.state.Drop(id); err != nil {
		s.fail(w, r, queueSide(err))
		return
	}
	s.log.Info("action dropped", "id", id)
	s.events.Publish()
	s.ok(w, r, map[string]any{"dropped": id})
}

// clearBody names what to sweep up. Empty means the done ones, which is the
// housekeeping case and the safe one.
type clearBody struct {
	// States to clear. Only settled ones: clearing is housekeeping over records of
	// things that have already happened. A waiting action has not happened, and
	// stopping one is a decision about that action — so it is cancelled on its own
	// row rather than swept up with a pile.
	States []string `json:"states,omitempty"`
}

func (b *clearBody) Validate() error { return nil }

// clearQueue drops every settled entry in the named states.
//
// It exists because the queue is a log as much as a queue: an action that is done
// stays on the list, and after a busy afternoon the useful rows — the ones that
// failed — are somewhere below fifty that did not. Clearing them one at a time is
// not housekeeping, it is a chore.
//
// The default is `done` alone, deliberately. `failed` and `in_doubt` carry the
// reason they failed, which is the only record of it anywhere; sweeping those away
// by default would make the tidy action the destructive one. A caller that wants
// them gone says so.
func (s *Server) clearQueue(w http.ResponseWriter, r *http.Request) {
	var body clearBody
	if err := decode(r, MaxRequestBytes, &body); err != nil {
		s.fail(w, r, err)
		return
	}

	wanted := map[store.State]bool{}
	if len(body.States) == 0 {
		wanted[store.Done] = true
	}
	for _, raw := range body.States {
		got := store.State(raw)
		if !got.Settled() {
			// Named rather than ignored: a caller asking to sweep the waiting pile
			// has a wrong idea about what clearing is for, and silently doing
			// nothing would leave them with it.
			s.fail(w, r, fault.Usage{Reason: fmt.Sprintf(
				"%q is not a state that can be cleared; clearing tidies away records of what has "+
					"already happened, so only done, failed, and in_doubt can go — an action that is "+
					"still waiting is cancelled on its own row", raw)})
			return
		}
		wanted[got] = true
	}

	entries, err := s.state.Queue()
	if err != nil {
		s.fail(w, r, serverSide(err))
		return
	}

	cleared := 0
	for _, e := range entries {
		if !wanted[e.State] {
			continue
		}
		if err := s.state.Drop(e.Action.ID); err != nil {
			// One that will not go does not stop the rest. A drop races a sync
			// that has just reported, and the honest outcome is "most of them
			// went" rather than a failure that leaves the list half swept.
			s.log.Warn("could not clear queue entry", "id", e.Action.ID, "error", err)
			continue
		}
		cleared++
	}

	s.log.Info("queue cleared", "cleared", cleared)
	if cleared > 0 {
		s.events.Publish()
	}
	s.ok(w, r, map[string]any{"cleared": cleared, "left": len(entries) - cleared})
}

// actionID reads and validates the identifier in the path.
func actionID(r *http.Request) (protocol.ActionID, error) {
	raw := r.PathValue("id")
	if raw == "" {
		return "", fault.Usage{Reason: "no action given"}
	}
	id := protocol.ActionID(raw)
	if err := id.Validate(); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Server) enqueue(w http.ResponseWriter, r *http.Request, machine string, op protocol.Op, args protocol.Args) {
	id := protocol.MachineID(machine)
	if machine == "" {
		var err error
		if id, err = s.machineFor(r); err != nil {
			s.fail(w, r, err)
			return
		}
	} else if err := id.Validate(); err != nil {
		s.fail(w, r, err)
		return
	}

	action, err := s.state.Enqueue(id, op, args, s.now())
	if err != nil {
		s.fail(w, r, serverSide(err))
		return
	}
	s.log.Info("action queued", "id", action.ID, "op", op, "machine", id)
	s.events.Publish()
	s.write(w, r, http.StatusAccepted, queuedView{
		ActionID: action.ID, Seq: action.Seq, Machine: id,
		State: store.Queued, Queued: action.Queued,
	})
}

// --- sync ----------------------------------------------------------------

// sync is the agent's whole conversation with the server: state up, actions
// down, in one round trip.
func (s *Server) sync(w http.ResponseWriter, r *http.Request) {
	var req protocol.SyncRequest
	if err := decode(r, MaxSyncBytes, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	now := s.now()

	// Results first: an action the agent has finished with must be settled
	// before the same batch is offered back to it.
	if err := s.state.Complete(req.Results); err != nil {
		s.fail(w, r, agentSide(err))
		return
	}
	if err := s.state.PutSnapshot(req.Snapshot, req.Agent, now); err != nil {
		s.fail(w, r, agentSide(err))
		return
	}

	pending, err := s.state.Pending(req.Snapshot.Machine)
	if err != nil {
		s.fail(w, r, agentSide(err))
		return
	}
	ids := make([]protocol.ActionID, len(pending))
	for i, a := range pending {
		ids[i] = a.ID
	}
	if err := s.state.MarkSent(ids, now); err != nil {
		s.fail(w, r, serverSide(err))
		return
	}

	s.log.Info("sync", "machine", req.Snapshot.Machine,
		"results", len(req.Results), "actions", len(pending))
	s.events.Publish()

	s.write(w, r, http.StatusOK, protocol.SyncResponse{
		Protocol: protocol.Version, ServerTime: now, Actions: pending,
	})
}
