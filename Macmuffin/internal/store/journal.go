package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/user"
	"orc/macmuffin/internal/task"
)

// record is the on-disk creation record: everything about a task that is fixed
// the moment it exists. It is written once and never rewritten, so the rename
// that creates it is what makes a task name unique.
type record struct {
	Version    int    `json:"version"`
	Name       string `json:"name"`
	Author     string `json:"author"`
	Priority   int    `json:"priority"`
	Difficulty int    `json:"difficulty"`
	Created    string `json:"created"`
}

// event is the on-disk shape of one journal line.
//
// It mirrors task.Event, which owns what an event *means*; this owns only how
// it is stored. Keeping them apart is what lets the fold's rules be tested
// without a filesystem and the codec's rules be fuzzed without a task.
type event struct {
	Op      string   `json:"op"`
	By      string   `json:"by"`
	At      string   `json:"at"`
	Sub     string   `json:"sub,omitempty"`
	Agent   string   `json:"agent,omitempty"`
	Paths   []string `json:"paths,omitempty"`
	Status  int      `json:"status,omitempty"`
	Path    string   `json:"path,omitempty"`
	Forced  bool     `json:"forced,omitempty"`
	Skipped []string `json:"skipped,omitempty"`
	Until   []string `json:"until,omitempty"`
}

// encodeEvent renders an event for storage.
func encodeEvent(e task.Event) ([]byte, error) {
	stored := event{
		Op:     string(e.Op()),
		By:     e.By().String(),
		At:     clock.Format(e.At()),
		Paths:  e.Paths(),
		Path:   e.Path(),
		Forced: e.Forced(),
	}
	if sub := e.Subtask(); !sub.Zero() {
		stored.Sub = sub.String()
	}
	if agent := e.Agent(); !agent.Zero() {
		stored.Agent = agent.String()
	}
	if s := e.Status(); s != task.StatusUnset {
		stored.Status = s.Int()
	}
	for _, s := range e.Skipped() {
		stored.Skipped = append(stored.Skipped, s.String())
	}
	for _, n := range e.Until() {
		stored.Until = append(stored.Until, n.String())
	}

	line, err := json.Marshal(stored)
	if err != nil {
		return nil, fault.Internal{Where: "store.encodeEvent", Detail: err.Error()}
	}
	if bytes.ContainsAny(line, "\n\r") {
		return nil, fault.Internal{Where: "store.encodeEvent", Detail: "encoded event contains a newline"}
	}
	// Decoded back before it is written: an event that cannot be read is an
	// event that has been lost, and it is cheaper to find that out here.
	if _, err := decodeEvent("<new event>", 1, line); err != nil {
		return nil, fault.Internal{Where: "store.encodeEvent", Detail: "event does not decode back: " + err.Error()}
	}
	return line, nil
}

