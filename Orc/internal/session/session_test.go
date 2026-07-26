package session_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"orc/common/clock"
	"orc/common/user"
	"orc/orc/internal/model"
	"orc/orc/internal/pty"
	"orc/orc/internal/session"
	"orc/orc/internal/store"
)

var epoch = time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)

// fakeClaude builds the stand-in once per run and returns its path.
//
// Nothing in this package's tests starts a real Claude session: it would need a
// credential, cost money, and make every test a network test. The fake is a real
// program because what is being tested is a *terminal* — output reaching an
// attacher, a poke arriving as keystrokes, a crash being restarted — and a shell
// script could not be told to ignore SIGTERM on purpose.
var fakeClaude = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "orc-fake-claude")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "claude")
	cmd := exec.Command("go", "build", "-o", bin, "orc/orc/internal/fixture/claude")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("building the fake claude: %w\n%s", err, out)
	}
	return bin, nil
})

// fleet builds a store with one employed identity, and returns the supervisor's
// spec ready to run.
func fleet(t *testing.T, name string) (*store.Store, user.Name) {
	t.Helper()

	// A short root: a socket path has to fit in sun_path, and a test's temporary
	// directory on darwin is long enough that the store's own fallback would kick
	// in. Both paths are tested — see TestSocketPathFallback.
	root, err := os.MkdirTemp("/tmp", "orc")
	if err != nil {
		t.Fatalf("temp root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	s, err := store.Create(filepath.Join(root, "f"), clock.NewFake(epoch, time.Second))
	if err != nil {
		t.Fatalf("creating the store: %v", err)
	}

	who, err := user.Parse(name)
	if err != nil {
		t.Fatalf("name: %v", err)
	}
	if _, err := s.CreateIdentity(who, "0000000a-00000001", user.Name{}); err != nil {
		t.Fatalf("identity: %v", err)
	}
	key, err := user.NewKey(nil)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	if err := s.WriteCredential(who, key); err != nil {
		t.Fatalf("credential: %v", err)
	}
	return s, who
}

// start runs a supervisor in the background and waits for its session to appear.
func start(t *testing.T, s *store.Store, who user.Name, env map[string]string) (*session.Supervisor, string) {
	t.Helper()

	bin, err := fakeClaude()
	if err != nil {
		t.Fatalf("fake claude: %v", err)
	}
	id, err := session.NewID()
	if err != nil {
		t.Fatalf("session id: %v", err)
	}

	composed, err := session.Environment(s, who, id)
	if err != nil {
		t.Fatalf("environment: %v", err)
	}
	for k, v := range env {
		composed = append(composed, k+"="+v)
	}

	sup, err := session.New(s, session.Spec{
		Identity: who, ID: id, Model: model.ModelSonnet, Effort: model.EffortMedium,
	}, composed, bin)
	if err != nil {
		t.Fatalf("supervisor: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- sup.Run() }()
	t.Cleanup(func() {
		sup.Stop()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Errorf("the supervisor did not exit after Stop")
		}
	})

	waitFor(t, 5*time.Second, "the session to come up", func() bool {
		_, live, err := s.Session(who)
		return err == nil && live
	})
	return sup, id
}

func waitFor(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", within, what)
}

// TestSessionComesUp: the supervisor starts a child in a pty, records the session,
// and the state it records is true — the pids are real and the id is the one Orc
// minted.
func TestSessionComesUp(t *testing.T) {
	s, who := fleet(t, "ember")
	_, id := start(t, s, who, nil)

	state, live, err := s.Session(who)
	if err != nil || !live {
		t.Fatalf("no live session: %v", err)
	}
	if state.ID != id {
		t.Errorf("the session id is %q, want the one orc minted (%q)", state.ID, id)
	}
	if state.Supervisor != os.Getpid() {
		t.Errorf("the supervisor pid is %d, want this process (%d)", state.Supervisor, os.Getpid())
	}
	if state.Child <= 0 {
		t.Errorf("no child pid was recorded")
	}
	if state.Model != "sonnet" || state.Effort != "medium" {
		t.Errorf("the session records %s/%s, want sonnet/medium", state.Model, state.Effort)
	}
}

// TestAttachSeesHistoryAndLive: an attach is shown what happened before it arrived
// and then what happens next. The first half is the scrollback, and it is what keeps
// an attach from looking like a hung agent.
func TestAttachSeesHistoryAndLive(t *testing.T) {
	s, who := fleet(t, "ember")
	start(t, s, who, map[string]string{"FAKE_CLAUDE_GREETING": "the-greeting"})

	state, _, err := s.Session(who)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	client, err := session.Dial(state.Socket)
	if err != nil {
		t.Fatalf("dialling: %v", err)
	}

	// Wait for the greeting to reach the scrollback before attaching, so this test
	// is about history rather than about timing.
	conn, err := client.Attach(pty.WinSize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("attaching: %v", err)
	}
	defer func() { _ = conn.Close() }()

	got := readUntil(t, conn, 5*time.Second, "the-greeting")
	if !strings.Contains(got, "the-greeting") {
		t.Errorf("the attach did not see the scrollback: %q", got)
	}
	// The command line is in there too, which is how a test pins the flags Orc
	// passes without reaching inside the supervisor.
	if !strings.Contains(got, "--session-id "+state.ID) {
		t.Errorf("the session was not started with its own id: %q", got)
	}

	// Now something live: a poke should arrive as keystrokes and come back out.
	if err := client.Poke("hello there"); err != nil {
		t.Fatalf("poking: %v", err)
	}
	if got := readUntil(t, conn, 5*time.Second, "you said: hello there"); !strings.Contains(got, "you said: hello there") {
		t.Errorf("the poke did not reach the session as keystrokes: %q", got)
	}
}

// TestPokeIsBracketedWhenMultiline: a multi-line message is one message.
//
// Without the bracketed paste a TUI submits at the first newline, so a composed
// buffer would arrive as several separate turns. The fake echoes per line, so what
// this asserts is that the escape sequences are there.
func TestPokeIsBracketedWhenMultiline(t *testing.T) {
	s, who := fleet(t, "ember")
	sup, _ := start(t, s, who, nil)

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

	if err := sup.Poke("first line\nsecond line"); err != nil {
		t.Fatalf("poking: %v", err)
	}
	got := readUntil(t, conn, 5*time.Second, "second line")
	if !strings.Contains(got, "\x1b[200~") {
		t.Errorf("a multi-line poke was not sent as a bracketed paste: %q", got)
	}
}

// TestRestartKeepsTheSessionID is the distinction the whole design turns on: a crash
// resumes the same conversation, because nobody asked for a new agent.
func TestRestartKeepsTheSessionID(t *testing.T) {
	s, who := fleet(t, "ember")
	_, id := start(t, s, who, nil)

	before, _, _ := s.Session(who)

	// `quit` makes the fake exit cleanly, which is a session ending on its own — the
	// case the supervisor has to restart.
	state, _, _ := s.Session(who)
	client, err := session.Dial(state.Socket)
	if err != nil {
		t.Fatalf("dialling: %v", err)
	}
	if err := client.Poke("quit"); err != nil {
		t.Fatalf("poking: %v", err)
	}

	waitFor(t, 15*time.Second, "the session to be restarted", func() bool {
		got, live, err := s.Session(who)
		return err == nil && live && got.Child != before.Child && got.Restarts > 0
	})

	after, _, _ := s.Session(who)
	if after.ID != id {
		t.Errorf("the restart changed the session id from %q to %q; a crash is not a refresh", id, after.ID)
	}
	if after.Child == before.Child {
		t.Errorf("the child was not restarted")
	}
	if after.LastExit == "" {
		t.Errorf("the restart did not record why the last one ended")
	}
}

// TestOneSupervisorPerIdentity: the session lock makes it a fact rather than a
// convention. A second supervisor refuses to start rather than racing the first for
// a pty.
func TestOneSupervisorPerIdentity(t *testing.T) {
	s, who := fleet(t, "ember")
	start(t, s, who, nil)

	bin, err := fakeClaude()
	if err != nil {
		t.Fatalf("fake claude: %v", err)
	}
	id, err := session.NewID()
	if err != nil {
		t.Fatalf("id: %v", err)
	}
	second, err := session.New(s, session.Spec{
		Identity: who, ID: id, Model: model.ModelHaiku, Effort: model.EffortLow,
	}, os.Environ(), bin)
	if err != nil {
		t.Fatalf("second supervisor: %v", err)
	}
	if err := second.Run(); err == nil {
		t.Errorf("a second supervisor for one identity started")
	}
}

// TestStopEscalates: a child that ignores SIGTERM is killed after the grace period,
// so `orc fire` cannot be defeated by an agent that will not leave.
func TestStopEscalates(t *testing.T) {
	if testing.Short() {
		t.Skip("this waits out the grace period")
	}
	s, who := fleet(t, "ember")
	sup, _ := start(t, s, who, map[string]string{"FAKE_CLAUDE_HANG": "1"})

	state, _, _ := s.Session(who)
	child := state.Child

	go sup.Stop()
	waitFor(t, session.GraceStop+5*time.Second, "the stubborn child to be killed", func() bool {
		return !processAlive(child)
	})
}

// TestDepopulateTidiesUp: the state file and the socket both go, because a socket
// with nothing behind it is something an attach would wait on forever.
func TestDepopulateTidiesUp(t *testing.T) {
	s, who := fleet(t, "ember")
	start(t, s, who, nil)

	state, _, _ := s.Session(who)
	if err := session.Depopulate(s, who); err != nil {
		t.Fatalf("depopulating: %v", err)
	}
	if _, live, _ := s.Session(who); live {
		t.Errorf("the session is still live after depopulating")
	}
	if _, err := os.Stat(state.Socket); !os.IsNotExist(err) {
		t.Errorf("the socket outlived the session")
	}
	// Twice is safe: `orc fire` and `orc tend` both call it to make a fact true.
	if err := session.Depopulate(s, who); err != nil {
		t.Errorf("depopulating twice failed: %v", err)
	}
}

// TestDeadSupervisorIsNotASession: a state file whose process is gone is a leftover,
// not a session. Reporting it as live would make `orc tend` refuse to restart the
// very thing it exists to restart.
func TestDeadSupervisorIsNotASession(t *testing.T) {
	s, who := fleet(t, "ember")

	// A pid that cannot be running: the kernel's own maximum plus one.
	if err := s.WriteSession(who, store.SessionState{
		ID: "0f9a1a6a-0000-4000-8000-000000000000", Supervisor: 4194305, Child: 4194306,
		Model: "sonnet", Effort: "medium",
	}); err != nil {
		t.Fatalf("writing a stale session: %v", err)
	}

	if _, live, err := s.Session(who); err != nil || live {
		t.Errorf("a dead supervisor's state read as live (err %v)", err)
	}
	sessions, err := s.Sessions()
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if sessions[who.String()] != "" {
		t.Errorf("a dead session was reported to the derivation as %q", sessions[who.String()])
	}
}

// TestEnvironmentCarriesTheCredentialAndNothingElseSecret: a session is authenticated
// because Orc put a key in its environment, and that is the only place the key goes.
func TestEnvironmentCarriesTheCredentialAndNothingElseSecret(t *testing.T) {
	s, who := fleet(t, "ember")
	key, err := s.Key(who)
	if err != nil {
		t.Fatalf("key: %v", err)
	}

	env, err := session.Environment(s, who, "0f9a1a6a-0000-4000-8000-000000000000")
	if err != nil {
		t.Fatalf("environment: %v", err)
	}

	joined := strings.Join(env, "\n")
	for _, want := range []string{
		"ORC_USER=ember", "ORC_KEY=" + key, "ORC_IDENTITY=ember",
		"CLAUDE_CONFIG_DIR=" + s.ClaudeDir(who), "ORC_AGENT=1", "ORC_HOME=" + s.Root(),
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the environment is missing %q", want)
		}
	}

	// Describe is what a log gets, and it must not get the key.
	if described := session.Describe(env); strings.Contains(described, key) {
		t.Errorf("the described environment leaked the key")
	} else if !strings.Contains(described, "ORC_KEY=(hidden)") {
		t.Errorf("the described environment does not say the key was hidden: %s", described)
	}
}

// TestSocketPathFallback: a store too deep for sun_path gets a short socket path,
// and the session records which one it used so no client has to guess.
func TestSocketPathFallback(t *testing.T) {
	deep := filepath.Join(os.TempDir(), strings.Repeat("a-very-long-directory-name/", 4), "fleet")
	s, err := store.Create(deep, clock.NewFake(epoch, time.Second))
	if err != nil {
		t.Fatalf("creating a deep store: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(os.TempDir() + "/a-very-long-directory-name") })

	who, err := user.Parse("ember")
	if err != nil {
		t.Fatalf("name: %v", err)
	}
	path := s.SocketPath(who)
	if len(path) > store.MaxSocketPath {
		t.Errorf("the socket path is %d bytes, over the %d limit: %s", len(path), store.MaxSocketPath, path)
	}
	if strings.HasPrefix(path, deep) {
		t.Errorf("a path too long for sun_path was used anyway: %s", path)
	}
}

// TestNewIDIsAUUID: `--session-id` takes a UUID, so a malformed one would make every
// session fail to start with a message about a flag rather than about the id.
func TestNewIDIsAUUID(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		id, err := session.NewID()
		if err != nil {
			t.Fatalf("id: %v", err)
		}
		if len(id) != 36 {
			t.Fatalf("id %q is %d characters, want 36", id, len(id))
		}
		for i, c := range id {
			switch i {
			case 8, 13, 18, 23:
				if c != '-' {
					t.Fatalf("id %q has no dash at %d", id, i)
				}
			default:
				if !strings.ContainsRune("0123456789abcdef", c) {
					t.Fatalf("id %q is not hexadecimal at %d", id, i)
				}
			}
		}
		if id[14] != '4' {
			t.Fatalf("id %q is not version 4", id)
		}
		if seen[id] {
			t.Fatalf("id %q was minted twice", id)
		}
		seen[id] = true
	}
}

// readUntil reads from a connection until it sees want or the deadline passes.
func readUntil(t *testing.T, r interface{ Read([]byte) (int, error) }, within time.Duration, want string) string {
	t.Helper()

	var b strings.Builder
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4<<10)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				b.Write(buf[:n])
				if strings.Contains(b.String(), want) {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(within):
	}
	return b.String()
}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(nil) == nil
}
