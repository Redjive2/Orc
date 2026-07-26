package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/macmuffin/internal/task"
)

// tombstone is one line of the deletion log.
type tombstone struct {
	Version int    `json:"version"`
	Task    string `json:"task"`
	By      string `json:"by"`
	At      string `json:"at"`
	Subs    int    `json:"subtasks,omitempty"`
	With    int    `json:"collaborators,omitempty"`
}

// Tombstone is a recorded deletion.
type Tombstone struct {
	Task          task.Name
	By            user.Name
	At            time.Time
	Subtasks      int
	Collaborators int
}

// Delete removes a task, recording the deletion before anything is erased.
//
// The order is the whole point. The tombstone is appended first, so a process
// killed midway leaves a task that `verify` can name — "alice deleted this and
// it did not finish" — rather than a half-erased directory nobody can account
// for. Deleting first and recording after would make a crash indistinguishable
// from a task that never existed.
//
// It is the only irreversible operation in the store, so it takes the task's
// lock for the whole sequence: no one may claim or edit a task while it is
// being removed.
func (s *Store) Delete(name task.Name, by user.Name) error {
	if name.Zero() || by.Zero() {
		return fault.Internal{Where: "store.Delete", Detail: "task and actor are both required"}
	}

	return s.withLock(name, func() error {
		current, err := s.Load(name)
		if err != nil {
			return err
		}
		_, subs := current.Progress()

		if err := s.appendTombstone(tombstone{
			Version: Version,
			Task:    name.String(),
			By:      by.String(),
			At:      clock.Format(s.clock.Now()),
			Subs:    subs,
			With:    len(current.Collaborators()),
		}); err != nil {
			return err
		}

		// Any worktree bound to the task goes with it: a binding pointing at a
		// task that is gone would make the hook enforce a scope nobody owns.
		if wt, bound := current.Worktree(); bound {
			if err := s.Unbind(wt); err != nil {
				return err
			}
		}

		if err := s.ops.removeAll(s.taskDir(name)); err != nil {
			return fault.IO{Op: "remove", Path: s.taskDir(name), Err: err}
		}
		return nil
	})
}

// appendTombstone records a deletion. It must be called with the task's lock
// held.
//
// The log is store-wide rather than per-task, because the thing it records is
// precisely that the per-task directory no longer exists. Appends from
// different tasks can interleave, so it takes the same one-line-at-a-time
// discipline as a journal.
func (s *Store) appendTombstone(t tombstone) error {
	line, err := json.Marshal(t)
	if err != nil {
		return fault.Internal{Where: "store.appendTombstone", Detail: err.Error()}
	}
	if bytes.ContainsAny(line, "\n\r") {
		return fault.Internal{Where: "store.appendTombstone", Detail: "encoded tombstone contains a newline"}
	}
	return s.appendLine(filepath.Join(s.root, tombstoneFile), line)
}

// Tombstones returns every recorded deletion, oldest first.
//
// Like a journal, an unreadable *final* line is an interrupted append and is
// dropped; anything earlier is corruption and is reported. A deletion log that
// silently skipped a line would be a log that cannot answer the one question it
// exists for.
func (s *Store) Tombstones() ([]Tombstone, int, error) {
	path := filepath.Join(s.root, tombstoneFile)

	data, err := s.ops.readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, fault.IO{Op: "read", Path: path, Err: err}
	}
	if len(data) > MaxJournalSize {
		return nil, 0, fault.Parse{Path: path, Reason: fmt.Sprintf(
			"deletion log is %d bytes, limit is %d", len(data), MaxJournalSize)}
	}

	complete := len(data) == 0 || data[len(data)-1] == '\n'
	lines := bytes.Split(data, []byte("\n"))
	if complete && len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}

	var out []Tombstone
	skipped := 0

	for i, raw := range lines {
		lineNo := i + 1
		last := i == len(lines)-1

		if len(raw) == 0 {
			if last && !complete {
				continue
			}
			return nil, 0, fault.Parse{Path: path, Line: lineNo, Reason: "empty line in the deletion log"}
		}

		got, err := decodeTombstone(path, lineNo, raw)
		if err != nil {
			if last && !complete {
				skipped = len(raw)
				break
			}
			return nil, 0, err
		}
		out = append(out, got)
	}
	return out, skipped, nil
}

func decodeTombstone(path string, line int, raw []byte) (Tombstone, error) {
	bad := func(format string, args ...any) (Tombstone, error) {
		return Tombstone{}, fault.Parse{Path: path, Line: line, Reason: fmt.Sprintf(format, args...)}
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	var t tombstone
	if err := dec.Decode(&t); err != nil {
		return bad("deletion record: %s", err)
	}
	if dec.More() {
		return bad("deletion record has trailing content")
	}
	if t.Version != Version {
		return bad("deletion record is version %d, this macmuffin writes version %d", t.Version, Version)
	}

	name, err := task.ParseName(t.Task)
	if err != nil {
		return bad("deletion record names a bad task: %s", err)
	}
	by, err := user.Parse(t.By)
	if err != nil {
		return bad("deletion record names a bad actor: %s", err)
	}
	at, err := clock.Parse(t.At)
	if err != nil {
		return bad("deletion record has a bad timestamp: %s", err)
	}
	if t.Subs < 0 || t.With < 0 {
		return bad("deletion record has negative counts")
	}

	return Tombstone{Task: name, By: by, At: at, Subtasks: t.Subs, Collaborators: t.With}, nil
}
