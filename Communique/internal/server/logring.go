package server

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"orc/cq/internal/logbook"
)

// What the server has been saying, kept so a browser can read it.
//
// The agent machines write their cycles to files, because the processes that
// produce those lines are not the process that sends them anywhere. The server is
// the opposite case: the thing that logs and the thing that answers the browser
// are one program, so a file would be a round trip through the disk for no reason
// — and a file is a thing to rotate, permission, and eventually forget to clean
// up on somebody's machine.
//
// So it is a ring in memory. The cost is stated rather than hidden: **a restart
// loses it.** That is the wrong trade for an audit trail and the right one for
// this, which is a window onto what is happening now. The queue, the mirror and
// the fleet all survive a restart on their own; nothing here is the only record
// of anything.

// LogRing is how many of the server's own lines are kept.
//
// Small enough to be free — a few hundred short strings — and long enough to hold
// the run-up to whatever somebody opened the page to look at.
const LogRing = 400

// logRing keeps the last lines, oldest first.
type logRing struct {
	mu    sync.Mutex
	lines []logbook.Line
}

func (r *logRing) add(line logbook.Line) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.lines) == LogRing {
		// Shifted rather than grown. The window is fixed and the cost of moving a
		// few hundred small structs once per log line is not worth a ring index
		// that has to be got right in two places.
		r.lines = append(r.lines[:0], r.lines[1:]...)
	}
	r.lines = append(r.lines, line)
}

// tail returns a copy, so a reader is never holding a slice the writer will
// shuffle underneath it.
func (r *logRing) tail() []logbook.Line {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]logbook.Line, len(r.lines))
	copy(out, r.lines)
	return out
}

// keepingLog wraps a handler so everything it writes is also remembered.
//
// A wrapper rather than a second logger the callers have to remember to use. The
// server logs from two dozen places and one of them would eventually be the
// interesting one; a handler cannot be forgotten at a call site.
type keepingLog struct {
	slog.Handler
	ring *logRing
}

func (h keepingLog) Handle(ctx context.Context, r slog.Record) error {
	// Assembled here rather than taken from the text handler's output, because the
	// text handler writes to an io.Writer and there is no seam to read it back
	// from. The shape matches what a machine's logbook carries — `level=INFO
	// msg=…` — so one renderer draws both.
	var b strings.Builder
	b.WriteString("level=" + r.Level.String() + " msg=" + quoteIfNeeded(r.Message))
	r.Attrs(func(a slog.Attr) bool {
		b.WriteString(" " + a.Key + "=" + quoteIfNeeded(a.Value.String()))
		return true
	})
	h.ring.add(logbook.Line{Level: r.Level.String(), Text: b.String()})
	return h.Handler.Handle(ctx, r)
}

func (h keepingLog) WithAttrs(attrs []slog.Attr) slog.Handler {
	return keepingLog{Handler: h.Handler.WithAttrs(attrs), ring: h.ring}
}

func (h keepingLog) WithGroup(name string) slog.Handler {
	return keepingLog{Handler: h.Handler.WithGroup(name), ring: h.ring}
}

// quoteIfNeeded matches what slog's text handler does with a value, so a line
// kept here reads the same as the one that went to stderr. A reader comparing the
// two should not have to wonder whether they are the same event.
func quoteIfNeeded(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, ` "=`) {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}
