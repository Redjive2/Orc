package pty_test

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"orc/orc/internal/pty"
)

// TestRoundTrip proves the layer does what the supervisor above it assumes: a child
// on the slave side is a real terminal program, what it prints comes out of the
// master, and what is written to the master arrives as keystrokes.
func TestRoundTrip(t *testing.T) {
	p, err := pty.Open()
	if err != nil {
		t.Fatalf("opening a pty: %v", err)
	}
	defer func() { _ = p.Close() }()

	// `cat` is the smallest thing that proves both directions at once.
	cmd := exec.Command("cat")
	p.Attach(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting: %v", err)
	}
	// The parent's copy of the child's side goes now: while it is open, a read on
	// the master never reports EOF, and a supervisor that forgot this would wait
	// forever for a child that had already gone.
	if err := p.CloseSlave(); err != nil {
		t.Fatalf("closing the slave: %v", err)
	}

	if _, err := p.Master.WriteString("hello\r"); err != nil {
		t.Fatalf("writing: %v", err)
	}

	got := readWithin(t, p.Master, time.Second, "hello")
	if !strings.Contains(got, "hello") {
		t.Errorf("the child's output did not come back: %q", got)
	}

	_ = p.Master.Close()
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}

// TestChildIsInItsOwnSession: the child is a session leader with the pty as its
// controlling terminal. Without that, Ctrl-C in an attached terminal would signal
// Orc rather than the agent, and no window-size change would ever reach it.
func TestChildIsInItsOwnSession(t *testing.T) {
	p, err := pty.Open()
	if err != nil {
		t.Fatalf("opening a pty: %v", err)
	}
	defer func() { _ = p.Close() }()

	// `tty` prints the terminal it is attached to, which is the slave's name if and
	// only if the controlling terminal was set.
	cmd := exec.Command("tty")
	p.Attach(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting: %v", err)
	}
	if err := p.CloseSlave(); err != nil {
		t.Fatalf("closing the slave: %v", err)
	}

	got := readWithin(t, p.Master, 2*time.Second, p.Name)
	if !strings.Contains(got, p.Name) {
		t.Errorf("the child reported %q, want the pty at %s", strings.TrimSpace(got), p.Name)
	}
	_, _ = cmd.Process.Wait()
}

// TestResize: a size is set and read back, and a zero size becomes a usable one
// rather than a terminal a TUI cannot draw into.
func TestResize(t *testing.T) {
	p, err := pty.Open()
	if err != nil {
		t.Fatalf("opening a pty: %v", err)
	}
	defer func() { _ = p.Close() }()

	want := pty.WinSize{Rows: 24, Cols: 80}
	if err := pty.Resize(p.Master, want); err != nil {
		t.Fatalf("resizing: %v", err)
	}
	got, err := pty.Size(p.Master)
	if err != nil {
		t.Fatalf("reading the size: %v", err)
	}
	if got.Rows != want.Rows || got.Cols != want.Cols {
		t.Errorf("size came back as %dx%d, want %dx%d", got.Rows, got.Cols, want.Rows, want.Cols)
	}

	if err := pty.Resize(p.Master, pty.WinSize{}); err != nil {
		t.Fatalf("resizing to zero: %v", err)
	}
	got, err = pty.Size(p.Master)
	if err != nil {
		t.Fatalf("reading the size: %v", err)
	}
	if got.Rows == 0 || got.Cols == 0 {
		t.Errorf("a zero size was accepted as %dx%d; a TUI cannot draw into that", got.Rows, got.Cols)
	}
}

// TestRawModeRestores is the one that matters for the operator's shell: raw mode is
// entered and left, and leaving twice is safe, because a deferred restore and a
// signal handler both have to be able to run it.
func TestRawModeRestores(t *testing.T) {
	p, err := pty.Open()
	if err != nil {
		t.Fatalf("opening a pty: %v", err)
	}
	defer func() { _ = p.Close() }()

	// The pty's own slave stands in for the operator's terminal: it is a terminal,
	// and a test that used os.Stdin would change the mode of whatever ran the test.
	restore, err := pty.MakeRaw(p.Slave)
	if err != nil {
		t.Fatalf("entering raw mode: %v", err)
	}
	if err := restore.Restore(); err != nil {
		t.Fatalf("restoring: %v", err)
	}
	if err := restore.Restore(); err != nil {
		t.Errorf("restoring twice failed: %v", err)
	}
}

// TestNotATerminal: the layer refuses a file that is not a terminal rather than
// producing a Pty whose ioctls quietly do nothing.
func TestNotATerminal(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "plain")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	defer func() { _ = f.Close() }()

	if _, err := pty.Size(f); err == nil {
		t.Errorf("reading the size of a plain file succeeded")
	}
	if _, err := pty.MakeRaw(f); err == nil {
		t.Errorf("raw mode on a plain file succeeded")
	}
	if err := pty.Resize(nil, pty.Sane()); err == nil {
		t.Errorf("resizing nothing succeeded")
	}
}

// readWithin reads until it sees want, or the deadline passes. It returns whatever
// arrived, so a failure message shows what the child actually said.
func readWithin(t *testing.T, f *os.File, within time.Duration, want string) string {
	t.Helper()

	type result struct{ text string }
	done := make(chan result, 1)

	go func() {
		var b strings.Builder
		buf := make([]byte, 512)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				b.Write(buf[:n])
				if strings.Contains(b.String(), want) {
					done <- result{b.String()}
					return
				}
			}
			if err != nil {
				// EIO is how a pty reports that the child side has gone, which is an
				// ordinary end of stream here rather than a failure.
				if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "input/output error") {
					done <- result{b.String()}
					return
				}
				done <- result{b.String()}
				return
			}
		}
	}()

	select {
	case got := <-done:
		return got.text
	case <-time.After(within):
		return "(nothing within " + within.String() + ")"
	}
}
