package cli_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"orc/common/fault"
	"orc/orc/internal/cli"
	"orc/orc/internal/pty"
	"orc/orc/internal/session"
	"orc/orc/internal/store"
)

// `orc attach --direct` hands the operator's terminal to a session, and the loop
// that does it is the one part of attach no unit test reaches: scanDetach is
// covered in detach_test.go, but nothing exercises the two goroutines, the raw
// mode, or the detach actually detaching.
//
// So this drives the real thing. A pty stands in for the operator's terminal, a
// unix socket for the session, and the test types into one end and reads the
// other — which is what an operator is.

// fakeSession is a session server that speaks enough of the protocol to be
// attached to: it answers OpAttach by handing the connection to an echo, and
// records the resizes it was told about.
type fakeSession struct {
	t        *testing.T
	listener net.Listener

	mu       sync.Mutex
	resizes  []pty.WinSize
	attached chan net.Conn
}

func listen(t *testing.T, path string) *fakeSession {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listening on %s: %v", path, err)
	}
	f := &fakeSession{t: t, listener: l, attached: make(chan net.Conn, 1)}
	t.Cleanup(func() { _ = l.Close() })
	go f.serve()
	return f
}

func (f *fakeSession) serve() {
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *fakeSession) handle(conn net.Conn) {
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		_ = conn.Close()
		return
	}
	var req session.Request
	if err := json.Unmarshal(line, &req); err != nil {
		_ = conn.Close()
		return
	}

	switch req.Op {
	case session.OpAttach:
		// The connection becomes the raw stream: what the test writes to it is
		// what the operator's terminal shows, and what it reads is what they
		// typed. Dial's own probe connection closes without a request, which is
		// why an unreadable line above is not an error.
		select {
		case f.attached <- conn:
		default:
			_ = conn.Close()
		}
	case session.OpResize:
		f.mu.Lock()
		f.resizes = append(f.resizes, pty.WinSize{Rows: req.Rows, Cols: req.Cols})
		f.mu.Unlock()
		_ = writeReply(conn)
		_ = conn.Close()
	default:
		_ = writeReply(conn)
		_ = conn.Close()
	}
}

func writeReply(conn net.Conn) error {
	data, err := json.Marshal(session.Reply{OK: true})
	if err != nil {
		return err
	}
	_, err = conn.Write(append(data, '\n'))
	return err
}

// seen reports the resizes the session was told about.
func (f *fakeSession) seen() []pty.WinSize {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pty.WinSize(nil), f.resizes...)
}

// fakeOf finds the server behind an attached stream, so a test holding only the
// stream can ask what else the session was told.
func fakeOf(conn net.Conn) *fakeSession {
	servers.mu.Lock()
	defer servers.mu.Unlock()
	return servers.byConn[conn]
}

// servers maps each attached stream back to the server that handed it over.
var servers = struct {
	mu     sync.Mutex
	byConn map[net.Conn]*fakeSession
}{byConn: map[net.Conn]*fakeSession{}}

// stream waits for the attach connection, so a test can act as the session.
func (f *fakeSession) stream() net.Conn {
	f.t.Helper()
	select {
	case conn := <-f.attached:
		servers.mu.Lock()
		servers.byConn[conn] = f
		servers.mu.Unlock()
		return conn
	case <-time.After(5 * time.Second):
		f.t.Fatal("nothing attached")
		return nil
	}
}

// operator is a terminal for the command to be given, and the far end of it for
// the test to type into.
type operator struct {
	terminal *os.File // handed to the App as its stdin
	keyboard *os.File // what the test writes to
}

func newOperator(t *testing.T) *operator {
	t.Helper()
	p, err := pty.Open()
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return &operator{terminal: p.Slave, keyboard: p.Master}
}

func (o *operator) types(t *testing.T, keys string) {
	t.Helper()
	if _, err := io.WriteString(o.keyboard, keys); err != nil {
		t.Fatalf("typing %q: %v", keys, err)
	}
}

