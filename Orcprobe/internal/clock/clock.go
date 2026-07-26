// Package clock supplies time, injectably.
//
// Every timestamp Orcprobe records comes from a Clock, so a test can pin the
// exact instant a probe was taken and compare rendered output byte for byte.
//
// The layout is Mailman's, deliberately: orcprobe writes records that Mailman
// itself reads back (a reminted user record carries a created time), and two
// tools disagreeing about how an instant is spelled would be a corruption that
// only shows up in someone else's parser.
package clock

import (
	"strings"
	"sync"
	"time"

	"orc/orcprobe/internal/fault"
)

// Layout is how an instant is written: RFC 3339 in UTC, to the millisecond.
const Layout = "2006-01-02T15:04:05.000Z"

// Display is how an instant is shown in a table: no timezone, since everything
// is UTC, and no sub-second part, since a column of milliseconds is noise.
const Display = "2006-01-02 15:04"

// Bounds on a stored timestamp. Anything outside them is a corrupt or hostile
// record rather than a plausible mistake.
var (
	Earliest = time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	Latest   = time.Date(2200, time.January, 1, 0, 0, 0, 0, time.UTC)
)

// Clock reports the current time.
type Clock interface {
	Now() time.Time
}

// Real is the system clock, normalised to UTC and truncated to the resolution
// Orcprobe stores, so what a command records is exactly what it reads back.
type Real struct{}

// Now implements Clock.
func (Real) Now() time.Time { return Normalise(time.Now()) }

// Fake is a deterministic clock for tests. Every read advances it by step, so
// successive events in one test are strictly ordered without the test saying
// so. It is safe for concurrent use.
type Fake struct {
	mu   sync.Mutex
	at   time.Time
	step time.Duration
}

// NewFake builds a deterministic clock starting at start and advancing by step
// on every read. A non-positive step becomes one millisecond, since a clock
// that never advances would give two events the same instant.
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
	at := f.at
	f.at = Normalise(f.at.Add(f.step))
	return at
}

// Normalise puts an instant in the form Orcprobe stores: UTC, truncated to the
// millisecond.
func Normalise(t time.Time) time.Time { return t.UTC().Truncate(time.Millisecond) }

// Format renders an instant for storage.
func Format(t time.Time) string { return Normalise(t).Format(Layout) }

// Parse reads a stored instant, refusing one outside the bounds rather than
// accepting a timestamp that would sort itself to the top of every listing.
func Parse(s string) (time.Time, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return time.Time{}, fault.Parse{Reason: "timestamp is empty"}
	}
	t, err := time.Parse(Layout, trimmed)
	if err != nil {
		return time.Time{}, fault.Parse{Reason: "timestamp " + quote(trimmed) + " is not in " + Layout + " form"}
	}
	t = Normalise(t)
	if t.Before(Earliest) || t.After(Latest) {
		return time.Time{}, fault.Parse{Reason: "timestamp " + quote(trimmed) + " is outside the range orcprobe accepts"}
	}
	return t, nil
}

// Show renders an instant for a table: no timezone, since everything is UTC,
// and no sub-second part, since a column of milliseconds is noise. A zero time
// renders as a dash rather than as the year 1, which would sort and read as a
// real date.
func Show(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return Normalise(t).Format(Display)
}

// Since renders how long ago an instant was, in the coarsest unit that is still
// informative. A probe's age is read at a glance, not measured.
func Since(now, then time.Time) string {
	d := now.Sub(then)
	if d < 0 {
		return "in the future"
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return itoa(int(d/time.Minute)) + "m ago"
	case d < 24*time.Hour:
		return itoa(int(d/time.Hour)) + "h ago"
	default:
		return itoa(int(d/(24*time.Hour))) + "d ago"
	}
}

func quote(s string) string { return `"` + s + `"` }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