// decodeEvent reads one journal line.
func decodeEvent(path string, line int, raw []byte) (task.Event, error) {
	bad := func(format string, args ...any) (task.Event, error) {
		return task.Event{}, fault.Parse{Path: path, Line: line, Reason: fmt.Sprintf(format, args...)}
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	var stored event
	if err := dec.Decode(&stored); err != nil {
		return bad("journal event: %s", err)
	}
	if dec.More() {
		return bad("journal line has trailing content")
	}

	by, err := user.Parse(stored.By)
	if err != nil {
		return bad("journal event names a bad actor: %s", err)
	}
	at, err := clock.Parse(stored.At)
	if err != nil {
		return bad("journal event has a bad timestamp: %s", err)
	}

	op := task.Op(stored.Op)
	if !op.Valid() {
		return bad("unknown journal operation %q", stored.Op)
	}

	// Each operation is rebuilt through its own constructor, so the shape rules
	// in task.Event apply to stored bytes exactly as they do to fresh events —
	// a hand-edited journal cannot introduce a shape the code never produces.
	switch op {
	case task.OpScope:
		return wrap(task.Scope(by, at, stored.Paths))
	case task.OpPush:
		return wrap(task.Push(by, at))
	case task.OpClaim:
		return wrap(task.Claim(by, at))
	case task.OpStatus:
		return wrap(task.SetStatus(by, at, task.Status(stored.Status)))
	case task.OpBlock, task.OpUnblock:
		var until []task.Name
		for _, raw := range stored.Until {
			name, err := task.ParseName(raw)
			if err != nil {
				return bad("journal event names a bad prerequisite: %s", err)
			}
			until = append(until, name)
		}
		if op == task.OpBlock {
			return wrap(task.Block(by, at, until))
		}
		return wrap(task.Unblock(by, at, until))
	case task.OpInvite, task.OpKick, task.OpAssign:
		agent, err := user.Parse(stored.Agent)
		if err != nil {
			return bad("journal event names a bad agent: %s", err)
		}
		switch op {
		case task.OpInvite:
			return wrap(task.Invite(by, at, agent))
		case task.OpKick:
			return wrap(task.Kick(by, at, agent))
		default:
			return wrap(task.Assign(by, at, agent))
		}
	case task.OpLeave:
		return wrap(task.Leave(by, at))
	case task.OpSubAdd, task.OpSubDone, task.OpSubDelete:
		sub, err := task.ParseName(stored.Sub)
		if err != nil {
			return bad("journal event names a bad subtask: %s", err)
		}
		switch op {
		case task.OpSubAdd:
			return wrap(task.AddSub(by, at, sub))
		case task.OpSubDone:
			return wrap(task.DoneSub(by, at, sub))
		default:
			return wrap(task.DeleteSub(by, at, sub))
		}
	case task.OpComplete:
		skipped := make([]task.Name, 0, len(stored.Skipped))
		for _, s := range stored.Skipped {
			n, err := task.ParseName(s)
			if err != nil {
				return bad("journal event names a bad skipped subtask: %s", err)
			}
			skipped = append(skipped, n)
		}
		return wrap(task.Complete(by, at, stored.Forced, skipped))
	case task.OpWorktree:
		if stored.Path == "" {
			return bad("worktree event has no path")
		}
		return wrap(task.BindWorktree(by, at, stored.Path))
	case task.OpDescribe:
		return wrap(task.Describe(by, at))
	case task.OpUndescribe:
		return wrap(task.Undescribe(by, at))
	default:
		return bad("unhandled journal operation %q", stored.Op)
	}
}

// wrap turns a constructor's internal fault into a parse fault. Bad bytes on
// disk are not a defect in Macmuffin, and the exit code should say so.
func wrap(e task.Event, err error) (task.Event, error) {
	if err != nil {
		return task.Event{}, fault.Parse{Reason: "journal event is not well formed: " + err.Error()}
	}
	return e, nil
}

// Load reads a task: its creation record, then its journal folded onto it.
// Inspect loads a task and reports how many bytes at the end of its journal an
// interrupted append left behind.
//
// Load throws that count away, because every command but one is right not to
// care: the fold already recovered. `verify` is the one that cares, since a
// store accumulating interrupted appends is a store something keeps killing.
func (s *Store) Inspect(name task.Name) (task.Task, int, error) {
	if name.Zero() {
		return task.Task{}, 0, fault.Internal{Where: "store.Inspect", Detail: "no task named"}
	}

	base, err := s.loadRecord(name)
	if err != nil {
		return task.Task{}, 0, err
	}

	data, err := s.ops.readFile(s.journalPath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return base, 0, nil
		}
		return task.Task{}, 0, fault.IO{Op: "read", Path: s.journalPath(name), Err: err}
	}
	return Fold(s.journalPath(name), base, data)
}

func (s *Store) Load(name task.Name) (task.Task, error) {
	if name.Zero() {
		return task.Task{}, fault.Internal{Where: "store.Load", Detail: "no task named"}
	}

	base, err := s.loadRecord(name)
	if err != nil {
		return task.Task{}, err
	}

	data, err := s.ops.readFile(s.journalPath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return base, nil
		}
		return task.Task{}, fault.IO{Op: "read", Path: s.journalPath(name), Err: err}
	}
	got, _, err := Fold(s.journalPath(name), base, data)
	return got, err
}

