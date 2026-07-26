package server

import (
	"net/http"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
)

// The task write endpoints.
//
// Reading the pool was already here — every snapshot carries it, and `GET
// /api/v1/tasks` serves it. These are the other half: one route per Macmuffin
// verb that changes something, so the board in the browser can do everything
// `muff` can rather than only show what it did.
//
// They queue, like every other write in cq. The server cannot reach the agent
// machine, so `202 Accepted` is the honest answer: the work is taken, not done,
// and it leaves on the next sync.
//
// The shape is one route per verb rather than a single "run a muff command"
// endpoint, and that is deliberate. A pass-through would make the queue a list of
// command lines nobody can report on, would put argument checking on the far side
// of a sync, and would turn every future Macmuffin flag into something the browser
// could invoke without anyone deciding it should.

// taskBody is the operand set every task route reads. Each handler takes the
// fields its verb uses and refuses the rest through protocol.Action.Validate, so
// there is one definition of what an operation takes and it is the protocol's.
type taskBody struct {
	Machine string `json:"machine,omitempty"`
	// User is the agent for assign, invite, and kick.
	User string `json:"user,omitempty"`
	// Sub is the subtask for complete and delete, and the name for a new one.
	Sub string `json:"sub,omitempty"`
	// Paths is the whole new scope: `muff scope` replaces rather than adds, and
	// so does this.
	Paths []string `json:"paths,omitempty"`
	// Path is the worktree.
	Path string `json:"path,omitempty"`
	// Text is a description: the whole new markdown, replacing whatever was there.
	Text       string `json:"text,omitempty"`
	Priority   int    `json:"priority,omitempty"`
	Difficulty int    `json:"difficulty,omitempty"`
	Status     int    `json:"status,omitempty"`
	Force      bool   `json:"force,omitempty"`
	// Name is the task, for the one route that has no task in its path — creating
	// one is the only verb whose subject does not exist yet.
	Name string `json:"name,omitempty"`
}

// Validate is deliberately empty, and this is the one place in cq where that is
// the right answer.
//
// Which operands a task verb requires — and which it must not carry — is
// protocol.argRules, checked by Action.Validate inside Enqueue before anything is
// written. Restating those rules here would be a second copy to keep in step, and
// the copy that drifts is always the one nearer the wire.
func (b *taskBody) Validate() error { return nil }

// taskAction reads the body, takes the task from the path, and queues one
// operation. Every route below is one line of arguments and this.
func (s *Server) taskAction(w http.ResponseWriter, r *http.Request, op protocol.Op,
	fill func(body taskBody, args *protocol.Args)) {
	var body taskBody
	if err := decode(r, MaxRequestBytes, &body); err != nil {
		s.fail(w, r, err)
		return
	}

	name := r.PathValue("name")
	if name == "" {
		name = body.Name
	}
	if name == "" {
		s.fail(w, r, fault.Usage{Reason: "no task given"})
		return
	}

	args := protocol.Args{Task: name}
	if fill != nil {
		fill(body, &args)
	}
	s.enqueue(w, r, body.Machine, op, args)
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	s.taskAction(w, r, protocol.OpTaskCreate, func(b taskBody, a *protocol.Args) {
		a.Priority, a.Difficulty = b.Priority, b.Difficulty
	})
}

func (s *Server) pushTask(w http.ResponseWriter, r *http.Request) {
	s.taskAction(w, r, protocol.OpTaskPush, nil)
}

func (s *Server) claimTask(w http.ResponseWriter, r *http.Request) {
	s.taskAction(w, r, protocol.OpTaskClaim, nil)
}

func (s *Server) leaveTask(w http.ResponseWriter, r *http.Request) {
	s.taskAction(w, r, protocol.OpTaskLeave, nil)
}

func (s *Server) assignTask(w http.ResponseWriter, r *http.Request) {
	s.taskAction(w, r, protocol.OpTaskAssign, func(b taskBody, a *protocol.Args) { a.User = b.User })
}

func (s *Server) inviteToTask(w http.ResponseWriter, r *http.Request) {
	s.taskAction(w, r, protocol.OpTaskInvite, func(b taskBody, a *protocol.Args) { a.User = b.User })
}

func (s *Server) kickFromTask(w http.ResponseWriter, r *http.Request) {
	s.taskAction(w, r, protocol.OpTaskKick, func(b taskBody, a *protocol.Args) { a.User = b.User })
}

func (s *Server) scopeTask(w http.ResponseWriter, r *http.Request) {
	s.taskAction(w, r, protocol.OpTaskScope, func(b taskBody, a *protocol.Args) { a.Paths = b.Paths })
}

// describeTask replaces the prose that says what the work is.
//
// PUT rather than POST: it replaces a whole document, and the same body twice lands
// in the same place. DELETE clears it — a description removed is not a description
// set to nothing, and the queue has to be able to say which happened.
func (s *Server) describeTask(w http.ResponseWriter, r *http.Request) {
	s.taskAction(w, r, protocol.OpTaskDescribe, func(b taskBody, a *protocol.Args) { a.Text = b.Text })
}

func (s *Server) undescribeTask(w http.ResponseWriter, r *http.Request) {
	s.taskAction(w, r, protocol.OpTaskDescribeClear, nil)
}

func (s *Server) worktreeTask(w http.ResponseWriter, r *http.Request) {
	s.taskAction(w, r, protocol.OpTaskWorktree, func(b taskBody, a *protocol.Args) { a.Path = b.Path })
}

func (s *Server) statusTask(w http.ResponseWriter, r *http.Request) {
	s.taskAction(w, r, protocol.OpTaskStatus, func(b taskBody, a *protocol.Args) { a.Status = b.Status })
}

func (s *Server) addSubtask(w http.ResponseWriter, r *http.Request) {
	s.taskAction(w, r, protocol.OpTaskSubtask, func(b taskBody, a *protocol.Args) { a.Sub = b.Sub })
}

func (s *Server) completeTask(w http.ResponseWriter, r *http.Request) {
	s.taskAction(w, r, protocol.OpTaskComplete, func(b taskBody, a *protocol.Args) {
		a.Sub, a.Force = b.Sub, b.Force
	})
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	s.taskAction(w, r, protocol.OpTaskDelete, func(b taskBody, a *protocol.Args) { a.Sub = b.Sub })
}
