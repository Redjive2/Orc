package store_test

import (
	"testing"
	"time"

	"orc/cq/internal/protocol"
	"orc/cq/internal/store"
)

// The series is the one part of a snapshot that accumulates.
//
// Everything else a machine sends replaces what was there; a rate cannot, because
// history is the thing it is a rate over and a snapshot only carries a window. The
// merge is safe because a bucket total only ever grows, so what is tested here is
// exactly that: the same bucket twice is not twice the number, a machine that was
// offline can fill in what it missed, and nothing depends on arriving in order.

const machine = protocol.MachineID("sandy")

// hourly renders one hour of the fixture day, the way the agent machine spells it.
func hourly(hour int) string {
	return time.Date(2026, 7, 26, hour, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
}

func bucket(hour int, turns int, out int64) protocol.ActivityBucket {
	b := protocol.ActivityBucket{At: hourly(hour), Model: "opus", Effort: "high", Turns: turns}
	b.Tokens.Output = out
	b.Files.Reads = 1
	return b
}

func fleetWith(name string, buckets ...protocol.ActivityBucket) protocol.Fleet {
	return protocol.Fleet{Identities: []protocol.FleetID{{Name: name, Buckets: buckets}}}
}

func merged(t *testing.T, s *store.Store, seen time.Time, fleet protocol.Fleet) {
	t.Helper()
	if err := s.MergeActivity(machine, fleet, seen); err != nil {
		t.Fatalf("merging: %v", err)
	}
}

func series(t *testing.T, s *store.Store, identity string) []protocol.ActivityBucket {
	t.Helper()
	got, err := s.Activity(machine, identity, time.Time{})
	if err != nil {
		t.Fatalf("reading the series: %v", err)
	}
	return got
}

// The property everything else rests on. A sync repeats the last two days on every
// pass, so the same bucket arrives over and over.
func TestTheSameBucketTwiceIsNotTwiceTheNumber(t *testing.T) {
	s := open(t)
	now := time.Date(2026, 7, 26, 15, 0, 0, 0, time.UTC)

	merged(t, s, now, fleetWith("ember", bucket(12, 5, 100)))
	merged(t, s, now.Add(time.Minute), fleetWith("ember", bucket(12, 5, 100)))
	merged(t, s, now.Add(2*time.Minute), fleetWith("ember", bucket(12, 5, 100)))

	got := series(t, s, "ember")
	if len(got) != 1 {
		t.Fatalf("three syncs of one bucket made %d buckets", len(got))
	}
	if got[0].Turns != 5 || got[0].Tokens.Output != 100 {
		t.Errorf("the bucket was summed rather than replaced: %+v", got[0])
	}
}

// A bucket that grew between two syncs takes the newer value.
func TestABucketThatGrewTakesTheNewerValue(t *testing.T) {
	s := open(t)
	now := time.Date(2026, 7, 26, 15, 0, 0, 0, time.UTC)

	merged(t, s, now, fleetWith("ember", bucket(12, 5, 100)))
	merged(t, s, now.Add(time.Minute), fleetWith("ember", bucket(12, 9, 250)))

	got := series(t, s, "ember")
	if len(got) != 1 || got[0].Turns != 9 || got[0].Tokens.Output != 250 {
		t.Errorf("the series holds %+v, want the newer reading", got)
	}
}

// An older reading arriving late must not overwrite a newer one. A retried request,
// a clock that jumped, two syncs racing: all the same shape.
func TestAnOlderReadingDoesNotOverwriteANewerOne(t *testing.T) {
	s := open(t)
	now := time.Date(2026, 7, 26, 15, 0, 0, 0, time.UTC)

	merged(t, s, now.Add(time.Minute), fleetWith("ember", bucket(12, 9, 250)))
	merged(t, s, now, fleetWith("ember", bucket(12, 5, 100)))

	if got := series(t, s, "ember"); got[0].Turns != 9 {
		t.Errorf("a late older reading won: %+v", got[0])
	}
}

// The reason the window is a window: a machine that could not sync all afternoon
// delivers every hour it missed at once, and they all land.
func TestAMachineThatWasOfflineFillsInWhatItMissed(t *testing.T) {
	s := open(t)
	now := time.Date(2026, 7, 26, 18, 0, 0, 0, time.UTC)

	merged(t, s, now, fleetWith("ember",
		bucket(12, 1, 10), bucket(13, 2, 20), bucket(14, 3, 30), bucket(15, 4, 40)))

	got := series(t, s, "ember")
	if len(got) != 4 {
		t.Fatalf("four missed hours landed as %d buckets", len(got))
	}
	// Oldest first, which is what a chart wants.
	for i := 1; i < len(got); i++ {
		if got[i-1].At >= got[i].At {
			t.Fatalf("the series is not oldest-first: %v", got)
		}
	}
}

func TestAgentsKeepTheirOwnSeries(t *testing.T) {
	s := open(t)
	now := time.Date(2026, 7, 26, 15, 0, 0, 0, time.UTC)

	merged(t, s, now, protocol.Fleet{Identities: []protocol.FleetID{
		{Name: "ember", Buckets: []protocol.ActivityBucket{bucket(12, 5, 100)}},
		{Name: "atlas", Buckets: []protocol.ActivityBucket{bucket(12, 1, 7)}},
	}})

	if got := series(t, s, "ember"); len(got) != 1 || got[0].Turns != 5 {
		t.Errorf("ember's series is %+v", got)
	}
	if got := series(t, s, "atlas"); len(got) != 1 || got[0].Turns != 1 {
		t.Errorf("atlas's series is %+v", got)
	}
	// And asking for everybody gets both.
	if got := series(t, s, ""); len(got) != 2 {
		t.Errorf("the whole machine's series is %d buckets, want 2", len(got))
	}
}

// Model and effort split a bucket, because "what does opus at high effort cost" is
// the question the split exists to answer.
func TestModelAndEffortAreSeparateBuckets(t *testing.T) {
	s := open(t)
	now := time.Date(2026, 7, 26, 15, 0, 0, 0, time.UTC)

	other := bucket(12, 1, 5)
	other.Model = "sonnet"
	merged(t, s, now, fleetWith("ember", bucket(12, 1, 5), other))

	if got := series(t, s, "ember"); len(got) != 2 {
		t.Errorf("two models in one hour made %d buckets", len(got))
	}
}

func TestAWindowBoundsTheSeries(t *testing.T) {
	s := open(t)
	now := time.Date(2026, 7, 26, 18, 0, 0, 0, time.UTC)
	merged(t, s, now, fleetWith("ember", bucket(9, 1, 10), bucket(15, 1, 10)))

	got, err := s.Activity(machine, "ember", time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].At != hourly(15) {
		t.Errorf("the window kept %+v", got)
	}
}

// A machine that has never sent a bucket is an older orc or an idle fleet, not an
// error.
func TestAMachineWithNoSeriesIsNotAnError(t *testing.T) {
	s := open(t)
	got, err := s.Activity(machine, "", time.Time{})
	if err != nil {
		t.Fatalf("a machine with no series: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("it read %d buckets from nothing", len(got))
	}
}

func TestGroupingGivesOneSeriesPerAgent(t *testing.T) {
	s := open(t)
	now := time.Date(2026, 7, 26, 15, 0, 0, 0, time.UTC)
	merged(t, s, now, protocol.Fleet{Identities: []protocol.FleetID{
		{Name: "ember", Buckets: []protocol.ActivityBucket{bucket(12, 5, 100), bucket(13, 1, 5)}},
		{Name: "atlas", Buckets: []protocol.ActivityBucket{bucket(12, 1, 7)}},
	}})

	by, err := s.ActivityByIdentity(machine, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(by["ember"]) != 2 || len(by["atlas"]) != 1 {
		t.Errorf("grouped as %v", by)
	}
}

// Retention drops whole months, because a month is the unit the series is written
// in and rewriting one to drop three days is work for nothing.
func TestPruningDropsOldMonths(t *testing.T) {
	s := open(t)
	old := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	merged(t, s, old, fleetWith("ember", protocol.ActivityBucket{At: old.Format(time.RFC3339Nano), Turns: 1}))
	merged(t, s, recent, fleetWith("ember", bucket(12, 2, 20)))

	if err := s.PruneActivity(machine, recent); err != nil {
		t.Fatalf("pruning: %v", err)
	}
	got := series(t, s, "ember")
	if len(got) != 1 {
		t.Fatalf("pruning left %d buckets, want 1", len(got))
	}
	if got[0].Turns != 2 {
		t.Errorf("pruning kept the wrong month: %+v", got[0])
	}
}
