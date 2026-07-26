package read

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"orc/orcprobe/internal/clock"
	"orc/orcprobe/internal/fault"
)

// Macmuffin's layout, from Macmuffin/internal/store/store.go.
const (
	muffTasksDir      = "tasks"
	muffWorktreesDir  = "worktrees"
	muffOutboxDir     = "outbox"
	muffRecordFile    = "task.json"
	muffJournalFile   = "journal.jsonl"
	muffTombstoneFile = "tombstones.jsonl"
)

// Sub is one subtask.
type Sub struct {
	Name string
	Done bool
}

// Task is one task, folded from its record and journal.
type Task struct {
	Name       string
	Author     string
	Priority   int
	Difficulty int
	Created    time.Time

	Owner         string
	Collaborators []string
	Status        int
	Scope         []string
	Subs          []Sub
	Worktree      string
	Pushed        bool
	Complete      bool

	// Events is the whole journal, decoded, for `orcprobe journal`.
	Events []TaskEvent
}

// Done counts finished subtasks, which is what `pool` reports as n/m.
func (t Task) Done() (int, int) {
	done := 0
	for _, s := range t.Subs {
		if s.Done {
			done++
		}
	}
	return done, len(t.Subs)
}

// Held reports whether anyone is on this task — the question a probe exists to
// be able to answer "no" to.
func (t Task) Held() bool { return t.Owner != "" || len(t.Collaborators) > 0 }

// TaskEvent is one journal line, from Macmuffin/internal/store/journal.go.
type TaskEvent struct {
	Op     string   `json:"op"`
	By     string   `json:"by"`
	At     string   `json:"at"`
	Sub    string   `json:"sub,omitempty"`
	Agent  string   `json:"agent,omitempty"`
	Paths  []string `json:"paths,omitempty"`
	Status int      `json:"status,omitempty"`
	Path   string   `json:"path,omitempty"`
	Forced bool     `json:"forced,omitempty"`
}

// When parses the event's timestamp, falling back to the zero time so one bad
// line cannot break an ordering.
func (e TaskEvent) When() time.Time {
	at, err := clock.Parse(e.At)
	if err != nil {
		return time.Time{}
	}
	return at
}

// Tasks is a whole decoded task store.
type Tasks struct {
	Present    bool
	Tasks      []Task
	Tombstones []string
	Worktrees  int
	Outbox     int
	Damage     []Damage
}

// Held returns the tasks somebody is still on.
func (t Tasks) Held() []Task {
	var out []Task
	for _, task := range t.Tasks {
		if task.Held() {
			out = append(out, task)
		}
	}
	return out
}

// Find returns one task by name.
func (t Tasks) Find(name string) (Task, bool) {
	for _, task := range t.Tasks {
		if task.Name == name {
			return task, true
		}
	}
	return Task{}, false
}

// Macmuffin decodes a copied task store.
//
// It reports what `muff pool` deliberately hides — completed tasks, drafts
// belonging to other agents, tombstoned names — because that is the whole point
// of looking from outside.
func Macmuffin(root string) (Tasks, error) {
	var out Tasks

	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, fault.IO{Op: "look at", Path: root, Err: err}
	}
	if !info.IsDir() {
		return out, fault.Conflict{Path: root, Reason: "is not a task store"}
	}
	out.Present = true

	entries, err := os.ReadDir(filepath.Join(root, muffTasksDir))
	if err != nil && !os.IsNotExist(err) {
		return out, fault.IO{Op: "list", Path: filepath.Join(root, muffTasksDir), Err: err}
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		task, damage, err := readTask(root, e.Name())
		if err != nil {
			return out, err
		}
		out.Damage = append(out.Damage, damage...)
		out.Tasks = append(out.Tasks, task)
	}
	sort.Slice(out.Tasks, func(i, j int) bool { return out.Tasks[i].Name < out.Tasks[j].Name })

	out.Worktrees = countFiles(filepath.Join(root, muffWorktreesDir))
	out.Outbox = countFiles(filepath.Join(root, muffOutboxDir))

	tombs, damage, err := readTombstones(filepath.Join(root, muffTombstoneFile))
	if err != nil {
		return out, err
	}
	out.Tombstones = tombs
	out.Damage = append(out.Damage, damage...)
	return out, nil
}

