package probe

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"orc/orcprobe/internal/clock"
	"orc/orcprobe/internal/fault"
	"orc/orcprobe/internal/snapshot"
)

// CheckpointsDir holds saved states, one directory per label.
const CheckpointsDir = "checkpoints"

// checkpointed names the parts of a probe a checkpoint captures and a restore
// puts back: everything a run inside the probe can change.
//
// What is deliberately *not* captured is everything that identifies the probe
// rather than its contents — probe.json, the stamp, identities.json, env, bin/,
// and the manifest. Restoring those would either be a no-op or would rewrite
// the probe's identity, and a rewind that could change who you are inside a
// probe is a rewind nobody can reason about. The manifest in particular is
// append-only *through* a restore: the rewind is itself an event in the
// probe's history, not a way to erase one.
var checkpointed = []string{StateDir, RepoDir, ClaudeDir}

// Checkpoint is one saved state.
type Checkpoint struct {
	Label string
	At    time.Time
	Bytes int64
	Files int
}

// CheckLabel validates a checkpoint label. Labels are path elements, so they
// are held to the same shape probe names are.
func CheckLabel(raw string) (string, error) {
	label := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case label == "":
		return "", fault.Usage{Reason: "checkpoint label is empty"}
	case len(label) > MaxNameLen:
		return "", fault.Usage{Reason: fmt.Sprintf("label %q is longer than %d characters", raw, MaxNameLen)}
	case label == "." || label == "..":
		return "", fault.Usage{Reason: fmt.Sprintf("label %q is reserved", label)}
	}
	for i, r := range label {
		if !allowed(r) {
			return "", fault.Usage{Reason: fmt.Sprintf(
				"label %q contains %q at position %d; use letters, digits, and . _ -", raw, r, i+1)}
		}
	}
	if !alphanumeric(rune(label[0])) {
		return "", fault.Usage{Reason: fmt.Sprintf("label %q must start with a letter or digit", raw)}
	}
	return label, nil
}

// Save captures the probe's current state under a label.
//
// A label that already exists is refused rather than overwritten. Overwriting
// would be the one operation in this tool that destroys state without saying
// so — `orcprobe save before-migration` typed twice, an hour apart, would
// silently discard the first hour.
func (s *Store) Save(p *Probe, label string) (Checkpoint, error) {
	clean, err := CheckLabel(label)
	if err != nil {
		return Checkpoint{}, err
	}
	dir := p.Path(CheckpointsDir, clean)
	if _, err := os.Stat(dir); err == nil {
		return Checkpoint{}, fault.Conflict{Path: dir,
			Reason: "checkpoint " + clean + " already exists; pick another label or restore it first"}
	} else if !os.IsNotExist(err) {
		return Checkpoint{}, fault.IO{Op: "check for", Path: dir, Err: err}
	}

	if err := os.MkdirAll(dir, snapshot.DirMode); err != nil {
		return Checkpoint{}, fault.IO{Op: "create", Path: dir, Err: err}
	}

	out := Checkpoint{Label: clean, At: s.clock.Now()}
	for _, part := range checkpointed {
		src := p.Path(part)
		if _, err := os.Stat(src); err != nil {
			if os.IsNotExist(err) {
				continue // a probe made with --no-repo has no repo to save
			}
			return Checkpoint{}, fault.IO{Op: "look at", Path: src, Err: err}
		}
		rep, err := snapshot.Copy(filepath.Join(dir, part), src, snapshot.Options{})
		if err != nil {
			_ = os.RemoveAll(dir)
			return Checkpoint{}, err
		}
		out.Bytes += rep.Bytes
		out.Files += rep.Files
	}

	man := OpenManifest(p.Dir(), s.clock)
	if err := man.Add(ActNote, "checkpoint "+clean,
		fmt.Sprintf("saved: %d files, %d bytes", out.Files, out.Bytes)); err != nil {
		return Checkpoint{}, err
	}
	return out, nil
}

// Restore puts a probe back to a saved state.
//
// Each part is replaced through a rename rather than by writing over the live
// one: the new copy is built beside it, the old is moved aside, the new is
// moved in, and only then is the old removed. A restore killed halfway leaves
// either the old state or the new, never a directory half of each.
func (s *Store) Restore(p *Probe, label string) error {
	clean, err := CheckLabel(label)
	if err != nil {
		return err
	}
	dir := p.Path(CheckpointsDir, clean)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			near, _ := s.Checkpoints(p)
			labels := make([]string, 0, len(near))
			for _, c := range near {
				labels = append(labels, c.Label)
			}
			return fault.NotFound{Target: clean, Near: labels}
		}
		return fault.IO{Op: "look at", Path: dir, Err: err}
	}

	for _, part := range checkpointed {
		saved := filepath.Join(dir, part)
		if _, err := os.Stat(saved); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fault.IO{Op: "look at", Path: saved, Err: err}
		}
		if err := swapIn(p.Path(part), saved); err != nil {
			return err
		}
	}

	// The rewind is itself an event. A probe that could be rolled back without
	// leaving a trace would be one whose manifest describes a history it no
	// longer has.
	man := OpenManifest(p.Dir(), s.clock)
	return man.Add(ActNote, "checkpoint "+clean, "restored; everything since it was saved is gone")
}

// swapIn replaces live with a copy of saved, atomically enough to survive a
// kill at any point.
func swapIn(live, saved string) error {
	staging := live + ".restoring"
	previous := live + ".previous"

	for _, leftover := range []string{staging, previous} {
		if err := os.RemoveAll(leftover); err != nil {
			return fault.IO{Op: "clear", Path: leftover, Err: err}
		}
	}
	if _, err := snapshot.Copy(staging, saved, snapshot.Options{}); err != nil {
		return err
	}

	moved := false
	if _, err := os.Stat(live); err == nil {
		if err := os.Rename(live, previous); err != nil {
			_ = os.RemoveAll(staging)
			return fault.IO{Op: "move aside", Path: live, Err: err}
		}
		moved = true
	} else if !os.IsNotExist(err) {
		_ = os.RemoveAll(staging)
		return fault.IO{Op: "look at", Path: live, Err: err}
	}

	if err := os.Rename(staging, live); err != nil {
		// Put the old one back rather than leaving the probe with neither.
		if moved {
			_ = os.Rename(previous, live)
		}
		_ = os.RemoveAll(staging)
		return fault.IO{Op: "move into place", Path: live, Err: err}
	}
	if moved {
		if err := os.RemoveAll(previous); err != nil {
			return fault.IO{Op: "remove", Path: previous, Err: err}
		}
	}
	syncDir(filepath.Dir(live))
	return nil
}

// Checkpoints lists a probe's saved states, oldest first.
func (s *Store) Checkpoints(p *Probe) ([]Checkpoint, error) {
	dir := p.Path(CheckpointsDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fault.IO{Op: "list", Path: dir, Err: err}
	}

	var out []Checkpoint
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		label, err := CheckLabel(e.Name())
		if err != nil {
			continue // not something orcprobe wrote
		}
		info, err := e.Info()
		if err != nil {
			return nil, fault.IO{Op: "look at", Path: filepath.Join(dir, e.Name()), Err: err}
		}
		out = append(out, Checkpoint{Label: label, At: clock.Normalise(info.ModTime())})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

// CheckpointDir returns where a label's state lives, for a diff.
func (p *Probe) CheckpointDir(label string) string { return p.Path(CheckpointsDir, label) }
