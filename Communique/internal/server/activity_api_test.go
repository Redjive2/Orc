package server_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"orc/cq/internal/protocol"
)

// The series, end to end: a machine syncs buckets, the mirror merges them, the
// browser reads them back.
//
// The path is worth testing whole because each half is uninteresting alone. What
// matters is that a snapshot — which is otherwise replaced entirely — leaves
// something behind.

func syncBuckets(t *testing.T, h *harness, buckets ...protocol.ActivityBucket) {
	t.Helper()

	body, err := json.Marshal(map[string]any{
		"protocol": protocol.Version,
		"agent":    "test",
		"sent_at":  time.Now().UTC(),
		"snapshot": protocol.Snapshot{
			Machine: "sandy",
			User:    "rdm",
			TakenAt: time.Now().UTC(),
			Fleet: &protocol.Fleet{
				Operator:   "rdm",
				Identities: []protocol.FleetID{{Name: "ember", Buckets: buckets}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if w := h.do(t, "POST", "/api/v1/sync", string(body), h.withToken()); w.Code != http.StatusOK {
		t.Fatalf("syncing: %d %s", w.Code, w.Body)
	}
}

func bucketAt(hour int, turns int, out int64) protocol.ActivityBucket {
	b := protocol.ActivityBucket{
		At:    time.Now().UTC().Add(-time.Duration(hour) * time.Hour).Truncate(time.Hour).Format(time.RFC3339Nano),
		Model: "opus", Effort: "high", Turns: turns,
	}
	b.Tokens.Output = out
	return b
}

// series reads the route and returns ember's buckets.
func readSeries(t *testing.T, h *harness, query string) []protocol.ActivityBucket {
	t.Helper()
	got, _ := readSeriesAndPeriod(t, h, query)
	return got
}

// readSeriesAndPeriod also returns the width the route folded to.
func readSeriesAndPeriod(t *testing.T, h *harness, query string) ([]protocol.ActivityBucket, string) {
	t.Helper()

	cookie, _ := h.login(t)
	res := h.do(t, "GET", "/api/v1/activity"+query, "", h.withCookie(cookie))
	if res.Code != http.StatusOK {
		t.Fatalf("reading the series: %d %s", res.Code, res.Body.String())
	}
	var got struct {
		Window   string `json:"window"`
		Period   string `json:"period"`
		Machines []struct {
			Machine    string                               `json:"machine"`
			Identities map[string][]protocol.ActivityBucket `json:"identities"`
		} `json:"machines"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v\n%s", err, res.Body.String())
	}
	if len(got.Machines) == 0 {
		return nil, got.Period
	}
	return got.Machines[0].Identities["ember"], got.Period
}

func TestBucketsSurviveASnapshotBeingReplaced(t *testing.T) {
	h := newHarness(t)

	syncBuckets(t, h, bucketAt(2, 5, 100))
	// A second sync carrying nothing at all: everything else about the machine is
	// replaced, and the series must not be.
	syncBuckets(t, h)

	got := readSeries(t, h, "")
	if len(got) != 1 || got[0].Turns != 5 {
		t.Errorf("the series was replaced along with the snapshot: %+v", got)
	}
}

func TestTheSeriesGrowsAcrossSyncs(t *testing.T) {
	h := newHarness(t)

	syncBuckets(t, h, bucketAt(3, 1, 10))
	syncBuckets(t, h, bucketAt(3, 1, 10), bucketAt(2, 2, 20))
	syncBuckets(t, h, bucketAt(2, 2, 20), bucketAt(1, 3, 30))

	got := readSeries(t, h, "")
	if len(got) != 3 {
		t.Fatalf("three hours across three syncs made %d buckets", len(got))
	}
	var turns int
	for _, b := range got {
		turns += b.Turns
	}
	if turns != 6 {
		t.Errorf("turns total %d, want 6 — a repeated bucket was counted twice", turns)
	}
}

func TestAWindowIsHonoured(t *testing.T) {
	h := newHarness(t)
	syncBuckets(t, h, bucketAt(40, 1, 10), bucketAt(2, 2, 20))

	if got := readSeries(t, h, "?since=6h"); len(got) != 1 {
		t.Errorf("a six-hour window returned %d buckets, want 1", len(got))
	}
	if got := readSeries(t, h, "?since=72h"); len(got) != 2 {
		t.Errorf("a three-day window returned %d buckets, want 2", len(got))
	}
}

// The width a chart is drawn at is the server's decision, not the recording's.
//
// Recording resolution and display resolution are different questions: an hour of
// minutes is sixty honest bars, and a month of them is forty thousand values crushed
// into sixty pixels. So the route folds to a period chosen from the window, and says
// which one it chose — a browser inferring the spacing from the buckets it happened
// to receive would draw a busy hour and a quiet fortnight at the same width.
func TestTheRouteFoldsToAPeriodThatFitsTheWindow(t *testing.T) {
	h := newHarness(t)
	// Four minutes inside one hour, two hours ago.
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour)
	var fine []protocol.ActivityBucket
	for i := range 4 {
		b := protocol.ActivityBucket{
			At:    base.Add(time.Duration(i*10) * time.Minute).Format(time.RFC3339Nano),
			Model: "opus", Effort: "high", Turns: 1,
		}
		b.Tokens.Output = 10
		fine = append(fine, b)
	}
	syncBuckets(t, h, fine...)

	// Wide: the four minutes fold into the hour they happened in.
	wide, period := readSeriesAndPeriod(t, h, "?since=48h")
	if len(wide) != 1 {
		t.Fatalf("a two-day window returned %d buckets, want 1 folded hour", len(wide))
	}
	if wide[0].Turns != 4 || wide[0].Tokens.Output != 40 {
		t.Errorf("folding lost work: %+v", wide[0])
	}
	if period != time.Hour.String() {
		t.Errorf("a two-day window reported period %q, want %s", period, time.Hour)
	}

	// Narrow: the minutes are kept apart, because that is the question being asked.
	narrow, period := readSeriesAndPeriod(t, h, "?since=6h")
	if len(narrow) != 4 {
		t.Fatalf("a six-hour window returned %d buckets, want the 4 minutes", len(narrow))
	}
	if got, _ := time.ParseDuration(period); got <= 0 || got > 10*time.Minute {
		t.Errorf("a six-hour window reported period %q, want minutes", period)
	}
}

// A window wider than the mirror keeps minutes for cannot be drawn at minutes: past
// FineWindow the stored buckets are hours, and a ten-minute period would put a whole
// hour's work in the first slot of each hour and nothing in the other five — a
// picture of the storage rather than of the work.
func TestAWideWindowIsNeverDrawnFinerThanWhatIsKept(t *testing.T) {
	h := newHarness(t)
	syncBuckets(t, h, bucketAt(2, 1, 10))
	for _, window := range []string{"24h", "48h", "168h", "720h"} {
		_, period := readSeriesAndPeriod(t, h, "?since="+window)
		got, err := time.ParseDuration(period)
		if err != nil {
			t.Fatalf("since=%s reported an unreadable period %q", window, period)
		}
		if got < time.Hour {
			t.Errorf("since=%s would be drawn every %s, finer than the mirror keeps", window, got)
		}
	}
}

func TestAWindowThatIsNotADurationIsRefused(t *testing.T) {
	h := newHarness(t)
	for _, bad := range []string{"soon", "0s", "-1h", "9000h"} {
		cookie, _ := h.login(t)
		res := h.do(t, "GET", "/api/v1/activity?since="+bad, "", h.withCookie(cookie))
		if res.Code == http.StatusOK {
			t.Errorf("since=%q was accepted", bad)
		}
	}
}

// A fleet from an older orc sends no buckets. The route answers with an empty list
// rather than an error: there is nothing wrong, there is simply nothing yet.
func TestNoBucketsIsAnEmptyAnswer(t *testing.T) {
	h := newHarness(t)
	syncBuckets(t, h)

	if got := readSeries(t, h, ""); len(got) != 0 {
		t.Errorf("a fleet with no buckets answered %+v", got)
	}
}
