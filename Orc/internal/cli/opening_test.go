package cli_test

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"strings"
	"sync"
	"testing"

	"orc/common/user"
	"orc/orc/internal/session"
)

// What a session is told when it starts.
//
// A Claude session that nobody has spoken to does nothing. It has the fleet's, the
// role's, and the identity's standing instructions in its system prompt — `employ`
// passes them on the command line — and no turn in which to act on any of them: it
// sits at its prompt, and the wake cycle does not call that silence until the quiet
// threshold has passed. An agent employed at midnight and found idle in the morning
// is an agent nobody ever said anything to.

// listener records what is poked into it, and answers the protocol well enough to
// be poked. Its own rather than proxy_test.go's, which is about attaching.
type listener struct {
	t    *testing.T
	path string

	mu    sync.Mutex
	on    net.Listener
	poked []string
}

// poking listens where the rig says a session will, and listens again every time
// the rig starts one — because depopulating unlinks the socket, and a refresh is a
// depopulate followed by a populate.
func poking(t *testing.T, r *rig, who string) *listener {
	t.Helper()

	path := mustStore(t, r).SocketPath(mustName(t, who))
	l := &listener{t: t, path: path}
	t.Cleanup(l.close)
	r.started = func(user.Name) { l.bind() }
	l.bind()
	return l
}

// bind takes the socket path, replacing whatever was there.
func (l *listener) bind() {
	l.close()
	_ = os.Remove(l.path)

	on, err := net.Listen("unix", l.path)
	if err != nil {
		l.t.Fatalf("listening on %s: %v", l.path, err)
	}
	l.mu.Lock()
	l.on = on
	l.mu.Unlock()
	go l.serve(on)
}

func (l *listener) close() {
	l.mu.Lock()
	on := l.on
	l.on = nil
	l.mu.Unlock()
	if on != nil {
		_ = on.Close()
	}
}

func (l *listener) serve(on net.Listener) {
	for {
		conn, err := on.Accept()
		if err != nil {
			return
		}
		go l.handle(conn)
	}
}

func (l *listener) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		// Dial's probe opens and closes without saying anything.
		return
	}
	var req session.Request
	if err := json.Unmarshal(line, &req); err != nil {
		return
	}
	if req.Op == session.OpPoke {
		l.mu.Lock()
		l.poked = append(l.poked, req.Text)
		l.mu.Unlock()
	}
	data, _ := json.Marshal(session.Reply{OK: true})
	_, _ = conn.Write(append(data, '\n'))
}

func (l *listener) said() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.poked...)
}

func TestEmployTellsTheNewSessionToBegin(t *testing.T) {
	r := fullFleet(t)
	heard := poking(t, r, "ember")

	got := r.ok("boss", "employ", "ember")
	if !strings.Contains(got.stdout, "told to begin") {
		t.Errorf("employ did not say it spoke to the session:\n%s%s", got.stdout, got.stderr)
	}
	if said := heard.said(); len(said) != 1 {
		t.Fatalf("the session was poked %d times, want 1: %q", len(said), said)
	}
}

func TestRefreshTellsTheNewSessionToBegin(t *testing.T) {
	r := fullFleet(t)
	heard := poking(t, r, "ember")
	r.ok("boss", "employ", "ember")

	got := r.ok("boss", "refresh", "ember")
	if !strings.Contains(got.stdout, "told to begin") {
		t.Errorf("refresh did not say it spoke to the session:\n%s%s", got.stdout, got.stderr)
	}
	// Once for the employ and once for the refresh: a fresh conversation is a
	// conversation nobody has said anything in, whichever verb made it.
	if said := heard.said(); len(said) != 2 {
		t.Fatalf("the session was poked %d times, want 2: %q", len(said), said)
	}
}

// What it says is the wake message, so a fleet that has written what to tell an
// idle agent has already written this. Two settings to keep in step would be one
// too many.
func TestTheOpeningTurnIsTheWakeMessage(t *testing.T) {
	r := fullFleet(t)
	heard := poking(t, r, "ember")
	r.ok("boss", "instruct", "wake", "--set", file(t, "pick up the parser work"))

	r.ok("boss", "employ", "ember")
	said := heard.said()
	if len(said) != 1 || said[0] != "pick up the parser work" {
		t.Errorf("the opening turn was %q, want the fleet's wake message", said)
	}
}

// Nothing listening is not a failed employ. The session is up, which is what was
// asked for; the wake cycle is the backstop, and a fleet that refused to employ an
// agent because it could not immediately say hello would be worse in every case.
func TestASilentSocketDoesNotFailTheEmploy(t *testing.T) {
	r := fullFleet(t)

	got := r.ok("boss", "employ", "ember")
	if !strings.Contains(got.stdout, "employed") {
		t.Errorf("the employ did not happen:\n%s%s", got.stdout, got.stderr)
	}
	if !strings.Contains(got.stderr, "could not be told to begin") {
		t.Errorf("it did not say the opening turn was lost:\n%s", got.stderr)
	}
}

// A session the *backstop* built is a session nobody has spoken to either.
//
// Which message a new session gets used to depend on who asked for it: `employ` said
// "opening" and `tend` said nothing, so a fresh session started by a backstop was
// never told to begin. That is the case it matters most in — a fleet nobody is
// watching, an agent whose session went away, and a `tend --watch` that brings it
// back to sit silently at its prompt until the wake cycle notices a whole interval
// later.
//
// It follows from the session now, not from the caller: resumed conversations carry
// on, and new ones are told to begin, whoever made them.
func TestTendTellsAFreshSessionToBegin(t *testing.T) {
	r := fullFleet(t)
	heard := poking(t, r, "ember")

	r.ok("boss", "employ", "ember")

	// The session goes away with no ending recorded, which is what a machine that
	// was restarted — or a store that lost the record — leaves behind: still on the
	// worklist, nothing running, and no conversation to resume. `tend` has to build
	// a new one, and a new one has never been spoken to.
	store := mustStore(t, r)
	who := mustName(t, "ember")
	if err := store.RemoveSession(who); err != nil {
		t.Fatal(err)
	}
	if err := store.ForgetEnded(who); err != nil {
		t.Fatal(err)
	}
	before := len(heard.said())

	got := r.ok("boss", "tend")
	said := heard.said()
	if len(said) <= before {
		t.Fatalf("a session tend started fresh was never spoken to: %q (%s%s)",
			said, got.stdout, got.stderr)
	}
}
