//go:build unix

package hook_test

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"orc/macmuffin/internal/hook"
)

// TestASlowStoreDoesNotStallASession is §8.4 rule 4.
//
// The store is genuinely stalled rather than mocked: the version file is a FIFO
// with no writer, so the first read blocks forever — which is what an
// unresponsive network mount looks like from inside a read. The hook must give
// up, say so, and let the edit through.
func TestStalledStoreFailsOpen(t *testing.T) {
	r := newRig(t)
	r.task("fix-the-parser", []string{"internal/tree/"}, true)

	version := filepath.Join(r.root, "version")
	if err := syscall.Unlink(version); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(version, 0o600); err != nil {
		t.Skipf("this filesystem will not make a fifo: %v", err)
	}

	opts := r.opts()
	opts.Deadline = 200 * time.Millisecond

	started := time.Now()
	got := hook.Run(r.event("Edit", filepath.Join(r.tree, "internal/render.go")), opts)
	took := time.Since(started)

	if got.Code != hook.CodeOK {
		t.Errorf("a stalled store exited %d; a hook must not block on a store it cannot read", got.Code)
	}
	// The cost is stated rather than hidden: while the store is broken, a
	// violation gets through, and the note is what tells anyone looking.
	if !strings.Contains(got.Stderr, "did not answer") {
		t.Errorf("a timeout should say so:\n%s", got.Stderr)
	}
	if took > 2*time.Second {
		t.Errorf("the check took %s; the deadline was %s", took, opts.Deadline)
	}
}
