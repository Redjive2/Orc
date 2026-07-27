package activity_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orc/orc/internal/activity"
)

// Reading a transcript for what a session cost and what it touched.
//
// The transcript is Claude's file, so most of what is worth pinning here is about
// *not* trusting it: a line that will not parse is skipped rather than fatal, a
// number that is not there is absent rather than zero, and a file that was rotated
// under the reader says so instead of quietly counting an hour twice.

const session = "s-1"

// line renders one transcript line. The fields are Claude's, spelled as Claude
// spells them, because that is the whole contract this package has.
func line(t *testing.T, at, model, effort string, u map[string]any, result any) string {
	t.Helper()

	entry := map[string]any{
		"type":      "assistant",
		"sessionId": session,
		"timestamp": at,
	}
	if effort != "" {
		entry["effort"] = effort
	}
	if u != nil {
		entry["message"] = map[string]any{"model": model, "usage": u}
	}
	if result != nil {
		entry["toolUseResult"] = result
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func used(input, output, create, read int64) map[string]any {
	return map[string]any{
		"input_tokens": input, "output_tokens": output,
		"cache_creation_input_tokens": create, "cache_read_input_tokens": read,
	}
}

func write(t *testing.T, lines ...string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func read(t *testing.T, path string, from activity.Cursor) activity.Reading {
	t.Helper()

	got, err := activity.Read(path, session, from)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return got
}

func TestATurnsCostIsCounted(t *testing.T) {
	path := write(t, line(t, "2026-07-26T12:10:00.000Z", "claude-opus-5", "high",
		used(2, 123, 1536, 292164), nil))

	got := read(t, path, activity.Cursor{})
	if len(got.Buckets) != 1 {
		t.Fatalf("got %d buckets, want 1", len(got.Buckets))
	}
	b := got.Buckets[0]
	if b.Tokens.Input != 2 || b.Tokens.Output != 123 || b.Tokens.CacheCreate != 1536 || b.Tokens.CacheRead != 292164 {
		t.Errorf("counters are %+v", b.Tokens)
	}
	// The figure worth a headline is everything but the cache reads.
	if want := int64(2 + 123 + 1536); b.Tokens.New() != want {
		t.Errorf("new tokens is %d, want %d", b.Tokens.New(), want)
	}
	if b.Turns != 1 {
		t.Errorf("turns is %d, want 1", b.Turns)
	}
}

// The reading is taken at the minute, which is the floor on every question asked of
// it: an hourly reading answers "what is it doing now" with a single bar, and no
// amount of folding afterwards recovers detail that was never taken.
func TestTurnsAreBucketedByTheMinute(t *testing.T) {
	path := write(t,
		line(t, "2026-07-26T12:10:10.000Z", "claude-opus-5", "high", used(1, 1, 0, 0), nil),
		line(t, "2026-07-26T12:10:50.000Z", "claude-opus-5", "high", used(1, 1, 0, 0), nil),
		line(t, "2026-07-26T12:11:05.000Z", "claude-opus-5", "high", used(1, 1, 0, 0), nil))

	got := read(t, path, activity.Cursor{})
	if len(got.Buckets) != 2 {
		t.Fatalf("got %d buckets, want 2 (two minutes)", len(got.Buckets))
	}
	if got.Buckets[0].Turns != 2 || got.Buckets[1].Turns != 1 {
		t.Errorf("turns landed %d/%d, want 2/1", got.Buckets[0].Turns, got.Buckets[1].Turns)
	}
	// Oldest first, so a rollup appends in a stable order.
	if !got.Buckets[0].At.Before(got.Buckets[1].At) {
		t.Error("buckets are not oldest-first")
	}
}

// Coarsen is the whole of downsampling, and everything downstream leans on it being
// exactly the fold that already merges buckets — a chart at five minutes and a chart
// at an hour disagreeing about a total would be two different measurements wearing
// one name.
func TestCoarsenFoldsWithoutLosingAnything(t *testing.T) {
	path := write(t,
		line(t, "2026-07-26T12:10:00.000Z", "claude-opus-5", "high", used(1, 10, 0, 0), nil),
		line(t, "2026-07-26T12:50:00.000Z", "claude-opus-5", "high", used(1, 20, 0, 0), nil),
		line(t, "2026-07-26T13:05:00.000Z", "claude-opus-5", "high", used(1, 30, 0, 0), nil))
	fine := read(t, path, activity.Cursor{}).Buckets

	hourly := activity.Coarsen(fine, time.Hour)
	if len(hourly) != 2 {
		t.Fatalf("got %d hours, want 2", len(hourly))
	}
	if hourly[0].Turns != 2 || hourly[1].Turns != 1 {
		t.Errorf("turns landed %d/%d, want 2/1", hourly[0].Turns, hourly[1].Turns)
	}
	if hourly[0].Tokens.Output != 30 || hourly[1].Tokens.Output != 30 {
		t.Errorf("tokens landed %d/%d, want 30/30", hourly[0].Tokens.Output, hourly[1].Tokens.Output)
	}
}

// Model and effort survive a fold. They are what a bucket is split by, and a
// downsample that merged them would answer "what does opus at high effort cost" with
// the average of everything that ran that hour.
func TestCoarsenKeepsTheSplit(t *testing.T) {
	path := write(t,
		line(t, "2026-07-26T12:10:00.000Z", "claude-opus-5", "high", used(0, 10, 0, 0), nil),
		line(t, "2026-07-26T12:20:00.000Z", "claude-sonnet-5", "high", used(0, 20, 0, 0), nil))

	got := activity.Coarsen(read(t, path, activity.Cursor{}).Buckets, time.Hour)
	if len(got) != 2 {
		t.Fatalf("got %d buckets, want 2 (one per model)", len(got))
	}
}

// Age is what a store and a wire both want: detail where somebody might ask for a
// minute, and a sixtieth of the lines everywhere else.
func TestAgeFoldsOnlyWhatIsOldEnough(t *testing.T) {
	path := write(t,
		line(t, "2026-07-26T12:10:00.000Z", "claude-opus-5", "high", used(0, 10, 0, 0), nil),
		line(t, "2026-07-26T12:20:00.000Z", "claude-opus-5", "high", used(0, 20, 0, 0), nil),
		line(t, "2026-07-26T14:10:00.000Z", "claude-opus-5", "high", used(0, 30, 0, 0), nil),
		line(t, "2026-07-26T14:20:00.000Z", "claude-opus-5", "high", used(0, 40, 0, 0), nil))
	fine := read(t, path, activity.Cursor{}).Buckets

	got := activity.Age(fine, time.Date(2026, 7, 26, 13, 0, 0, 0, time.UTC))
	// The old pair folded into one hour; the recent pair kept its minutes.
	if len(got) != 3 {
		t.Fatalf("got %d buckets, want 3 (one hour and two minutes)", len(got))
	}
	var total int64
	for _, b := range got {
		total += b.Tokens.Output
	}
	if total != 100 {
		t.Errorf("folding lost tokens: %d, want 100", total)
	}
}

// A session can change model or effort mid-conversation, and a bucket that merged
// them would make "what does opus at high effort cost" unanswerable.
func TestModelAndEffortSplitABucket(t *testing.T) {
	path := write(t,
		line(t, "2026-07-26T12:10:00.000Z", "claude-opus-5", "high", used(0, 10, 0, 0), nil),
		line(t, "2026-07-26T12:20:00.000Z", "claude-sonnet-5", "high", used(0, 20, 0, 0), nil),
		line(t, "2026-07-26T12:30:00.000Z", "claude-opus-5", "medium", used(0, 30, 0, 0), nil))

	got := read(t, path, activity.Cursor{})
	if len(got.Buckets) != 3 {
		t.Fatalf("got %d buckets, want 3", len(got.Buckets))
	}
	for _, b := range got.Buckets {
		if b.Model != "opus" && b.Model != "sonnet" {
			t.Errorf("model %q was not reduced to the word a budget uses", b.Model)
		}
	}
}

// A model id this build has never seen keeps its own name. A wrong guess about
// what a model costs is worse than an unfamiliar word on a screen.
func TestAnUnknownModelKeepsItsName(t *testing.T) {
	path := write(t, line(t, "2026-07-26T12:10:00.000Z", "claude-fable-9", "high", used(0, 1, 0, 0), nil))
	if got := read(t, path, activity.Cursor{}).Buckets[0].Model; got != "claude-fable-9" {
		t.Errorf("model is %q, want it kept as it was", got)
	}
}

// Somebody else's session in the same project directory is somebody else's work.
func TestOnlyThisSessionIsCounted(t *testing.T) {
	mine := line(t, "2026-07-26T12:10:00.000Z", "claude-opus-5", "high", used(0, 10, 0, 0), nil)
	theirs := strings.Replace(mine, `"sessionId":"`+session+`"`, `"sessionId":"somebody-else"`, 1)
	if theirs == mine {
		t.Fatal("the fixture did not change session")
	}
	path := write(t, mine, theirs)

	got := read(t, path, activity.Cursor{})
	if got.Turns != 1 {
		t.Errorf("counted %d turns, want 1: another session's work was counted", got.Turns)
	}
}

// --- what it touched ------------------------------------------------------

func TestAReadCountsItsLines(t *testing.T) {
	path := write(t, line(t, "2026-07-26T12:10:00.000Z", "", "", nil, map[string]any{
		"type": "text",
		"file": map[string]any{"filePath": "/x/a.go", "numLines": 240, "content": "a\nb\n"},
	}))

	files := read(t, path, activity.Cursor{}).Buckets[0].Files
	if files.Reads != 1 || files.ReadLines != 240 {
		t.Errorf("a read counted %+v, want 1 read of 240 lines", files)
	}
}

// numLines is not always there. Counting the content is the fallback, and it beats
// reporting a read of zero lines — which is a measurement, and would be wrong.
func TestAReadWithoutACountFallsBackToTheContent(t *testing.T) {
	path := write(t, line(t, "2026-07-26T12:10:00.000Z", "", "", nil, map[string]any{
		"file": map[string]any{"filePath": "/x/a.go", "content": "one\ntwo\nthree\n"},
	}))

	if got := read(t, path, activity.Cursor{}).Buckets[0].Files.ReadLines; got != 3 {
		t.Errorf("read lines is %d, want 3", got)
	}
}

func TestAnEditCountsItsPatch(t *testing.T) {
	path := write(t, line(t, "2026-07-26T12:10:00.000Z", "", "", nil, map[string]any{
		"filePath": "/x/a.go",
		"structuredPatch": []map[string]any{{
			"lines": []string{" keep", "-gone", "-also gone", "+new", " keep"},
		}},
	}))

	files := read(t, path, activity.Cursor{}).Buckets[0].Files
	if files.Edits != 1 || files.Added != 1 || files.Removed != 2 {
		t.Errorf("an edit counted %+v, want 1 edit, +1, -2", files)
	}
}

func TestAWriteCountsWhatItWrote(t *testing.T) {
	path := write(t, line(t, "2026-07-26T12:10:00.000Z", "", "", nil, map[string]any{
		"type": "create", "filePath": "/x/new.go", "content": "package x\n\nfunc f() {}\n",
	}))

	files := read(t, path, activity.Cursor{}).Buckets[0].Files
	if files.Writes != 1 || files.WriteLines != 3 {
		t.Errorf("a write counted %+v, want 1 write of 3 lines", files)
	}
}

// A Bash result, an image, an interrupted call: not file work, and not damage.
func TestAResultThatIsNotFileWorkCountsNothing(t *testing.T) {
	path := write(t, line(t, "2026-07-26T12:10:00.000Z", "", "", nil, map[string]any{
		"stdout": "hello\nthere\n", "stderr": "", "interrupted": false,
	}))

	got := read(t, path, activity.Cursor{})
	if len(got.Buckets) != 1 {
		t.Fatalf("got %d buckets", len(got.Buckets))
	}
	if files := got.Buckets[0].Files; files != (activity.Files{}) {
		t.Errorf("a shell result counted as file work: %+v", files)
	}
	if got.Skipped != 0 {
		t.Errorf("a shell result was counted as damage (%d skipped)", got.Skipped)
	}
}

// --- reading again --------------------------------------------------------

func TestASecondReadStartsWhereTheFirstStopped(t *testing.T) {
	first := line(t, "2026-07-26T12:10:00.000Z", "claude-opus-5", "high", used(0, 10, 0, 0), nil)
	path := write(t, first)

	one := read(t, path, activity.Cursor{})
	if one.Turns != 1 {
		t.Fatalf("the first pass counted %d turns", one.Turns)
	}

	// Nothing appended: the same cursor must find nothing rather than the same turn.
	again := read(t, path, one.Cursor)
	if again.Turns != 0 {
		t.Errorf("re-reading the same bytes counted %d turns again", again.Turns)
	}

	// Now append, and only the new turn is counted.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(f, line(t, "2026-07-26T12:20:00.000Z", "claude-opus-5", "high", used(0, 7, 0, 0), nil)); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	third := read(t, path, one.Cursor)
	if third.Turns != 1 || third.Buckets[0].Tokens.Output != 7 {
		t.Errorf("the incremental pass read %d turns %+v", third.Turns, third.Buckets)
	}
}

// A transcript smaller than where the last read stopped is not the file that was
// being read. Starting again is right; saying so is what makes a doubled hour
// something an operator can see rather than something they cannot.
func TestARotatedTranscriptIsNoticed(t *testing.T) {
	path := write(t,
		line(t, "2026-07-26T12:10:00.000Z", "claude-opus-5", "high", used(0, 10, 0, 0), nil),
		line(t, "2026-07-26T12:20:00.000Z", "claude-opus-5", "high", used(0, 10, 0, 0), nil))
	first := read(t, path, activity.Cursor{})

	if err := os.WriteFile(path,
		[]byte(line(t, "2026-07-26T13:00:00.000Z", "claude-opus-5", "high", used(0, 3, 0, 0), nil)+"\n"),
		0o600); err != nil {
		t.Fatal(err)
	}

	got := read(t, path, first.Cursor)
	if !got.Reset {
		t.Error("a rotated transcript was read as though it were the same file")
	}
	if got.Turns != 1 {
		t.Errorf("after a rotation it counted %d turns, want 1", got.Turns)
	}
}

// --- when it cannot read --------------------------------------------------

func TestALineThatWillNotParseIsSkippedAndCounted(t *testing.T) {
	path := write(t,
		line(t, "2026-07-26T12:10:00.000Z", "claude-opus-5", "high", used(0, 10, 0, 0), nil),
		`{"this line is": truncated`,
		line(t, "2026-07-26T12:20:00.000Z", "claude-opus-5", "high", used(0, 10, 0, 0), nil))

	got := read(t, path, activity.Cursor{})
	if got.Turns != 2 {
		t.Errorf("a torn line cost %d of 2 turns", 2-got.Turns)
	}
	if got.Skipped != 1 {
		t.Errorf("skipped is %d, want 1: a torn line has to be visible", got.Skipped)
	}
}

// An unknown field means Claude shipped a release. Refusing would turn every
// upgrade into an outage of the measurement.
func TestAnUnknownFieldIsIgnored(t *testing.T) {
	raw := line(t, "2026-07-26T12:10:00.000Z", "claude-opus-5", "high", used(0, 10, 0, 0), nil)
	odd := strings.Replace(raw, `{"message"`, `{"somethingNew":{"a":1},"message"`, 1)
	if odd == raw {
		odd = strings.Replace(raw, `{`, `{"somethingNew":{"a":1},`, 1)
	}
	path := write(t, odd)

	got := read(t, path, activity.Cursor{})
	if got.Turns != 1 {
		t.Errorf("a line with a field this build does not know cost the turn: %+v", got)
	}
}

func TestATranscriptThatIsNotThereIsNotAFailure(t *testing.T) {
	got, err := activity.Read(filepath.Join(t.TempDir(), "nothing.jsonl"), session, activity.Cursor{})
	if err != nil {
		t.Fatalf("a missing transcript is an error: %v", err)
	}
	if len(got.Buckets) != 0 || got.Turns != 0 {
		t.Errorf("a missing transcript read something: %+v", got)
	}
}

func TestReadingNeedsAPathAndASession(t *testing.T) {
	if _, err := activity.Read("", session, activity.Cursor{}); err == nil {
		t.Error("a read with no path was accepted")
	}
	if _, err := activity.Read("x.jsonl", "", activity.Cursor{}); err == nil {
		t.Error("a read with no session was accepted")
	}
}

// The bucket key is what a rollup merges on, so two buckets that describe the same
// hour of the same session on the same model must key alike — and differ otherwise.
func TestTheKeyIsTheHourTheModelAndTheEffort(t *testing.T) {
	at := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	same := activity.Bucket{At: at, Model: "opus", Effort: "high"}
	if same.Key() != (activity.Bucket{At: at, Model: "opus", Effort: "high", Turns: 9}).Key() {
		t.Error("two buckets for the same hour key differently")
	}
	for _, other := range []activity.Bucket{
		{At: at.Add(time.Hour), Model: "opus", Effort: "high"},
		{At: at, Model: "sonnet", Effort: "high"},
		{At: at, Model: "opus", Effort: "low"},
	} {
		if same.Key() == other.Key() {
			t.Errorf("%+v keys the same as %+v", same, other)
		}
	}
}
