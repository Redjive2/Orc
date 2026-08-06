package session

import "testing"

// A client must wait longer than the server can take.
//
// The two numbers used to be one — HandshakeDeadline served as both the server's
// bound on a silent client and the client's bound on a slow reply — and they
// measure different things. The client's has to outlast the confirmation ladder,
// which waits ConfirmWithin up to three times before the reply is written.
//
// The failure past that margin is the one that matters. `ask` returns
// fault.Unavailable when the read times out, cli.transient reads that as "not
// yet", and keepTrying re-sends the whole message — so a poke that was slow and
// *successful* is delivered twice, to an agent that acts on it twice. The whole
// of `confirm` is ordered to prevent exactly that; a client-side retry walks past
// it.
func TestAClientWaitsLongerThanThePokeCanTake(t *testing.T) {
	worst := WorstConfirm()
	if ReplyDeadline <= worst {
		t.Fatalf("ReplyDeadline is %s and a poke can take %s: a slow-but-successful "+
			"poke will time out on the client and be sent again", ReplyDeadline, worst)
	}
	// And with room to spare, because the ladder's waits are not the only thing in
	// there — every rung parses the event feed and writes to a pty, and on the
	// machine where this matters those are not free.
	if slack := ReplyDeadline - worst; slack < 2*ConfirmWithin {
		t.Errorf("only %s of slack over a %s worst case; the feed parses and pty "+
			"writes inside the ladder come out of that", slack, worst)
	}
}

// The old arrangement, kept as a statement of what was wrong: the handshake
// bound is *not* enough to cover the ladder. If someone folds the two constants
// back together, this says why they cannot be.
func TestTheHandshakeBoundWouldNotCoverAPoke(t *testing.T) {
	if HandshakeDeadline > WorstConfirm()+ConfirmWithin {
		t.Skip("the handshake bound is now generous enough that sharing it would be safe")
	}
	if HandshakeDeadline > WorstConfirm() {
		t.Logf("note: the handshake bound (%s) exceeds the worst case (%s) by only %s",
			HandshakeDeadline, WorstConfirm(), HandshakeDeadline-WorstConfirm())
	}
}
