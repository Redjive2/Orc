package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/orc/internal/activity"
)

// What an identity has done, kept beside its journal.
//
// Two files, and the split is the usual one in this tree: an append-only record of
// what happened, and a small mutable note saying how far something has got through
// reading it.
//
//	identities/<name>/activity.jsonl   one line per read, totals per hour bucket
//	identities/<name>/activity.cursor  which transcript, and where the last read stopped
//
// Beside the identity's journal rather than inside `session/`, because a rollup
// outlives any one session: `orc refresh` mints a new id under the same identity,
// and a history that went with the session would restart every time somebody asked
// for a fresh context.
//
// Each line is a **delta**, and folding sums them. A bucket's total therefore only
// grows, which is what makes it safe to send: a mirror that receives the same
// bucket twice writes the same number, and one that misses a sync catches up
// whole. Nothing here has to be exactly-once, which is the only reliable thing to
// ask of a file two processes may both be appending to.

const (
	activityFile   = "activity.jsonl"
	activityCursor = "activity.cursor"
)

// MaxActivityLine bounds one rollup line, which is a handful of numbers.
const MaxActivityLine = 4 << 10

// storedBucket is one line of the rollup.
type storedBucket struct {
	At     string          `json:"at"`
	Model  string          `json:"model,omitempty"`
	Effort string          `json:"effort,omitempty"`
	Turns  int             `json:"turns,omitempty"`
	Tokens activity.Tokens `json:"tokens,omitzero"`
	Files  activity.Files  `json:"files,omitzero"`
}

// Rollup is how far the reader has got with one identity's transcript.
type Rollup struct {
	// Session is the conversation the cursor describes. A different one starts
	// again from the beginning of its own file rather than at somebody else's
	// offset.
	Session string          `json:"session"`
	Cursor  activity.Cursor `json:"cursor"`
	// At is when the last read happened, for `orc doctor` and for anybody asking
	// why a fleet's figures stop an hour ago.
	At string `json:"at,omitempty"`
}

func (s *Store) activityPath(name user.Name) string {
	return filepath.Join(s.identityDir(name), activityFile)
}

func (s *Store) activityCursorPath(name user.Name) string {
	return filepath.Join(s.identityDir(name), activityCursor)
}

// ActivityRollup returns where the last read stopped.
//
// A missing or unreadable cursor is "nothing has been read", which starts the next
// pass at the beginning of the transcript. That is the safe direction: re-reading
// costs one pass over a file, and skipping costs an hour of somebody's fleet
// nobody can get back.
func (s *Store) ActivityRollup(name user.Name) (Rollup, bool) {
	data, err := s.ops.readFile(s.activityCursorPath(name))
	if err != nil {
		return Rollup{}, false
	}
	var got Rollup
	if err := json.Unmarshal(data, &got); err != nil {
		return Rollup{}, false
	}
	return got, got.Session != ""
}

// RecordActivity appends what a read found and moves the cursor.
//
// In that order, deliberately. A crash between the two costs a re-read of bytes
// already counted, which shows up as one hour counted twice; the other order would
// lose them, and a lost hour is invisible. Double counting is a thing an operator
// can see.
func (s *Store) RecordActivity(name user.Name, buckets []activity.Bucket, at Rollup) error {
	if err := s.refuseWrite(); err != nil {
		return err
	}
	if name.Zero() {
		return fault.Internal{Where: "store.RecordActivity", Detail: "no identity named"}
	}

	// One line at a time, through the same appender every journal in this tree
	// uses: it refuses a line with a newline in it and flushes before returning, so
	// a rollup that reported success survives a power cut like everything else.
	for _, b := range buckets {
		raw, err := json.Marshal(storedBucket{
			At: clock.Format(b.At), Model: b.Model, Effort: b.Effort,
			Turns: b.Turns, Tokens: b.Tokens, Files: b.Files,
		})
		if err != nil {
			return fault.Internal{Where: "store.RecordActivity", Detail: err.Error()}
		}
		if len(raw)+1 > MaxActivityLine {
			return fault.Internal{Where: "store.RecordActivity", Detail: "a bucket does not fit on a line"}
		}
		if err := s.appendLine(s.activityPath(name), raw); err != nil {
			return err
		}
	}

	at.At = clock.Format(s.Now())
	data, err := json.Marshal(at)
	if err != nil {
		return fault.Internal{Where: "store.RecordActivity", Detail: err.Error()}
	}
	return s.writeFile(s.activityCursorPath(name), append(data, '\n'))
}

// Activity folds the rollup into one bucket per hour, model and effort.
//
// `since` bounds what comes back; a zero time is everything on disk. A line that
// will not parse is skipped: the file is append-only and a reader may catch a
// half-written final line, which is ordinary rather than damage.
func (s *Store) Activity(name user.Name, since time.Time) ([]activity.Bucket, error) {
	data, err := s.ops.readFile(s.activityPath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fault.IO{Op: "read", Path: s.activityPath(name), Err: err}
	}

	byKey := map[string]*activity.Bucket{}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var stored storedBucket
		if err := json.Unmarshal(line, &stored); err != nil {
			continue
		}
		at, err := clock.Parse(stored.At)
		if err != nil || (!since.IsZero() && at.Before(since)) {
			continue
		}

		got := activity.Bucket{At: at, Model: stored.Model, Effort: stored.Effort}
		into, ok := byKey[got.Key()]
		if !ok {
			into = &got
			byKey[got.Key()] = into
		}
		into.Turns += stored.Turns
		into.Tokens.Add(stored.Tokens)
		into.Files.Add(stored.Files)
	}

	out := make([]activity.Bucket, 0, len(byKey))
	for _, b := range byKey {
		out = append(out, *b)
	}
	// Oldest first, which is the order every screen and chart wants.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].At.Before(out[j-1].At); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// PruneActivity drops buckets older than `before` by rewriting the journal.
//
// The same discipline as everywhere else in this tree: fold, keep what is wanted,
// write it whole. A rollup grows by a handful of lines an hour, so this is a file
// of thousands rather than millions and rewriting it is cheaper than any scheme
// that avoided rewriting it.
func (s *Store) PruneActivity(name user.Name, before time.Time) error {
	if err := s.refuseWrite(); err != nil {
		return err
	}
	kept, err := s.Activity(name, before)
	if err != nil {
		return err
	}
	// Rewriting anyway, so this is where the old detail goes. A minute-by-minute
	// reading is worth its lines for as long as somebody might ask about a minute;
	// past that the same numbers fit in a sixtieth of the file, and the fold is the
	// one the reader above already performs.
	kept = activity.Age(kept, s.Now().Add(-activity.Fine))
	if len(kept) == 0 {
		if err := s.ops.remove(s.activityPath(name)); err != nil && !os.IsNotExist(err) {
			return fault.IO{Op: "remove", Path: s.activityPath(name), Err: err}
		}
		return nil
	}

	var lines bytes.Buffer
	for _, b := range kept {
		raw, err := json.Marshal(storedBucket{
			At: clock.Format(b.At), Model: b.Model, Effort: b.Effort,
			Turns: b.Turns, Tokens: b.Tokens, Files: b.Files,
		})
		if err != nil {
			return fault.Internal{Where: "store.PruneActivity", Detail: err.Error()}
		}
		lines.Write(raw)
		lines.WriteByte('\n')
	}
	return s.writeFile(s.activityPath(name), lines.Bytes())
}