// loadRecord reads the immutable half of a task.
func (s *Store) loadRecord(name task.Name) (task.Task, error) {
	path := s.recordPath(name)

	data, err := s.ops.readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return task.Task{}, fault.NotFound{Target: name.String()}
		}
		return task.Task{}, fault.IO{Op: "read", Path: path, Err: err}
	}
	if len(data) > MaxRecordSize {
		return task.Task{}, fault.Parse{Path: path, Reason: fmt.Sprintf(
			"creation record is %d bytes, limit is %d", len(data), MaxRecordSize)}
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var r record
	if err := dec.Decode(&r); err != nil {
		return task.Task{}, fault.Parse{Path: path, Reason: "creation record: " + err.Error()}
	}
	if dec.More() {
		return task.Task{}, fault.Parse{Path: path, Reason: "creation record has trailing content"}
	}
	if r.Version != Version {
		return task.Task{}, fault.Parse{Path: path, Reason: fmt.Sprintf(
			"creation record is version %d, this macmuffin writes version %d", r.Version, Version)}
	}

	stored, err := task.ParseName(r.Name)
	if err != nil {
		return task.Task{}, fault.Parse{Path: path, Reason: "creation record name: " + err.Error()}
	}
	// The directory states an identity and so does the content. A disagreement
	// means the store was hand-edited or a directory was copied, and either way
	// the content must not answer for a name it is not.
	if !stored.Equal(name) {
		return task.Task{}, fault.Conflict{Path: path, Reason: fmt.Sprintf(
			"directory is named for %s but the record says %s", name, stored)}
	}

	author, err := user.Parse(r.Author)
	if err != nil {
		return task.Task{}, fault.Parse{Path: path, Reason: "creation record author: " + err.Error()}
	}
	priority, err := task.NewPriority(r.Priority)
	if err != nil {
		return task.Task{}, fault.Parse{Path: path, Reason: "creation record priority: " + err.Error()}
	}
	difficulty, err := task.NewDifficulty(r.Difficulty)
	if err != nil {
		return task.Task{}, fault.Parse{Path: path, Reason: "creation record difficulty: " + err.Error()}
	}
	created, err := clock.Parse(r.Created)
	if err != nil {
		return task.Task{}, fault.Parse{Path: path, Reason: "creation record created: " + err.Error()}
	}

	got, err := task.NewDraft(stored, author, priority, difficulty, created)
	if err != nil {
		return task.Task{}, fault.Parse{Path: path, Reason: "creation record is invalid: " + err.Error()}
	}
	return got, nil
}

// Fold replays a journal onto a task, and reports how many trailing bytes were
// dropped as an interrupted append.
//
// The recovery rule is the reason for the append-only design, and it is
// Mailman's: a process killed mid-append can only damage the *last* line, so an
// unparseable final line is dropped with a count. An unparseable line anywhere
// else is corruption rather than interruption and is a hard error — silently
// skipping it would silently drop a claim, and a dropped claim is two agents
// editing the same files.
//
// It is a pure function of its input so the rules can be fuzzed without a
// filesystem.
func Fold(path string, base task.Task, data []byte) (task.Task, int, error) {
	if len(data) > MaxJournalSize {
		return task.Task{}, 0, fault.Parse{Path: path, Reason: fmt.Sprintf(
			"journal is %d bytes, limit is %d", len(data), MaxJournalSize)}
	}

	out := base
	skipped := 0

	// A trailing newline means the file ends on a complete line. Without one,
	// the final fragment is what an interrupted append left behind.
	complete := len(data) == 0 || data[len(data)-1] == '\n'
	lines := bytes.Split(data, []byte("\n"))
	if complete && len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}

	for i, raw := range lines {
		lineNo := i + 1
		last := i == len(lines)-1

		if len(raw) == 0 {
			if last && !complete {
				continue
			}
			return task.Task{}, 0, fault.Parse{Path: path, Line: lineNo, Reason: "empty journal line"}
		}
		if len(raw) > MaxJournalLine {
			return task.Task{}, 0, fault.Parse{Path: path, Line: lineNo, Reason: fmt.Sprintf(
				"journal line is %d bytes, limit is %d", len(raw), MaxJournalLine)}
		}

		ev, err := decodeEvent(path, lineNo, raw)
		if err != nil {
			if last && !complete {
				skipped = len(raw)
				break
			}
			return task.Task{}, 0, err
		}

		next, err := out.With(ev)
		if err != nil {
			// A journal that folds to an illegal state is corruption, not an
			// interrupted write — the events were legal when they were
			// appended, so something has rewritten them.
			return task.Task{}, 0, fault.Parse{Path: path, Line: lineNo,
				Reason: fmt.Sprintf("journal event %q cannot apply: %s", ev.Op(), err)}
		}
		out = next
	}
	return out, skipped, nil
}