// attached employs an identity, stands a fake session on its socket, and starts
// `orc attach --direct` against it.
//
// The session is made the way every other test makes one — through `orc employ`,
// which the rig's populate turns into a live record — so this exercises the same
// path an operator takes rather than a hand-built store.
func attached(t *testing.T, r *rig, who string) (*operator, net.Conn, *lockedWriter, func() int) {
	t.Helper()

	r.ok("boss", "employ", who)

	s, err := store.Open(r.root, r.now)
	if err != nil {
		t.Fatal(err)
	}
	name := mustName(t, who)

	socket := s.SocketPath(name)
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	fake := listen(t, socket)

	op := newOperator(t)
	screen := &lockedWriter{to: &strings.Builder{}}
	codes := make(chan int, 1)

	go func() {
		codes <- cli.Main(cli.App{
			Stdin:  op.terminal,
			Stdout: screen,
			Stderr: io.Discard,
			Env: func(k string) (string, bool) {
				switch k {
				case "ORC_USER":
					return "boss", true
				case "ORC_KEY":
					return r.keys["boss"], true
				}
				return "", false
			},
			Root: r.root, Clock: r.now, Width: 100, User: "boss",
			Terminal:   true,
			Provision:  r.provision,
			Populate:   r.populate,
			Depopulate: r.depopulate,
		}, []string{"attach", who, "--direct"})
	}()

	conn := fake.stream()
	return op, conn, screen, func() int {
		select {
		case code := <-codes:
			return code
		case <-time.After(5 * time.Second):
			t.Fatal("attach did not return")
			return -1
		}
	}
}

// lockedWriter serialises writes from the proxy's output goroutine against the
// test reading what was drawn, so the race detector has nothing to say about it.
type lockedWriter struct {
	mu sync.Mutex
	to *strings.Builder
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.to.Write(p)
}

func (w *lockedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.to.String()
}

// readWithin reads whatever arrives within a moment, so a test can assert on
// what the session received without knowing how it was chunked.
func readWithin(t *testing.T, conn net.Conn, want int) string {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4<<10)
	var got strings.Builder
	for got.Len() < want {
		n, err := conn.Read(buf)
		if n > 0 {
			got.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return got.String()
}

// readQuietly reads whatever is already there and gives up at once. It is for
// asserting that nothing arrived, where waiting the full deadline would spend
// five seconds proving a negative.
func readQuietly(conn net.Conn) string {
	_ = conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	buf := make([]byte, 4<<10)
	n, _ := conn.Read(buf)
	return string(buf[:n])
}

// TestKeystrokesReachTheSessionAndOutputReachesTheTerminal is the loop itself:
// both directions, over a real pty and a real socket.
func TestKeystrokesReachTheSessionAndOutputReachesTheTerminal(t *testing.T) {
	r := fullFleet(t)
	op, conn, screen, wait := attached(t, r, "ember")

	// What the operator types arrives at the session, unprocessed.
	op.types(t, "ls -la\r")
	if got := readWithin(t, conn, len("ls -la\r")); !strings.Contains(got, "ls -la") {
		t.Errorf("the session received %q", got)
	}

	// What the session writes arrives on the operator's terminal.
	if _, err := io.WriteString(conn, "total 0\r\n"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return strings.Contains(screen.String(), "total 0") })

	op.types(t, "\x1cd") // ^\ d
	if code := wait(); code != fault.CodeOK {
		t.Errorf("attach exited %d, want 0", code)
	}
	if !strings.Contains(screen.String(), "detached") {
		t.Errorf("the operator was not told they detached:\n%s", screen.String())
	}
}

// TestAResizeTravelsOnItsOwnConnection is the reason the proxy watches SIGWINCH
// rather than letting the size ride the stream: a resize arriving mid-escape-
// sequence would corrupt the screen it was trying to fix.
func TestAResizeTravelsOnItsOwnConnection(t *testing.T) {
	r := fullFleet(t)
	op, conn, _, wait := attached(t, r, "ember")

	// Change the terminal, then tell the process about it the way a window
	// manager would.
	if err := pty.Resize(op.keyboard, pty.WinSize{Rows: 24, Cols: 80}); err != nil {
		t.Skipf("this platform will not resize a pty: %v", err)
	}
	if err := raiseResize(); err != nil {
		if errors.Is(err, errors.ErrUnsupported) {
			t.Skip("this platform has no window-change signal")
		}
		t.Fatalf("raising a resize: %v", err)
	}

	waitFor(t, func() bool { return len(fakeOf(conn).seen()) > 0 })
	got := fakeOf(conn).seen()
	if got[0].Rows != 24 || got[0].Cols != 80 {
		t.Errorf("the session was told %+v, want 24x80", got[0])
	}
	// It did not arrive inside the stream.
	if text := readQuietly(conn); text != "" {
		t.Errorf("something reached the stream during a resize: %q", text)
	}

	op.types(t, "\x1cd")
	if code := wait(); code != fault.CodeOK {
		t.Errorf("attach exited %d", code)
	}
}

