package server_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestStreamAnnouncesChanges: the browser learns that something moved without
// polling, and the event carries no payload — it says "look again", and the
// client refetches, so there is no second copy of the state to disagree.
func TestStreamAnnouncesChanges(t *testing.T) {
	h := newHarness(t)
	putSnapshot(t, h, "studio")
	cookie, csrf := h.login(t)

	srv := httptest.NewServer(h.Server)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: "cq_session", Value: cookie})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("Content-Type = %q", got)
	}

	reader := bufio.NewReader(resp.Body)
	if got := readEvent(t, reader); got != "ready" {
		t.Fatalf("first event = %q, want ready", got)
	}

	// A queued action is a change, and the stream should say so.
	done := make(chan string, 1)
	go func() { done <- readEvent(t, reader) }()

	// Wait for the listener to be registered before publishing, so the test
	// does not race the subscription.
	waitFor(t, func() bool { return h.Events().Listeners() > 0 })

	w := postWith(t, srv.URL+"/api/v1/messages/41/read", cookie, csrf, "")
	if w != http.StatusAccepted {
		t.Fatalf("queueing failed: status %d", w)
	}

	select {
	case got := <-done:
		if got != "change" {
			t.Errorf("event = %q, want change", got)
		}
	case <-ctx.Done():
		t.Fatal("no change event arrived")
	}
}

// readEvent reads one SSE event and returns its name.
func readEvent(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	name := ""
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return ""
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case line == "" && name != "":
			return name
		}
	}
}

func postWith(t *testing.T, url, cookie, csrf, body string) int {
	t.Helper()
	var reader *strings.Reader = strings.NewReader(body)
	req, err := http.NewRequestWithContext(t.Context(), "POST", url, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: "cq_session", Value: cookie})
	req.Header.Set("X-CSRF-Token", csrf)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}

// TestStreamNeedsASession: the change feed is not a way around the gate.
func TestStreamNeedsASession(t *testing.T) {
	h := newHarness(t)
	w := h.do(t, "GET", "/api/v1/events", "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", w.Code)
	}
}

// TestBrokerDoesNotBlockOnASlowListener: a browser that stops reading must not
// stall a sync.
func TestBrokerDoesNotBlockOnASlowListener(t *testing.T) {
	h := newHarness(t)
	b := h.Events()

	_, cancel := b.Subscribe()
	defer cancel()

	// Publish far more than the buffer holds. If publishing blocked, this
	// would never return.
	done := make(chan struct{})
	go func() {
		for range 1000 {
			b.Publish()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publishing blocked on a listener that is not reading")
	}
}

func TestBrokerUnsubscribeIsIdempotent(t *testing.T) {
	h := newHarness(t)
	b := h.Events()

	_, cancel := b.Subscribe()
	if got := b.Listeners(); got != 1 {
		t.Fatalf("listeners = %d, want 1", got)
	}
	cancel()
	cancel()
	if got := b.Listeners(); got != 0 {
		t.Errorf("listeners = %d, want 0", got)
	}
}

func TestBrokerCloseReleasesListeners(t *testing.T) {
	h := newHarness(t)
	b := h.Events()

	ch, cancel := b.Subscribe()
	defer cancel()

	b.Close()
	select {
	case _, open := <-ch:
		if open {
			t.Errorf("a closed broker should close its listeners")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not release the listener")
	}

	// Subscribing after Close returns a closed channel rather than hanging.
	after, cancelAfter := b.Subscribe()
	defer cancelAfter()
	select {
	case _, open := <-after:
		if open {
			t.Errorf("subscribing to a closed broker should not yield events")
		}
	case <-time.After(time.Second):
		t.Fatal("subscribing after Close hung")
	}

	b.Close() // idempotent
}

func TestBrokerIsSafeUnderConcurrency(t *testing.T) {
	h := newHarness(t)
	b := h.Events()

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, cancel := b.Subscribe()
			b.Publish()
			select {
			case <-ch:
			default:
			}
			cancel()
		}()
	}
	wg.Wait()
	if got := b.Listeners(); got != 0 {
		t.Errorf("listeners = %d, want 0 after everyone left", got)
	}
}
