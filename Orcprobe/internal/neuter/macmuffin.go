package neuter

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"orc/orcprobe/internal/clock"
	"orc/orcprobe/internal/fault"
	"orc/orcprobe/internal/snapshot"
)

// Macmuffin's layout, from its plan §4. Orcprobe reads a format another tool
// owns, so the names live here in one place: if Macmuffin's layout moves, this
// is the file that moves with it.
const (
	tasksDir     = "tasks"
	worktreesDir = "worktrees"
	outboxDir    = "outbox"
	journalFile  = "journal.jsonl"
)

// event is one line of a Macmuffin journal. Only the fields ownership depends
// on are named; everything else in a line is passed over, because orcprobe is
// not trying to reimplement Macmuffin's state — only to find who holds what.
type event struct {
	Op    string `json:"op"`
	By    string `json:"by"`
	Agent string `json:"agent,omitempty"`
	At    string `json:"at"`
}

// The ops orcprobe reads, and the one it writes.
//
// Only `leave` is written, and only for a collaborator. Releasing an owner has
// no valid event today, and the reason is worth stating where the code is:
//
//   - Macmuffin refuses a journal line whose op it does not know — so inventing
//     `release` would not release anything, it would make the whole task
//     unreadable inside the probe;
//   - and its `leave` explicitly refuses an owner, because "a task is never
//     orphaned by accident".
//
// Both refusals are right for the real world and leave orcprobe with nothing to
// append. So an owned task keeps its owner, and the probe says so loudly rather
// than quietly writing something that breaks it. §10 of the orcprobe plan
// carries the one-line change to Macmuffin that closes this.
const (
	opLeave  = "leave"
	opClaim  = "claim"
	opAssign = "assign"
	opInvite = "invite"
	opKick   = "kick"
	// opRelease is what orcprobe would append if Macmuffin defined it. It is
	// named here so the day it exists, this is the one line that changes.
	opRelease = "release"
)

// macmuffin releases every claim, removes every collaborator, drops every
// worktree binding, and deletes every undelivered notification.
func macmuffin(s Spec, rep *Report) error {
	entries, err := os.ReadDir(filepath.Join(s.MacmuffinDir, tasksDir))
	if err != nil && !os.IsNotExist(err) {
		return fault.IO{Op: "list", Path: filepath.Join(s.MacmuffinDir, tasksDir), Err: err}
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if err := releaseTask(s, rep, e.Name()); err != nil {
			return err
		}
	}
	if err := dropWorktrees(s, rep); err != nil {
		return err
	}
	return dropOutbox(s, rep)
}

// releaseTask replays one task's journal and appends what an agent walking away
// from it would have appended.
func releaseTask(s Spec, rep *Report, name string) error {
	path := filepath.Join(s.MacmuffinDir, tasksDir, name, journalFile)
	owner, collaborators, err := replay(path)
	if err != nil {
		return err
	}
	if owner == "" && len(collaborators) == 0 {
		return nil
	}

	// Collaborators leave, one appended event each. This much is ordinary
	// Macmuffin: a collaborator dropping out is a thing it already understands.
	for _, who := range collaborators {
		if err := appendEvent(path, event{Op: opLeave, By: who, At: clock.Format(s.Clock.Now())}); err != nil {
			return err
		}
		rep.Collaborators++
		rep.add(ActDrop, "task "+name, "collaborator "+who+" left")
	}

	// The owner stays, because there is nothing valid to append (see the op
	// constants above). This is the one place a probe falls short of "nobody is
	// working here", so it is recorded as a deferred guarantee rather than a
	// drop — a probe must never claim to have scrubbed something it did not.
	if owner != "" {
		rep.Unreleased = append(rep.Unreleased, Unreleased{Task: name, Owner: owner})
		rep.add(ActDefer, "task "+name,
			"still owned by "+owner+": macmuffin has no `release` op and refuses an owner's `leave`, "+
				"so orcprobe has no valid event to append. Nothing was started — but this task does not look unclaimed.")
	}
	return nil
}

