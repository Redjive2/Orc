package session_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"orc/orc/internal/pty"
	"orc/orc/internal/session"
)

// Writing into a pty is not delivery.
//
// A write to the master succeeds whether or not anything is listening on the other
// end, and measured against the real binary a message written while it is starting
// is dropped — sometimes entirely, sometimes only its submitting return, which
// leaves the text sitting in the box unsent. That second one is what an operator
// reports as "it was already loaded, just unsent". Orc could not tell either from a
// delivery that worked, because it never asked.
//
// It asks now, using the `UserPromptSubmit` hook it already installs. These pin the
// part that is easy to get dangerously wrong: what it does when the answer is no.

// feedFor writes an event feed for a session, so confirmation has something to read.
func feedFor(t *testing.T, path, id string, lines ...string) {
	t.Helper()
	body := strings.Join(lines, "\n")
	if body != "" {
		body += "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func started(id string) string {
	return `{"at":"2026-07-27T10:00:00.000Z","session":"` + id + `","event":"SessionStart"}`
}

// The order of the two retries is the whole safety of the thing. A bare return
// carries no content, so it cannot duplicate anything — it is tried first, and it
// fixes the loaded-but-unsent case outright. Sending the message again is the second
// rung and only reached when the first changed nothing, which means the box was
// empty and the text never landed.
//
// The other order would deliver the message twice every time the first attempt was
// merely unsent, and an agent acting twice on one instruction is worse than an agent
// that missed it.
func TestAnUnconfirmedPokeSubmitsBeforeItRepeats(t *testing.T) {
	s, who := fleet(t, "ember")
	sup, id := start(t, s, who, nil)

	state, _, _ := s.Session(who)
	client, err := session.Dial(state.Socket)
	if err != nil {
		t.Fatalf("dialling: %v", err)
	}
	conn, err := client.Attach(pty.Sane())
	if err != nil {
		t.Fatalf("attaching: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// The session reports, but never reports submitting anything — so every rung of
	// the ladder is walked and the poke ends up refused.
	feedFor(t, s.EventsPath(who), id, started(id))

	err = sup.Poke("wake up please")
	if err == nil {
		t.Fatal("a poke nothing ever acknowledged was reported as delivered")
	}
	if !strings.Contains(err.Error(), "never reported submitting") {
		t.Errorf("the refusal does not say what went wrong: %v", err)
	}

	// Exactly once. The bare return between the two attempts is invisible here —
	// which is the point of trying it first.
	got := readUntil(t, conn, 5*time.Second, "wake up please")
	if n := strings.Count(got, "wake up please"); n != 2 {
		// Twice: the fake echoes what it is given, so one write shows as the
		// keystrokes and the echo of them.
		t.Logf("the session saw the message %d times: %q", n, got)
	}
}

// A fleet whose hooks are not installed reports nothing, and absence of a submission
// means nothing there. Retrying against that would deliver every prompt to every
// agent twice, for ever — so a session that has never written an event is written to
// once and believed, exactly as before.
func TestAPokeIsNotRepeatedWhereNothingCanReport(t *testing.T) {
	s, who := fleet(t, "ember")
	sup, _ := start(t, s, who, nil)

	// No feed at all: this fleet cannot say whether anything arrived.
	_ = os.Remove(s.EventsPath(who))

	// The first poke waits once for the session to say anything, because a session
	// that has only just started has not reported *yet* — that wait is the whole fix
	// for an opening message going into a terminal before it is listening.
	if err := sup.Poke("only once"); err != nil {
		t.Fatalf("a poke on a fleet that cannot report was refused: %v", err)
	}

	// And never again. Paying that wait on every message would put half a minute on
	// a wake cycle's first pass over a fleet of seven.
	done := make(chan error, 1)
	go func() { done <- sup.Poke("and again") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the second poke was refused: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a fleet that cannot report was waited for twice")
	}
}

// And the ordinary case: the session says it submitted, so nothing is retried and
// nothing waits.
func TestAConfirmedPokeReturnsAtOnce(t *testing.T) {
	s, who := fleet(t, "ember")
	sup, id := start(t, s, who, nil)

	// Already one submission on the feed; the poke's own will make two. Written
	// ahead of time because the fake agent runs no hooks — what is being pinned is
	// that Poke reads the feed and stops as soon as the count moves.
	go func() {
		time.Sleep(200 * time.Millisecond)
		feedFor(t, s.EventsPath(who), id, started(id),
			`{"at":"2026-07-27T10:00:01.000Z","session":"`+id+`","event":"UserPromptSubmit","turn":1}`)
	}()
	feedFor(t, s.EventsPath(who), id, started(id))

	start := time.Now()
	if err := sup.Poke("hello"); err != nil {
		t.Fatalf("a poke the session acknowledged was refused: %v", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("an acknowledged poke took %s; it should stop as soon as the count moves", took)
	}
}

// The asymmetry this was written for: `orc poke` got messages through and the
// opening message did not.
//
// Both go through the same Poke. What differed was *when*. A person runs `poke`
// against a session that has been up for a while, so its feed has events, so
// delivery was confirmed and the ladder rescued anything the terminal dropped.
// The opening message is sent the moment `Populate` returns — which is when the
// supervisor's *state file* says the session is live, before Claude has done
// anything at all — so the feed was empty, confirmation switched itself off, and
// the message went into a terminal that drops input for its first second with
// nothing left to notice.
//
// A session that has not reported yet is waited for now, and the first event is
// the readiness signal there was no other way to get.
func TestAPokeWaitsForANewSessionToSayAnything(t *testing.T) {
	s, who := fleet(t, "ember")
	sup, id := start(t, s, who, nil)

	// Nothing on the feed when the poke starts, exactly as an opening message finds
	// it. The session's start arrives while the poke is waiting, and nothing is ever
	// submitted — so a poke that confirmed refuses, and one that skipped confirming
	// reports success. That is what tells the two apart: under the old guard this
	// returned nil, which is the whole failure written down.
	_ = os.Remove(s.EventsPath(who))
	go func() {
		time.Sleep(300 * time.Millisecond)
		feedFor(t, s.EventsPath(who), id, started(id))
	}()

	err := sup.Poke("begin")
	if err == nil {
		t.Fatal("an opening message was reported as delivered without the session ever saying so")
	}
	if !strings.Contains(err.Error(), "never reported submitting") {
		t.Errorf("it refused for the wrong reason: %v", err)
	}
}

// And the ordinary end of the same case: the session starts, the message lands, and
// the poke returns without a word.
func TestAnOpeningMessageThatLandsIsNotRetried(t *testing.T) {
	s, who := fleet(t, "ember")
	sup, id := start(t, s, who, nil)

	_ = os.Remove(s.EventsPath(who))
	go func() {
		time.Sleep(200 * time.Millisecond)
		feedFor(t, s.EventsPath(who), id, started(id))
		time.Sleep(200 * time.Millisecond)
		feedFor(t, s.EventsPath(who), id, started(id),
			`{"at":"2026-07-27T10:00:01.000Z","session":"`+id+`","event":"UserPromptSubmit","turn":1}`)
	}()

	if err := sup.Poke("begin"); err != nil {
		t.Fatalf("an opening message the session acknowledged was refused: %v", err)
	}
}
