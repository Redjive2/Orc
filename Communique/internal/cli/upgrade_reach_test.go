package cli

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
)

// `cq upgrade` had one dial, on http.DefaultClient, with no timeout and no retry.
//
// The server takes itself down to restart — restartGrace, then the listener
// draining, then the supervisor's own backoff — so an upgrade issued while a
// previous one restarts found a closed port and failed. That is the refused dial
// an operator sees, and it reads as "upgrade is broken" rather than "ask again in
// twenty seconds".

// refusing answers nothing until the nth attempt, like a server coming back up.
func refusing(t *testing.T, until int32) (*httptest.Server, *int32) {
	t.Helper()
	var seen int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&seen, 1) < until {
			// The closest a live handler gets to a refused connection: hang up
			// without answering, which the client reports as an unexpected EOF.
			hijacked, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = hijacked.Close()
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"queued":[],"server":"pulling","restarting":false}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestUpgradeRidesOutAServerThatIsRestarting(t *testing.T) {
	srv, seen := refusing(t, 3)
	var out strings.Builder
	a := App{Stdout: &out, Stderr: &out}

	view, err := a.askUpgrade(t.Context(), srv.URL, "token", map[string]any{})
	if err != nil {
		t.Fatalf("it gave up on a server that came back: %v", err)
	}
	if *seen < 3 {
		t.Errorf("it succeeded after %d attempts, so nothing was retried", *seen)
	}
	if view.Server != "pulling" {
		t.Errorf("the answer was not read: %+v", view)
	}
	// Said out loud. A command that sits silent for half a minute is one somebody
	// interrupts, and interrupting this one leaves a fleet half upgraded.
	if !strings.Contains(out.String(), "restarting") {
		t.Errorf("the wait was not explained:\n%s", out.String())
	}
}

// Only a gap is retried. Every answer the server gives is an answer, and asking
// again would turn one clear refusal into seven.
func TestAnAnsweringServerIsNotRetried(t *testing.T) {
	var seen int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&seen, 1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("no"))
	}))
	defer srv.Close()

	var out strings.Builder
	a := App{Stdout: &out, Stderr: &out}
	if _, err := a.askUpgrade(t.Context(), srv.URL, "token", map[string]any{}); err == nil {
		t.Fatal("a refusal was reported as success")
	}
	if seen != 1 {
		t.Errorf("a refusal was asked %d times; once is the answer", seen)
	}
}

// A server that never comes back is reported as unreachable, which is what sends
// the command to its local fallback.
func TestAServerThatNeverAnswersIsUnreachable(t *testing.T) {
	// A port nothing is listening on: taken and released, so the refusal is real.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + listener.Addr().String()
	_ = listener.Close()

	var out strings.Builder
	a := App{Stdout: &out, Stderr: &out}
	_, err = a.askUpgrade(t.Context(), url, "token", map[string]any{})
	if err == nil {
		t.Fatal("a dead address answered")
	}
	if !errors.Is(err, syscall.ECONNREFUSED) && !strings.Contains(err.Error(), "no answer in") {
		t.Errorf("the failure does not say it was never reached: %v", err)
	}
}

// unreached is what decides whether to wait, so what it calls a gap matters.
func TestOnlyAGapIsWaitedFor(t *testing.T) {
	for _, err := range []error{syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.EPIPE} {
		if !unreached(err) {
			t.Errorf("%v should be waited out", err)
		}
	}
	if unreached(errors.New("the server answered 403")) {
		t.Error("an answer was treated as a gap")
	}
}
