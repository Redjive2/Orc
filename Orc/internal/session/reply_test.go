package session

import (
	"errors"
	"testing"

	"orc/common/fault"
)

// A refusal keeps its kind on the way across the socket.
//
// The wire carries a *string*, so before `Again` a client rebuilt every refusal as
// `errors.New(reply.Error)` and could not tell "not yet" from "no". That made a poke
// during a restart — an agent that is coming back on its own, this second — the one
// case a retry never covered, which is the case retries exist for.

func TestATemporaryRefusalSaysSo(t *testing.T) {
	got := replyFor(fault.Unavailable{Peer: "ember", Err: errors.New("the session is restarting")})
	if got.OK {
		t.Fatal("a refusal was reported as success")
	}
	if !got.Again {
		t.Error("a session that is restarting did not ask to be tried again")
	}
}

func TestADecisionDoesNotAskToBeRetried(t *testing.T) {
	for _, decided := range []error{
		fault.Usage{Reason: "that message cannot be typed"},
		fault.Conflict{Path: "ember", Reason: "already stopping"},
		fault.Internal{Where: "session.Poke", Detail: "no pty"},
		errors.New("something else entirely"),
	} {
		if replyFor(decided).Again {
			t.Errorf("%T asked to be retried; a decision is answered once", decided)
		}
	}
}

func TestSuccessCarriesNeither(t *testing.T) {
	got := replyFor(nil)
	if !got.OK || got.Again || got.Error != "" {
		t.Errorf("a success came back as %+v", got)
	}
}

// The other half: what the client rebuilds. A reply marked Again has to come back as
// something `transient` recognises, or the flag travels and changes nothing.
func TestAnAgainReplyRebuildsAsUnavailable(t *testing.T) {
	reply := Reply{Error: "the session is restarting", Again: true}
	err := rebuild(reply)

	var unavailable fault.Unavailable
	if !errors.As(err, &unavailable) {
		t.Fatalf("an Again reply rebuilt as %T, which no caller will retry", err)
	}
	if got := rebuild(Reply{Error: "that message cannot be typed"}); errors.As(got, &unavailable) {
		t.Error("a decision rebuilt as something a caller will spin on")
	}
}