// Create writes a task's creation record.
//
// The record is written with O_EXCL, so the filesystem decides the race between
// two agents creating the same name rather than a read-then-write that another
// process can interleave. The loser gets a conflict naming the author.
func (s *Store) Create(name task.Name, author user.Name, priority, difficulty task.Score) (task.Task, error) {
	if name.Zero() || author.Zero() {
		return task.Task{}, fault.Internal{Where: "store.Create", Detail: "task and author are both required"}
	}

	created := s.clock.Now()
	fresh, err := task.NewDraft(name, author, priority, difficulty, created)
	if err != nil {
		return task.Task{}, err
	}

	data, err := json.MarshalIndent(record{
		Version:    Version,
		Name:       name.String(),
		Author:     author.String(),
		Priority:   priority.Value(),
		Difficulty: difficulty.Value(),
		Created:    clock.Format(created),
	}, "", "  ")
	if err != nil {
		return task.Task{}, fault.Internal{Where: "store.Create", Detail: err.Error()}
	}

	err = s.withLock(name, func() error {
		if existing, err := s.loadRecord(name); err == nil {
			return fault.Conflict{Path: name.String(), Reason: fmt.Sprintf(
				"a task called %s already exists, created by %s", name, existing.Author())}
		} else if !isNotFound(err) {
			return err
		}
		return s.writeNew(s.recordPath(name), append(data, '\n'))
	})
	if err != nil {
		return task.Task{}, err
	}
	return fresh, nil
}

// Decide is a caller's conditional write: it is handed the task as it stands
// under the lock, and returns the event to append — or an error to abort with.
type Decide func(task.Task) (task.Event, error)

// Apply is the only way to change a task.
//
// The lock is taken, the journal is replayed, the caller decides against *that*
// state, and the event is appended before the lock is released. The check and
// the write are never separated, which is what makes the claim race resolve
// rather than interleave. A command that wanted to read first and write later
// cannot: the API does not offer it.
//
// The decided event is folded before it is written, so an event that would
// leave the task in an illegal state is refused rather than journaled — the
// journal only ever contains transitions that happened.
func (s *Store) Apply(name task.Name, decide Decide) (task.Task, error) {
	if name.Zero() {
		return task.Task{}, fault.Internal{Where: "store.Apply", Detail: "no task named"}
	}
	if decide == nil {
		return task.Task{}, fault.Internal{Where: "store.Apply", Detail: "no decision given"}
	}

	var out task.Task
	err := s.withLock(name, func() error {
		current, err := s.Load(name)
		if err != nil {
			return err
		}

		ev, err := decide(current)
		if err != nil {
			return err
		}
		if ev.Zero() {
			// A decision that produced no event is a no-op the caller has
			// already reported on — `claim` on a task you own, say.
			out = current
			return nil
		}

		// Ordering, asked here so no command can omit it. Both questions need
		// the store and neither belongs to the task: whether its prerequisites
		// are finished, and whether a new one would close a ring.
		if err := s.holds(current, ev.Op()); err != nil {
			return err
		}
		if err := s.wouldCycle(current, ev); err != nil {
			return err
		}

		next, err := current.With(ev)
		if err != nil {
			return err
		}

		line, err := encodeEvent(ev)
		if err != nil {
			return err
		}
		if err := s.appendLine(s.journalPath(name), line); err != nil {
			return err
		}
		out = next
		return nil
	})
	if err != nil {
		return task.Task{}, err
	}
	return out, nil
}

// Now returns the store's clock, so a caller building an event stamps it with
// the same time source the store does.
func (s *Store) Now() time.Time { return s.clock.Now() }

// isNotFound reports whether an error is a missing-thing fault.
func isNotFound(err error) bool {
	var nf fault.NotFound
	return asNotFound(err, &nf)
}

func asNotFound(err error, out *fault.NotFound) bool {
	if e, ok := err.(fault.NotFound); ok {
		*out = e
		return true
	}
	return false
}
