// Package watch is what a long-running loop needs in order to survive an upgrade.
//
// Three processes in this tree run until somebody stops them: `cq sync --watch`,
// `orc wake --every`, and `orc tend --watch`. They are the fleet's heartbeat — the
// mirror stays current because one is running, and agents keep moving because
// another is. None of them is supervised, because none of them serves a port;
// they are loops in a terminal or under a launchd job.
//
// Which makes an upgrade a problem they alone have. Replacing a binary on unix
// leaves the running process on its old inode, so a watcher started before an
// upgrade keeps running the build it started with — for ever, silently, doing its
// job correctly with code nobody has any more. `cq upgrade` reports success, every
// tool on disk is new, and the three processes that actually keep the fleet alive
// are the three still running the old one. That is the failure this package
// exists for, and it is invisible: nothing is broken, nothing is logged, and the
// only symptom is that a fix somebody shipped never takes effect.
//
// So a watcher does two things it could not do alone:
//
//   - It **says it is running**, in a file, so that an upgrade can ask whether
//     anything is watching and start one when nothing is.
//   - It **notices its own binary changed** and re-execs into the new one, keeping
//     its pid, its arguments, and its place in the cycle.
//
// The second is the same trick `cq serve`'s supervisor already uses at the end of
// a restart, for the same reason: exec replaces the process image in place, so
// whatever is watching *this* process — a shell, launchd, a terminal — sees
// nothing happen at all.
//
// What this package does not do is decide when. A watcher checks between rounds,
// never during one, because a round that is half-applied when the process image
// changes underneath it is the one outcome worse than running an old build.
package watch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"orc/common/fault"
)

// Kind is what a watcher is for. It is the question an upgrade asks — "is
// anything syncing this machine?" — so it names the job rather than the command.
type Kind string

const (
	// Sync is `cq sync --watch`: the mirror this machine pushes to the website.
	Sync Kind = "sync"
	// Wake is `orc wake --every`: the sweep that pokes agents that have gone quiet.
	Wake Kind = "wake"
	// Tend is `orc tend --watch`: the sweep that restarts sessions that have died.
	Tend Kind = "tend"
)

// Record is one watcher, while it runs.
//
// It is written whole, replaced whole, and removed when the process it describes
// exits — the same shape as Orc's session.json, and for the same reason: having a
// watcher is a fact about a process, not a decision worth keeping history of. A
// crash must be able to take one away without anything having to be rewritten.
//
// Which means **the file's existence is a claim, not a fact**. A watcher killed
// with SIGKILL leaves one behind, so every read checks the process is really
// there and nothing above here treats the file as evidence on its own.
type Record struct {
	Kind Kind `json:"kind"`
	Pid  int  `json:"pid"`
	// Exe and Args are what it would take to start this watcher again. They are
	// recorded rather than reconstructed because the flags a watcher runs with are
	// not derivable from its kind: an operator who chose `--watch 30s` chose it.
	Exe  string   `json:"exe"`
	Args []string `json:"args"`
	// Period is how often the loop comes round, for a status line to read.
	Period  Duration  `json:"period"`
	Started time.Time `json:"started"`
	// Expires is when this watcher will stop on its own, absent when it runs until
	// stopped. A watcher nobody started by hand needs one — see Until.
	Expires *time.Time `json:"expires,omitempty"`
}

// Duration is a time.Duration that survives a round trip through JSON as
// something a person can read.
//
// encoding/json renders time.Duration as a count of nanoseconds, so a five-minute
// watcher records itself as 300000000000 — correct, and unreadable in a file whose
// whole purpose is that an operator can open it and see what is running.
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fault.Parse{Path: "watch record", Reason: err.Error()}
	}
	*d = Duration(v)
	return nil
}

// Registry is a directory of watcher records.
//
// One directory per tool's state — `$CQ_HOME/watchers`, `$ORC_HOME/watchers` — so
// that a watcher is recorded beside the store it acts on, and a sandboxed run
// records into the sandbox without anything having to know it is sandboxed.
type Registry struct{ Dir string }

// Register writes this process's record and returns the way to remove it.
//
// The returned function is safe to call more than once and never returns an
// error: it runs in a defer, on a path where the caller is already leaving, and
// there is nothing useful a loop shutting down can do about a file it could not
// unlink. A record left behind is handled by every reader anyway, because a
// reader cannot trust one to begin with.
func (r Registry) Register(rec Record) (func(), error) {
	if r.Dir == "" {
		return nil, fault.Internal{Where: "watch.Register", Detail: "no registry directory"}
	}
	if rec.Pid == 0 {
		rec.Pid = os.Getpid()
	}
	if rec.Started.IsZero() {
		rec.Started = time.Now()
	}
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return nil, fault.IO{Op: "create", Path: r.Dir, Err: err}
	}

	path := r.path(rec.Kind, rec.Pid)
	body, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return nil, fault.Internal{Where: "watch.Register", Detail: err.Error()}
	}
	if err := write(path, append(body, '\n')); err != nil {
		return nil, err
	}
	return func() { _ = os.Remove(path) }, nil
}

// Live lists the watchers that are really running, and clears away the ones that
// are not.
//
// Reaping here rather than in a separate sweep is deliberate: the only moment
// anybody cares whether a record is stale is the moment they read it, and a
// registry that tidied itself on a timer would need a process running to do it —
// which is the thing being looked for.
func (r Registry) Live() ([]Record, error) {
	entries, err := os.ReadDir(r.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			// No directory is no watchers, which is an answer and not a failure.
			// A machine that has never run one is the ordinary case.
			return nil, nil
		}
		return nil, fault.IO{Op: "read", Path: r.Dir, Err: err}
	}

	var out []Record
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(r.Dir, entry.Name())
		body, err := os.ReadFile(path)
		if err != nil {
			// A record that cannot be read cannot be trusted, and cannot be
			// reported as a watcher. Removing it is the same treatment a stale one
			// gets, and for the same reason: this file is a claim.
			_ = os.Remove(path)
			continue
		}
		var rec Record
		if err := json.Unmarshal(body, &rec); err != nil {
			_ = os.Remove(path)
			continue
		}
		if !Alive(rec.Pid) {
			_ = os.Remove(path)
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Pid < out[j].Pid
	})
	return out, nil
}

// Running reports whether any live watcher of this kind exists.
func (r Registry) Running(kind Kind) (bool, error) {
	live, err := r.Live()
	if err != nil {
		return false, err
	}
	for _, rec := range live {
		if rec.Kind == kind {
			return true, nil
		}
	}
	return false, nil
}

func (r Registry) path(kind Kind, pid int) string {
	return filepath.Join(r.Dir, string(kind)+"-"+strconv.Itoa(pid)+".json")
}

// write replaces a file atomically: a reader must see the whole record or no
// record, never half of one written while it looked.
func write(path string, body []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fault.IO{Op: "create", Path: tmp, Err: err}
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fault.IO{Op: "write", Path: tmp, Err: err}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fault.IO{Op: "sync", Path: tmp, Err: err}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fault.IO{Op: "close", Path: tmp, Err: err}
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fault.IO{Op: "replace", Path: path, Err: err}
	}
	return nil
}