// TestTheDetachSequenceDoesNotReachTheSession: ^\ d is Orc's, and forwarding it
// would make the session see two keystrokes nobody typed at it.
func TestTheDetachSequenceDoesNotReachTheSession(t *testing.T) {
	r := fullFleet(t)
	op, conn, _, wait := attached(t, r, "ember")
	op.types(t, "abc\x1cd")

	got := readWithin(t, conn, 3)
	if code := wait(); code != fault.CodeOK {
		t.Errorf("attach exited %d", code)
	}
	if !strings.Contains(got, "abc") {
		t.Errorf("the keystrokes before the sequence did not arrive: %q", got)
	}
	if strings.Contains(got, "\x1c") {
		t.Errorf("the detach prefix reached the session: %q", got)
	}
}

// TestASessionThatGoesAwayDetachesRatherThanFailing: a closed stream is how
// detaching looks from this side, and an operator whose agent stopped should be
// returned to their shell rather than shown a failure.
func TestASessionThatGoesAwayDetachesRatherThanFailing(t *testing.T) {
	r := fullFleet(t)
	_, conn, screen, wait := attached(t, r, "ember")
	_ = conn.Close()

	if code := wait(); code != fault.CodeOK {
		t.Errorf("attach exited %d, want 0", code)
	}
	if !strings.Contains(screen.String(), "still running") {
		t.Errorf("the operator was not told the session outlives the attach:\n%s", screen.String())
	}
}

// TestAttachDirectNeedsATerminal: it hands the caller's terminal over, so a
// piped invocation has nothing to hand and says so rather than half working.
func TestAttachDirectNeedsATerminal(t *testing.T) {
	r := fullFleet(t)
	r.ok("boss", "employ", "ember")

	got := r.run("boss", "attach", "ember", "--direct")
	if got.code != fault.CodeUsage {
		t.Fatalf("exited %d, want a usage error\n%s", got.code, got.stderr)
	}
	if !strings.Contains(got.stderr, "terminal") {
		t.Errorf("the refusal should say what is missing: %q", got.stderr)
	}
}

// A session that is not running is an unavailable peer, and the refusal names
// the command that starts one.
//
// It needs a terminal to reach: attach checks that first, deliberately, because
// "this needs a terminal" is about the caller's own invocation and hearing about
// an unreachable session instead would send somebody to debug the fleet.
func TestAttachingToNothingSaysHowToStartIt(t *testing.T) {
	r := fullFleet(t)
	op := newOperator(t)

	var errOut strings.Builder
	code := cli.Main(cli.App{
		Stdin:  op.terminal,
		Stdout: io.Discard,
		Stderr: &errOut,
		Env: func(k string) (string, bool) {
			switch k {
			case "ORC_USER":
				return "boss", true
			case "ORC_KEY":
				return r.keys["boss"], true
			}
			return "", false
		},
		Root: r.root, Clock: r.now, Width: 100, User: "boss",
		Terminal:   true,
		Provision:  r.provision,
		Populate:   r.populate,
		Depopulate: r.depopulate,
	}, []string{"attach", "ember", "--direct"})

	if code != fault.CodeUnavailable {
		t.Fatalf("exited %d, want unavailable\n%s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "orc employ ember") {
		t.Errorf("the refusal should name the way forward: %q", errOut.String())
	}
}

// waitFor polls a condition, so a test does not depend on how promptly a
// goroutine was scheduled.
func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the condition never became true")
}
