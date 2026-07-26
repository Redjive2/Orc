// Package clock supplies time, injectably, to every Orc tool.
//
// Every timestamp a tool records comes from a Clock, so a test can pin the
// exact instant a message was sent and compare rendered output byte for byte.
// Time is also the axis a mail store and a task pool are both ordered on, which
// makes an accidental time.Now() in the middle of a package a source of flaky
// tests that only fail on a busy machine.
package clock

import (
	"fmt"
	"sync"
	"time"

	"orc/common/fault"
)

// Layout is how an Orc tool writes an instant: RFC 3339 in UTC, to the
// millisecond. Millisecond resolution is enough to order a mailbox and short
// enough to stay readable in a file a human may have to repair by hand.
const Layout = "2006-01-02T15:04:05.000Z"

// Display is how an instant is shown in a table: no timezone, since everything
// is UTC, and no sub-second part, since a column of milliseconds is noise.
const Display = "2006-01-02 15:04"

// Bounds on a stored timestamp. Anything outside them is a corrupt or hostile
// record rather than a plausible mistake, and is refused at parse time so it
// can never sort itself to the top of an inbox.
var (
	Earliest = time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	Latest   = time.Date(2200, time.January, 1, 0, 0, 0, 0, time.UTC)
)

// Clock reports the current time.
type Clock interface {
	Now() time.Time
}

// Real is the system clock, normalised to UTC and truncated to the resolution
// Mailman stores, so what a command records is exactly what it would read back.
type Real struct{}

// Now implements Clock.
func (Real) Now() time.Time { return Normalise(time.Now()) }

// Fake is a deterministic clock for tests. Every read advances it by Step, so
// successive events in one test are strictly ordered without the test having to
// say so. It is safe for concurrent use, which the concurrency tests need.
type Fake struct {
	mu   sync.Mutex
	at   time.Time
	step time.Duration
}

// NewFake builds a deterministic clock starting at start and advancing by step
// on every read. A non-positive step is treated as one millisecond, since a
// clock that never advances would give two messages the same instant.
func NewFake(start time.Time, step time.Duration) *Fake {
	if step <= 0 {
		step = time.Millisecond
	}
	return &Fake{at: Normalise(start), step: step}
}

// Now implements Clock.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.at
	f.at = f.at.Add(f.step)
	return now
}

// Peek returns the next instant Now would return, without advancing.
func (f *Fake) Peek() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.at
}

// Normalise puts an instant in the form the tools store: UTC, truncated to the
// resolution of Layout. Formatting an un-normalised instant would round-trip
// into a different value, and equality on timestamps is load-bearing in the
// tests.
func Normalise(t time.Time) time.Time {
	return t.UTC().Truncate(time.Millisecond)
}

// Format renders an instant for storage.
func Format(t time.Time) string { return Normalise(t).Format(Layout) }

// Parse reads a stored instant, rejecting anything outside Earliest..Latest.
// The bounds are checked here rather than at the call site so no caller can
// forget: a timestamp is the sort key of an inbox, and a year-9999 record
// would pin itself to the top of everyone's mail forever.
func Parse(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fault.Parse{Reason: "empty timestamp"}
	}
	t, err := time.Parse(Layout, s)
	if err != nil {
		return time.Time{}, fault.Parse{Reason: fmt.Sprintf("timestamp %q is not %s", s, Layout)}
	}
	t = Normalise(t)
	if t.Before(Earliest) || !t.Before(Latest) {
		return time.Time{}, fault.Parse{Reason: fmt.Sprintf(
			"timestamp %q is outside %s..%s", s, Earliest.Format(Layout), Latest.Format(Layout))}
	}
	return t, nil
}

// Show renders an instant for a table cell.
func Show(t time.Time) string { return Normalise(t).Format(Display) }

// ParseSpan reads a duration written the way a person writes one — 30m, 2h,
// 7d, 4w — and returns it as a time.Duration. Go's own ParseDuration stops at
// hours, and "everything since last week" is the query a mailbox actually
// wants.
func ParseSpan(s string) (time.Duration, error) {
	if s == "" {
		return 0, fault.Parse{Reason: "empty duration"}
	}
	unit := s[len(s)-1]
	scale, ok := map[byte]time.Duration{
		's': time.Second,
		'm': time.Minute,
		'h': time.Hour,
		'd': 24 * time.Hour,
		'w': 7 * 24 * time.Hour,
	}[unit]
	if !ok {
		return 0, fault.Parse{Reason: fmt.Sprintf("duration %q must end in s, m, h, d, or w", s)}
	}

	digits := s[:len(s)-1]
	if digits == "" {
		return 0, fault.Parse{Reason: fmt.Sprintf("duration %q has no number", s)}
	}
	var n int64
	for i := 0; i < len(digits); i++ {
		c := digits[i]
		if c < '0' || c > '9' {
			return 0, fault.Parse{Reason: fmt.Sprintf("duration %q is not a whole number of %c", s, unit)}
		}
		n = n*10 + int64(c-'0')
		// A duration long enough to overflow is a typo, not a request.
		if n > 1<<20 {
			return 0, fault.Parse{Reason: fmt.Sprintf("duration %q is too large", s)}
		}
	}
	return time.Duration(n) * scale, nil
}
