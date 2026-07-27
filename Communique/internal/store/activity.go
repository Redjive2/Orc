package store

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"orc/cq/internal/atomic"
	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
)

// The one part of a snapshot that is not replaced wholesale.
//
// Everything else here follows the rule at the top of store.go: a snapshot is the
// machine's current state and the newest one wins, so there is nothing to merge and
// a half-written one is impossible. That is exactly wrong for a *series*. A rate
// needs history, the mirror is the only place history can accumulate — the agent
// machine prunes its own — and a snapshot carries only a window.
//
// So buckets are merged rather than replaced, and the merge is trivial because of a
// property the agent machine guarantees: **a bucket total only ever grows**. It is a
// fold of deltas over one hour, so:
//
//   - receiving the same bucket twice writes the same number;
//   - receiving six at once, out of order, after a machine was offline all
//     afternoon, lands them all correctly;
//   - nothing has to arrive exactly once, which is the only reliable thing to ask
//     of a link between two machines.
//
// Last write wins, per (machine, identity, hour, model, effort). Append-only, one
// file per month, folded on read.

const activityDir = "activity"

// MaxActivityMonths is how far back the series is kept.
//
// A year, because the questions this answers are seasonal — "are we spending more
// than we were" — and because a month of buckets for a ten-agent fleet is a few
// thousand short lines. Older files are dropped whole rather than rewritten.
const MaxActivityMonths = 12

// storedActivity is one line: a bucket, and whose it is.
type storedActivity struct {
	Identity string `json:"identity"`
	// At is the hour, in the tree's format, exactly as the agent machine wrote it.
	// It is compared as a string when merging and parsed only for windows: two
	// spellings of one instant would be two buckets, so the spelling is the key.
	At     string                  `json:"at"`
	Model  string                  `json:"model,omitempty"`
	Effort string                  `json:"effort,omitempty"`
	Turns  int                     `json:"turns,omitempty"`
	Tokens protocol.ActivityTokens `json:"tokens,omitzero"`
	Files  protocol.ActivityFiles  `json:"files,omitzero"`
	// Seen is when this mirror received it, which is what makes the newest write
	// findable when two lines describe one bucket.
	Seen string `json:"seen"`
}

func (s *Store) activityDir(machine protocol.MachineID) string {
	return s.path(activityDir, string(machine))
}

// MergeActivity records the buckets a snapshot carried.
//
// It appends rather than rewrites, and never fails a sync: a mirror that refused a
// snapshot because a chart could not be updated would be trading the thing it is
// for the thing it shows.
func (s *Store) MergeActivity(machine protocol.MachineID, fleet protocol.Fleet, at time.Time) error {
	if err := machine.Validate(); err != nil {
		return err
	}
	if at.IsZero() {
		return fault.Internal{Where: "store.MergeActivity", Detail: "zero timestamp"}
	}

	var lines strings.Builder
	seen := at.UTC().Format(time.RFC3339Nano)
	for _, id := range fleet.Identities {
		for _, b := range id.Buckets {
			if strings.TrimSpace(b.At) == "" {
				continue
			}
			raw, err := json.Marshal(storedActivity{
				Identity: id.Name, At: b.At, Model: b.Model, Effort: b.Effort,
				Turns: b.Turns, Tokens: b.Tokens, Files: b.Files, Seen: seen,
			})
			if err != nil {
				return fault.Internal{Where: "store.MergeActivity", Detail: err.Error()}
			}
			lines.Write(raw)
			lines.WriteByte('\n')
		}
	}
	if lines.Len() == 0 {
		return nil
	}

	dir := s.activityDir(machine)
	if err := atomic.MkdirAll(dir, dirMode); err != nil {
		return err
	}
	path := filepath.Join(dir, at.UTC().Format("2006-01")+".jsonl")

	s.series.Lock()
	defer s.series.Unlock()

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, fileMode)
	if err != nil {
		return fault.IO{Op: "open", Subject: path, Err: err}
	}
	defer func() { _ = f.Close() }()

	if _, err := f.WriteString(lines.String()); err != nil {
		return fault.IO{Op: "append to", Subject: path, Err: err}
	}
	return f.Sync()
}

