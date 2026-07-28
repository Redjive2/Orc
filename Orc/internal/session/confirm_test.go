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

	done := make(chan error, 1)
	go func() { done <- sup.Poke("only once") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a poke on a fleet that cannot report was refused: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a poke on a fleet that cannot report waited to be confirmed")
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
