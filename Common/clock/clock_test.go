package clock_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"orc/common/clock"
	"orc/common/fault"
)

func TestFormatParseRoundTrip(t *testing.T) {
	for _, tc := range []time.Time{
		time.Date(2026, 7, 24, 18, 31, 4, 512_000_000, time.UTC),
		time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2199, 12, 31, 23, 59, 59, 999_000_000, time.UTC),
		// A non-UTC instant must round-trip to the same moment, not the same clock face.
		time.Date(2026, 7, 24, 18, 31, 4, 0, time.FixedZone("x", 3600)),
	} {
		text := clock.Format(tc)
		got, err := clock.Parse(text)
		if err != nil {
			t.Fatalf("Parse(%q): %v", text, err)
		}
		if want := clock.Normalise(tc); !got.Equal(want) {
			t.Errorf("round trip of %s gave %s", want, got)
		}
		if got.Location() != time.UTC {
			t.Errorf("Parse(%q) returned zone %v, want UTC", text, got.Location())
		}
	}
}

// TestNormaliseTruncatesRatherThanRounds matters because a rounded timestamp
// can land in the future, and the message id derived from it would then
// disagree with the message's own sent field.
func TestNormaliseTruncatesRatherThanRounds(t *testing.T) {
	at := time.Date(2026, 7, 24, 18, 31, 4, 999_999_999, time.UTC)
	got := clock.Normalise(at)
	if got.Nanosecond() != 999_000_000 {
		t.Errorf("Normalise gave %d ns, want 999000000", got.Nanosecond())
	}
	if got.After(at) {
		t.Error("Normalise must never move an instant forward")
	}
}

func TestParseRejectsBadTimestamps(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
	}{
		{"empty", ""},
		{"not a time", "yesterday"},
		{"wrong layout", "2026-07-24T18:31:04Z"},
		{"no zone", "2026-07-24T18:31:04.512"},
		{"before earliest", "1999-12-31T23:59:59.999Z"},
		{"after latest", "2200-01-01T00:00:00.000Z"},
		{"far future", "9999-01-01T00:00:00.000Z"},
		{"trailing text", "2026-07-24T18:31:04.512Z "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := clock.Parse(tc.text); !errors.Is(err, fault.ErrParse) {
				t.Errorf("Parse(%q) = %v, want a parse fault", tc.text, err)
			}
		})
	}
}

// TestBoundsAreHalfOpen pins the exact edges, since "outside the range" is
// enforced on every stored timestamp.
func TestBoundsAreHalfOpen(t *testing.T) {
	if _, err := clock.Parse(clock.Format(clock.Earliest)); err != nil {
		t.Errorf("Earliest should be accepted, got %v", err)
	}
	if _, err := clock.Parse(clock.Format(clock.Latest)); err == nil {
		t.Error("Latest should be rejected: the range is half-open")
	}
	if _, err := clock.Parse(clock.Format(clock.Latest.Add(-time.Millisecond))); err != nil {
		t.Errorf("one tick before Latest should be accepted, got %v", err)
	}
}

func TestRealClockIsNormalised(t *testing.T) {
	now := clock.Real{}.Now()
	if now.Location() != time.UTC {
		t.Errorf("Real.Now returned zone %v, want UTC", now.Location())
	}
	if now.Nanosecond()%1_000_000 != 0 {
		t.Errorf("Real.Now returned sub-millisecond precision: %d ns", now.Nanosecond())
	}
	// Whatever the machine's clock says, it must be storable.
	if _, err := clock.Parse(clock.Format(now)); err != nil {
		t.Errorf("Real.Now produced an unstorable time: %v", err)
	}
}

func TestFakeAdvancesAndIsOrdered(t *testing.T) {
	start := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	c := clock.NewFake(start, time.Second)

	if got := c.Peek(); !got.Equal(start) {
		t.Errorf("Peek before any read = %s, want %s", got, start)
	}
	var prev time.Time
	for i := range 5 {
		got := c.Now()
		if i > 0 && !got.After(prev) {
			t.Errorf("reading %d gave %s, not after %s", i, got, prev)
		}
		prev = got
	}
	if got, want := c.Peek(), start.Add(5*time.Second); !got.Equal(want) {
		t.Errorf("after five reads Peek = %s, want %s", got, want)
	}
}

// TestFakeRejectsAStalledStep guards the case that would make two messages
// share an instant and therefore sort unpredictably.
func TestFakeRejectsAStalledStep(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 0)
	first, second := c.Now(), c.Now()
	if !second.After(first) {
		t.Errorf("a zero step must be replaced with a real one; got %s then %s", first, second)
	}
}

// TestFakeIsConcurrencySafe is needed because the concurrency suite shares one
// fake clock across every sending goroutine.
func TestFakeIsConcurrencySafe(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Millisecond)

	const n = 64
	seen := make([]time.Time, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			seen[i] = c.Now()
		}()
	}
	wg.Wait()

	unique := make(map[time.Time]bool, n)
	for _, at := range seen {
		if unique[at] {
			t.Fatalf("two goroutines were given the same instant %s", at)
		}
		unique[at] = true
	}
}

func TestParseSpan(t *testing.T) {
	for _, tc := range []struct {
		text string
		want time.Duration
	}{
		{"30s", 30 * time.Second},
		{"5m", 5 * time.Minute},
		{"2h", 2 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"4w", 4 * 7 * 24 * time.Hour},
		{"0d", 0},
		{"1w", 7 * 24 * time.Hour},
	} {
		got, err := clock.ParseSpan(tc.text)
		if err != nil {
			t.Errorf("ParseSpan(%q): %v", tc.text, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseSpan(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestParseSpanRejectsBadInput(t *testing.T) {
	for _, tc := range []string{
		"", "d", "w", "5", "5y", "-3d", "1.5h", "1 d", "d5", "999999999d", "5D",
	} {
		if _, err := clock.ParseSpan(tc); !errors.Is(err, fault.ErrParse) {
			t.Errorf("ParseSpan(%q) = %v, want a parse fault", tc, err)
		}
	}
}

// TestParseSpanOverflowIsRefused checks that a very long digit string is
// reported rather than silently wrapping to a small or negative duration.
func TestParseSpanOverflowIsRefused(t *testing.T) {
	_, err := clock.ParseSpan(strings.Repeat("9", 40) + "d")
	if !errors.Is(err, fault.ErrParse) {
		t.Fatalf("a 40-digit duration = %v, want a parse fault", err)
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("message %q should say the value is too large", err)
	}
}

func TestShowIsFixedWidth(t *testing.T) {
	// Every rendered timestamp must be the same width, or the table column
	// shears. This holds for any instant in the allowed range.
	want := len(clock.Show(clock.Earliest))
	for _, at := range []time.Time{
		clock.Earliest,
		time.Date(2026, 7, 24, 18, 31, 4, 512_000_000, time.UTC),
		time.Date(2199, 12, 31, 23, 59, 59, 0, time.UTC),
	} {
		if got := len(clock.Show(at)); got != want {
			t.Errorf("Show(%s) is %d characters, want %d", at, got, want)
		}
	}
}