// Activity folds a machine's series, newest write per bucket winning.
//
// An identity of "" is every identity. A zero `since` is everything kept.
func (s *Store) Activity(machine protocol.MachineID, identity string, since time.Time) ([]protocol.ActivityBucket, error) {
	if err := machine.Validate(); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(s.activityDir(machine))
	if err != nil {
		if os.IsNotExist(err) {
			// A machine that has never sent a bucket is not an error. It is an
			// older orc, or a fleet that has done nothing yet.
			return nil, nil
		}
		return nil, fault.IO{Op: "list", Subject: s.activityDir(machine), Err: err}
	}

	type winner struct {
		bucket protocol.ActivityBucket
		seen   string
	}
	best := map[string]winner{}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		if err := foldActivity(filepath.Join(s.activityDir(machine), name), identity, since,
			func(got storedActivity) {
				key := got.Identity + "\x00" + got.At + "\x00" + got.Model + "\x00" + got.Effort
				// Last write wins, and "last" is what this mirror saw last rather
				// than what a file happens to hold last: two months' files are read
				// in order, but a clock that jumped would otherwise let an older
				// reading overwrite a newer one.
				if was, ok := best[key]; ok && was.seen > got.Seen {
					return
				}
				best[key] = winner{
					bucket: protocol.ActivityBucket{
						At: got.At, Model: got.Model, Effort: got.Effort,
						Turns: got.Turns, Tokens: got.Tokens, Files: got.Files,
					},
					seen: got.Seen,
				}
			}); err != nil {
			return nil, err
		}
	}

	out := make([]protocol.ActivityBucket, 0, len(best))
	for _, w := range best {
		out = append(out, w.bucket)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].At != out[j].At {
			return out[i].At < out[j].At
		}
		if out[i].Model != out[j].Model {
			return out[i].Model < out[j].Model
		}
		return out[i].Effort < out[j].Effort
	})
	return out, nil
}

// foldActivity reads one month's file, handing each line that passes the filters to
// `keep`.
//
// A line that will not parse is skipped rather than fatal: this file is appended to
// by a live server, and a reader can catch a half-written final line. Losing one
// bucket from a chart is not worth refusing to draw the chart.
func foldActivity(path, identity string, since time.Time, keep func(storedActivity)) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fault.IO{Op: "read", Subject: path, Err: err}
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 16<<10), 64<<10)
	for scanner.Scan() {
		var got storedActivity
		if err := json.Unmarshal(scanner.Bytes(), &got); err != nil {
			continue
		}
		if identity != "" && got.Identity != identity {
			continue
		}
		if !since.IsZero() {
			at, err := time.Parse(time.RFC3339Nano, got.At)
			if err != nil || at.Before(since) {
				continue
			}
		}
		keep(got)
	}
	return nil
}

// Identities lists who a machine's series has buckets for, in name order.
func (s *Store) ActivityIdentities(machine protocol.MachineID, since time.Time) ([]string, error) {
	buckets, err := s.ActivityByIdentity(machine, since)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(buckets))
	for name := range buckets {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// ActivityByIdentity folds the series and groups it, which is the shape every
// screen wants: one series per agent.
func (s *Store) ActivityByIdentity(machine protocol.MachineID, since time.Time) (map[string][]protocol.ActivityBucket, error) {
	if err := machine.Validate(); err != nil {
		return nil, err
	}

	names := map[string]bool{}
	entries, err := os.ReadDir(s.activityDir(machine))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string][]protocol.ActivityBucket{}, nil
		}
		return nil, fault.IO{Op: "list", Subject: s.activityDir(machine), Err: err}
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		if err := foldActivity(filepath.Join(s.activityDir(machine), e.Name()), "", since,
			func(got storedActivity) { names[got.Identity] = true }); err != nil {
			return nil, err
		}
	}

	out := make(map[string][]protocol.ActivityBucket, len(names))
	for name := range names {
		got, err := s.Activity(machine, name, since)
		if err != nil {
			return nil, err
		}
		out[name] = got
	}
	return out, nil
}

// PruneActivity drops whole months older than the retention.
//
// By file rather than by line: a month is the unit the series is written in, and
// rewriting a file to drop its first three days would be work in exchange for
// nothing anybody asked for.
func (s *Store) PruneActivity(machine protocol.MachineID, now time.Time) error {
	if err := machine.Validate(); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.activityDir(machine))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fault.IO{Op: "list", Subject: s.activityDir(machine), Err: err}
	}

	oldest := now.UTC().AddDate(0, -MaxActivityMonths, 0).Format("2006-01")
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".jsonl")
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") || name >= oldest {
			continue
		}
		if err := atomic.Remove(filepath.Join(s.activityDir(machine), e.Name())); err != nil {
			return err
		}
	}
	return nil
}