func readTask(root, name string) (Task, []Damage, error) {
	task := Task{Name: name}
	var damage []Damage

	recordPath := filepath.Join(root, muffTasksDir, name, muffRecordFile)
	if data, err := os.ReadFile(recordPath); err == nil {
		var stored struct {
			Name       string `json:"name"`
			Author     string `json:"author"`
			Priority   int    `json:"priority"`
			Difficulty int    `json:"difficulty"`
			Created    string `json:"created"`
		}
		if err := json.Unmarshal(data, &stored); err != nil {
			damage = append(damage, Damage{Path: recordPath, Why: "task record does not parse"})
		} else {
			task.Author, task.Priority, task.Difficulty = stored.Author, stored.Priority, stored.Difficulty
			if at, err := clock.Parse(stored.Created); err == nil {
				task.Created = at
			}
		}
	} else if !os.IsNotExist(err) {
		return task, nil, fault.IO{Op: "read", Path: recordPath, Err: err}
	}

	journalPath := filepath.Join(root, muffTasksDir, name, muffJournalFile)
	lines, complete, err := readLines(journalPath)
	if err != nil {
		return task, nil, err
	}

	members := map[string]bool{}
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev TaskEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			if i == len(lines)-1 && !complete {
				break
			}
			damage = append(damage, Damage{Path: journalPath, Why: "line " + strconv.Itoa(i+1) + " does not parse"})
			continue
		}
		task.Events = append(task.Events, ev)

		switch ev.Op {
		case "claim":
			task.Owner = ev.By
			delete(members, ev.By)
		case "release":
			// Not an op Macmuffin defines today; folded anyway so that a probe
			// made by a future orcprobe still reads correctly here.
			task.Owner = ""
		case "invite":
			if ev.Agent != "" {
				members[ev.Agent] = true
			}
		case "leave":
			delete(members, ev.By)
		case "kick":
			delete(members, ev.Agent)
		case "status":
			task.Status = ev.Status
		case "scope":
			task.Scope = ev.Paths
		case "push":
			task.Pushed = true
		case "complete":
			task.Complete = true
		case "worktree":
			task.Worktree = ev.Path
		case "sub.add":
			task.Subs = append(task.Subs, Sub{Name: ev.Sub})
		case "sub.done":
			for i := range task.Subs {
				if task.Subs[i].Name == ev.Sub {
					task.Subs[i].Done = true
				}
			}
		case "sub.del":
			kept := task.Subs[:0]
			for _, s := range task.Subs {
				if s.Name != ev.Sub {
					kept = append(kept, s)
				}
			}
			task.Subs = kept
		default:
			// An op orcprobe has not heard of is noted, not fatal: Macmuffin is
			// still growing, and a view that refused to draw because of one new
			// event kind would be useless exactly when the tool changed.
			damage = append(damage, Damage{Path: journalPath, Why: "unknown op " + strconv.Quote(ev.Op)})
		}
	}

	for who := range members {
		task.Collaborators = append(task.Collaborators, who)
	}
	sort.Strings(task.Collaborators)
	return task, damage, nil
}

func readTombstones(path string) ([]string, []Damage, error) {
	lines, complete, err := readLines(path)
	if err != nil {
		return nil, nil, err
	}

	var (
		out    []string
		damage []Damage
	)
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var stored struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(line), &stored); err != nil {
			if i == len(lines)-1 && !complete {
				break
			}
			damage = append(damage, Damage{Path: path, Why: "line " + strconv.Itoa(i+1) + " does not parse"})
			continue
		}
		out = append(out, stored.Name)
	}
	return out, damage, nil
}

func countFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			n++
		}
	}
	return n
}