// replay folds a journal down to the two facts that make a task look live.
//
// Unknown ops are passed over rather than refused: Macmuffin is still being
// built, and orcprobe refusing to make a probe because a task carried an op it
// had not heard of would be this tool getting in the way of the tool it exists
// to test. What is *not* tolerated is a line that does not parse at all, except
// as the final line — the same rule Macmuffin and Mailman apply to their own
// journals, for the same reason.
func replay(path string) (owner string, collaborators []string, err error) {
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return "", nil, nil
		}
		return "", nil, fault.IO{Op: "read", Path: path, Err: readErr}
	}

	var lines []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return "", nil, fault.IO{Op: "read", Path: path, Err: scanErr}
	}
	complete := bytes.HasSuffix(data, []byte("\n"))

	members := map[string]bool{}
	for i, line := range lines {
		if len(bytes.TrimSpace([]byte(line))) == 0 {
			continue
		}
		var ev event
		if jsonErr := json.Unmarshal([]byte(line), &ev); jsonErr != nil {
			if i == len(lines)-1 && !complete {
				break // an interrupted append; the copy caught it mid-write
			}
			return "", nil, fault.Parse{Path: path, Line: i + 1, Reason: "task journal: " + jsonErr.Error()}
		}

		switch ev.Op {
		case opClaim:
			owner = ev.By
		case opAssign:
			owner = ev.Agent
		case opRelease:
			owner = ""
		case opInvite:
			if ev.Agent != "" {
				members[ev.Agent] = true
			}
		case opLeave:
			delete(members, ev.By)
		case opKick:
			delete(members, ev.Agent)
		}
	}

	for who := range members {
		collaborators = append(collaborators, who)
	}
	sort.Strings(collaborators)
	return owner, collaborators, nil
}

// appendEvent adds one line, flushed before returning.
func appendEvent(path string, ev event) error {
	line, err := json.Marshal(ev)
	if err != nil {
		return fault.Internal{Where: "neuter.appendEvent", Detail: err.Error()}
	}
	if bytes.ContainsRune(line, '\n') {
		return fault.Internal{Where: "neuter.appendEvent", Detail: "encoded event contains a newline"}
	}

	// Opened for reading as well as appending: ensureNewline has to look at the
	// last byte, and a write-only descriptor cannot be read from — which would
	// make every append take the "add one just in case" path and leave a blank
	// line between every event.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, snapshot.FileMode)
	if err != nil {
		return fault.IO{Op: "open for appending", Path: path, Err: err}
	}
	defer func() { _ = f.Close() }()

	// A journal whose last line was truncated by the copy would otherwise get
	// this event welded onto the end of it, turning two half-lines into one
	// unparseable one.
	if err := ensureNewline(f, path); err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fault.IO{Op: "append to", Path: path, Err: err}
	}
	if err := f.Sync(); err != nil {
		return fault.IO{Op: "flush", Path: path, Err: err}
	}
	return nil
}

func ensureNewline(f *os.File, path string) error {
	info, err := f.Stat()
	if err != nil {
		return fault.IO{Op: "look at", Path: path, Err: err}
	}
	if info.Size() == 0 {
		return nil
	}
	tail := make([]byte, 1)
	if _, err := f.ReadAt(tail, info.Size()-1); err != nil {
		// Failing to read the tail is not a reason to refuse an append, and a
		// stray blank line is cheaper than two half-lines welded into one — but
		// it is still a blank line another tool's parser has to tolerate, so it
		// is the fallback and not the path.
		if _, err := f.Write([]byte("\n")); err != nil {
			return fault.IO{Op: "append to", Path: path, Err: err}
		}
		return nil
	}
	if tail[0] != '\n' {
		if _, err := f.Write([]byte("\n")); err != nil {
			return fault.IO{Op: "append to", Path: path, Err: err}
		}
	}
	return nil
}

// dropWorktrees removes every task-to-worktree binding.
//
// They are dropped rather than repointed at the probe's repo copy, and the
// reason is worth stating: bindings are stored under `worktrees/<hash>.json`,
// keyed by a hash of the resolved path, and orcprobe does not know Macmuffin's
// hash function. A rewritten path under an unchanged key is a binding the
// lookup can never find — worse than none, because it looks present. A dropped
// binding is honest, and rebinding inside the probe is one `muff worktree` away.
func dropWorktrees(s Spec, rep *Report) error {
	dir := filepath.Join(s.MacmuffinDir, worktreesDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fault.IO{Op: "list", Path: dir, Err: err}
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return fault.IO{Op: "remove", Path: filepath.Join(dir, e.Name()), Err: err}
		}
		rep.Worktrees++
	}
	if rep.Worktrees > 0 {
		rep.add(ActDrop, "worktree bindings", fmt.Sprintf(
			"%d removed; a probe binding that pointed at a real checkout is the escape itself, and rebinding inside the probe is one `muff worktree` away", rep.Worktrees))
	}
	return nil
}

// dropOutbox deletes undelivered Mailman notifications.
//
// These are the most obviously live thing in the whole store: mail addressed to
// real agents that Macmuffin retries on its next command. Left in place, the
// first `muff` run inside a probe would try to deliver them.
func dropOutbox(s Spec, rep *Report) error {
	dir := filepath.Join(s.MacmuffinDir, outboxDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fault.IO{Op: "list", Path: dir, Err: err}
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return fault.IO{Op: "remove", Path: filepath.Join(dir, e.Name()), Err: err}
		}
		rep.Outbox++
	}
	if rep.Outbox > 0 {
		rep.add(ActDrop, "task notifications", fmt.Sprintf(
			"%d undelivered notification(s) removed; they are addressed to agents outside the probe", rep.Outbox))
	}
	return nil
}
