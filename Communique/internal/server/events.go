package server

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"orc/cq/internal/fault"
)

// broker fans a "something changed" signal out to every listening browser.
//
// It carries no payload. A client that hears the signal refetches what it is
// showing, so there is no second copy of the state travelling down a second
// channel to disagree with the first.
type broker struct {
	mu        sync.Mutex
	listeners map[chan struct{}]struct{}
	closed    bool
}

func newBroker() *broker {
	return &broker{listeners: make(map[chan struct{}]struct{})}
}

// Subscribe registers a listener and returns it with the function that removes
// it. The channel has a buffer of one: a listener that is busy does not block
// the publisher, and coalescing two signals into one loses nothing, since the
// signal carries no information beyond "look again".
func (b *broker) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	b.listeners[ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.listeners, ch)
			b.mu.Unlock()
		})
	}
}

// Publish signals every listener, without blocking on any of them.
func (b *broker) Publish() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.listeners {
		select {
		case ch <- struct{}{}:
		default: // already signalled and not yet read
		}
	}
}

// Close releases every listener, so shutting down does not leave connections
// waiting for a signal that will never come.
func (b *broker) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for ch := range b.listeners {
		delete(b.listeners, ch)
		close(ch)
	}
}

// Listeners reports how many streams are open, for tests and diagnostics.
func (b *broker) Listeners() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.listeners)
}

// stream is the change feed: server-sent events, one-way, stdlib only.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.fail(w, r, fault.Internal{Where: "server.stream", Detail: "the response writer cannot stream"})
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	// Tell a reverse proxy not to buffer this; nginx in particular will
	// otherwise hold every event until the connection closes.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	changes, unsubscribe := s.subscribeWithClock()
	defer unsubscribe()

	// An immediate event so a client knows the stream is live rather than
	// merely accepted.
	if !s.emit(w, flusher, "ready") {
		return
	}

	ticker := time.NewTicker(s.heartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case _, open := <-changes:
			if !open {
				return
			}
			if !s.emit(w, flusher, "change") {
				return
			}
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) subscribeWithClock() (<-chan struct{}, func()) { return s.events.Subscribe() }

// emit writes one event and reports whether the connection is still usable.
func (s *Server) emit(w http.ResponseWriter, f http.Flusher, name string) bool {
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %d\n\n", name, s.now().UnixMilli()); err != nil {
		return false
	}
	f.Flush()
	return true
}
