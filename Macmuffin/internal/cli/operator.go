package cli

import (
	"errors"

	"orc/common/user"
	"orc/macmuffin/internal/control"
	"orc/macmuffin/internal/policy"
	"orc/macmuffin/internal/task"
)

// The operator's standing over unowned tasks, as the CLI sees it.
//
// The check costs a subprocess — `orc introspect --only operator` — so it is
// asked **only when the answer would change something**: after the policy table
// has refused, and only for the actions §policy.OperatorMay says are the unowned
// case. An agent doing ordinary work never runs it, and the operator runs it once
// per command however many tasks that command touches.

// standing memoises the answer for one command.
//
// A pointer on the session, because a session is passed by value and an answer
// worked out in one method would otherwise be forgotten by the next.
type standing struct {
	asked bool
	is    bool
	// why records what stopped an answer, so a refusal can say that nobody could
	// be asked rather than implying the caller was told no.
	why string
}

// operating reports whether the caller is the fleet's operator.
func (s session) operating() bool {
	if s.standing == nil {
		return false
	}
	if s.standing.asked {
		return s.standing.is
	}
	s.standing.asked = true

	ask := s.app.Operator
	if ask == nil {
		ask = control.Operator
	}
	is, err := ask(s.who)
	if err != nil {
		var unasked control.Unasked
		if errors.As(err, &unasked) {
			s.standing.why = unasked.Reason
		} else {
			s.standing.why = err.Error()
		}
		return false
	}
	s.standing.is = is
	return is
}

// permit is what every command asks instead of policy.Allows.
//
// The table decides first and decides most things. Where it refuses, and where
// the refusal is the unowned-task one, the fleet's operator stands in for the
// owner it does not have.
//
// The note is not decoration. Acting as the operator is a different act from
// acting as an owner — it leaves the task unowned, so the next reader sees a task
// that changed with nobody on it — and a command that did that silently would
// make the journal harder to read than the screen.
func (s session) permit(t task.Task, action policy.Action) error {
	err := policy.Allows(s.who, t, action)
	if err == nil {
		return nil
	}
	if !policy.OperatorMay(s.who, t, action) {
		return err
	}
	if !s.operating() {
		// Where nobody could be asked, say so beside the refusal. The refusal
		// itself is unchanged and correct — this only explains why the one thing
		// that might have lifted it did not get the chance.
		if s.standing != nil && s.standing.why != "" {
			s.app.note("the fleet could not be asked whether you are the operator: %s", s.standing.why)
		}
		return err
	}
	s.app.note("nobody owns %s; acting as the operator", t.Name())
	return nil
}

// operatorNote is what `verify` says about this standing.
func operatorNote(who user.Name, is bool) string {
	if is {
		return who.String() + " is the fleet's operator, so unowned tasks answer to it"
	}
	return ""
}
