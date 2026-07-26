package control

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"orc/common/fault"
	"orc/common/user"
)

// Asking whether the caller is the fleet's operator.
//
// A task nobody owns is a task nobody can direct: `scope`, `complete`, `invite`
// and `delete` are the owner's, and with no owner they refuse and say "claim it
// first". That is right for an agent — taking the work is how you get the say
// over it — and wrong for the person running the fleet, who has to be able to
// retire a stale task, fix a wrong scope, or hand one on without first making
// themselves its owner and then having to give it away again.
//
// Macmuffin has no view of the fleet and should not grow one, so the question
// goes where the answer lives: `orc introspect --only operator` prints the
// identity Orc holds as the operator, and Macmuffin compares it with the caller.
//
// This **fails closed**, and for the opposite reason to the identity check.
// Verification only ever *refuses*, so an absent Orc leaves the claim standing;
// this only ever *widens*, so an absent Orc must leave it exactly as narrow as
// it was. On a machine with no fleet nobody is the operator and Macmuffin
// behaves as it always has.

// FieldOperator is the introspect field naming the fleet's operator.
const FieldOperator = "operator"

// Unasked reports that no authority was available to answer.
//
// Its own type rather than Unverifiable's, because the two say different things
// and a caller that mixed them would report the wrong one: Unverifiable means
// nobody could confirm who the caller is, and this means nobody could say who the
// fleet answers to. Both are "no answer" and neither is "no".
type Unasked struct {
	Reason string
}

func (e Unasked) Error() string { return "the fleet has no operator to compare against: " + e.Reason }

// Unwrap makes this exit 10 if it ever reaches the top. Nothing returns it that
// far — permit treats it as a no and mentions it — but the code is right for what
// it is.
func (e Unasked) Unwrap() error { return fault.ErrUnavailable }

// Operating reports whether a claimed identity is the fleet's operator.
//
// The error is never a refusal of the command: it says nobody could be asked,
// which the caller reports and treats as a no.
type Operating func(claimed user.Name) (bool, error)

// Operator asks the real `orc` binary, within Deadline.
func Operator(claimed user.Name) (bool, error) { return OperatorWithin(Deadline, claimed) }

// OperatorWithin is Operator with the deadline given.
func OperatorWithin(deadline time.Duration, claimed user.Name) (bool, error) {
	if claimed.Zero() {
		return false, fault.Internal{Where: "control.Operator", Detail: "no identity claimed"}
	}

	if _, err := exec.LookPath(Binary); err != nil {
		return false, Unasked{Reason: Binary + " is not installed, so no fleet has an operator"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, Binary, "introspect", "--only", FieldOperator)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return false, Unasked{Reason: fmt.Sprintf("%s could not be run: %v", Binary, err)}
		}
		// Every non-zero exit is "nobody said yes", including exit 7. A
		// credential Orc will not accept is not the operator's, and the identity
		// check has already refused the command by the time this is reached.
		return false, Unasked{Reason: fmt.Sprintf("%s exited %d: %s", Binary, exit.ExitCode(),
			reasonOr(stderr.String(), "no reason given"))}
	}

	named := strings.TrimSpace(stdout.String())
	if named == "" {
		// A fleet with no operator is not a fleet, but it is not this package's
		// business to say so — `orc doctor` is. An empty answer is a no.
		return false, nil
	}
	got, err := user.Parse(named)
	if err != nil {
		return false, Unasked{Reason: Binary + " named an operator that will not parse: " + err.Error()}
	}
	return got.String() == claimed.String(), nil
}
