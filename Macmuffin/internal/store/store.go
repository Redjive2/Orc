// Package store is Macmuffin's filesystem: the only package that knows where
// anything lives.
//
// The layout is chosen for how it fails. Several agents write here at once,
// none of them coordinate, and any of them may be killed at any moment. So a
// task's creation record is written once and never touched again, everything
// mutable is an append-only journal replayed on every command, and there is one
// lock per task rather than one for the store — contention is naturally
// per-task, because two agents race for *a* task, not for the pool.
//
// The one operation that needs real mutual exclusion is a conditional write:
// claiming a task, pushing it, completing it. Every one of those goes through
// Apply, which holds the lock across the read *and* the write, so a decision can
// never be made against state that has already moved.
package store

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/sandbox"
	"orc/macmuffin/internal/task"
)

// Layout constants.
const (
	// Version is the store format this package reads and writes. An unknown
	// version is a hard, clear error rather than a best effort: guessing at a
	// layout a newer Macmuffin wrote is how a claim gets lost.
	Version = 1

	versionFile   = "version"
	tasksDir      = "tasks"
	worktreesDir  = "worktrees"
	outboxDir     = "outbox"
	tombstoneFile = "tombstones.jsonl"

	recordFile  = "task.json"
	journalFile = "journal.jsonl"
	lockFile    = "lock"
)

// Environment variables consulted when no root is given.
const (
	EnvHome    = "MACMUFFIN_HOME"
	EnvXDGData = "XDG_DATA_HOME"
)

// Bounds. Each turns a damaged or hostile store into a clear message rather
// than an out-of-memory kill.
const (
	// MaxJournalLine bounds one event.
	MaxJournalLine = 8 << 10

	// MaxJournalSize bounds a whole task journal. At roughly 150 bytes an
	// event this is millions of events, far past any real task.
	MaxJournalSize = 64 << 20

	// MaxRecordSize bounds a creation record.
	MaxRecordSize = 64 << 10

	// MaxTasks bounds a pool. A board this long is not a board.
	MaxTasks = 4096
)

// Store is an opened task store. It is safe for concurrent use within a
// process, and safe against other processes through the per-task advisory lock.
type Store struct {
	root  string
	clock clock.Clock
	ops   ops

	// readOnly refuses every write path. It exists for the hook, which fires on
	// every tool call and must be a bystander: a hook that could write would put
	// a lock in the path of every edit, and one that could create the store
	// would conjure a task store into any directory an agent happened to be in.
	readOnly bool

	// mu serialises this process's own writers. The file lock handles other
	// processes; this handles goroutines, which the file lock would not, since
	// flock is per open file description rather than per thread.
	mu sync.Mutex
}

// Root returns the directory the store lives in.
func (s *Store) Root() string { return s.root }

// Env looks up an environment variable, reporting whether it was set.
type Env func(key string) (string, bool)

// MapEnv reads an injected environment, for tests.
func MapEnv(m map[string]string) Env {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

// DefaultRoot resolves where the store lives when no path is given.
//
// home is passed in rather than looked up so the resolution is testable without
// touching the real one.
func DefaultRoot(env Env, home string) (string, error) {
	if env == nil {
		env = os.LookupEnv
	}
	if root, ok := env(EnvHome); ok {
		if strings.TrimSpace(root) == "" {
			return "", fault.Usage{Reason: EnvHome + " is set but empty"}
		}
		return filepath.Clean(root), nil
	}
	if data, ok := env(EnvXDGData); ok && strings.TrimSpace(data) != "" {
		return filepath.Join(filepath.Clean(data), "macmuffin"), nil
	}
	if home == "" {
		return "", fault.Usage{Reason: "no home directory found; set " + EnvHome + " to say where the task store is"}
	}
	return filepath.Join(home, ".macmuffin"), nil
}

// Open opens a store, creating the layout if it is not there yet.
//
// Creating on open is deliberate: an agent's first command should work rather
// than fail with an instruction to run a setup step it has no way to know about.
func Open(root string, c clock.Clock) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fault.Usage{Reason: "empty store path"}
	}
	if c == nil {
		return nil, fault.Internal{Where: "store.Open", Detail: "no clock was given"}
	}

	// Before anything is created. Opening a store creates its layout, so a
	// process inside an Orcprobe probe that resolved a real path — by an
	// absolute path, a restored MACMUFFIN_HOME, or a hardcoded location — would
	// otherwise have written to the real world before this check could matter.
	// Outside a probe this does nothing at all.
	if err := sandbox.Guard(sandbox.OSEnv, root); err != nil {
		return nil, err
	}

	s := &Store{root: filepath.Clean(root), clock: c, ops: realOps()}
	if err := s.init(); err != nil {
		return nil, err
	}
	return s, nil
}

// Read opens an existing store without touching it.
//
// Unlike Open it creates nothing — no directories, no version file — and every
// write path refuses. A store that is not there is reported as not there rather
// than brought into being, because the caller that wants this is asking a
// question about a store somebody else made.
func Read(root string, c clock.Clock) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fault.Usage{Reason: "empty store path"}
	}
	if c == nil {
		return nil, fault.Internal{Where: "store.Read", Detail: "no clock was given"}
	}

	// Guarded even though it writes nothing. The hook opens the store this way
	// to decide whether an edit is in scope, and answering that question from
	// the *real* pool while inside a probe would let real state govern what
	// happens in a sandbox — a quieter failure than writing, and still one.
	if err := sandbox.Guard(sandbox.OSEnv, root); err != nil {
		return nil, err
	}

	s := &Store{root: filepath.Clean(root), clock: c, ops: realOps(), readOnly: true}
	if _, err := s.ops.readFile(filepath.Join(s.root, versionFile)); err != nil {
		if os.IsNotExist(err) {
			return nil, fault.NotFound{Target: "a task store at " + s.root}
		}
		return nil, fault.IO{Op: "read", Path: filepath.Join(s.root, versionFile), Err: err}
	}
	if err := s.checkVersion(); err != nil {
		return nil, err
	}
	return s, nil
}

// refuseWrite is the guard on every path that would change the store.
func (s *Store) refuseWrite() error {
	if !s.readOnly {
		return nil
	}
	return fault.Denied{Actor: "this process", Action: "write", Target: s.root,
		Reason: "the store was opened read-only"}
}

func (s *Store) init() error {
	for _, dir := range []string{"", tasksDir, worktreesDir, outboxDir} {
		path := filepath.Join(s.root, dir)
		if err := s.ops.mkdirAll(path, dirMode); err != nil {
			return fault.IO{Op: "create", Path: path, Err: err}
		}
	}
	return s.checkVersion()
}

// checkVersion reads the version marker, writing it if the store is new.
//
// A store whose version this build does not understand is refused outright.
// Reading what it recognises and ignoring the rest would silently drop whatever
// the newer format added, which for a task store could be an owner.
func (s *Store) checkVersion() error {
	path := filepath.Join(s.root, versionFile)

	data, err := s.ops.readFile(path)
	if os.IsNotExist(err) {
		return s.writeFile(path, []byte(itoa(Version)+"\n"))
	}
	if err != nil {
		return fault.IO{Op: "read", Path: path, Err: err}
	}

	text := strings.TrimSpace(string(data))
	got, convErr := atoi(text)
	if convErr != nil {
		return fault.Parse{Path: path, Reason: "store version is " + quote(text) + ", which is not a number"}
	}
	if got != Version {
		return fault.Parse{Path: path, Reason: versionMismatch(got)}
	}
	return nil
}

func versionMismatch(got int) string {
	if got > Version {
		return "store is format version " + itoa(got) + ", but this macmuffin understands version " +
			itoa(Version) + "; upgrade macmuffin rather than letting it guess"
	}
	return "store is format version " + itoa(got) + ", which this macmuffin (version " +
		itoa(Version) + ") can no longer read"
}

// Paths. A task.Name is already validated — that is what the type means — so
// none of these can escape the store.

func (s *Store) taskDir(name task.Name) string {
	return filepath.Join(s.root, tasksDir, name.String())
}

func (s *Store) recordPath(name task.Name) string {
	return filepath.Join(s.taskDir(name), recordFile)
}

func (s *Store) journalPath(name task.Name) string {
	return filepath.Join(s.taskDir(name), journalFile)
}

func (s *Store) lockPath(name task.Name) string {
	return filepath.Join(s.taskDir(name), lockFile)
}

// withLock runs fn holding both the process mutex and the task's file lock.
//
// The lock is per task, so two agents working on different tasks never wait for
// each other. Readers deliberately do not take it: a torn journal tail is
// already handled by replay, and blocking every `pool` behind every `claim` is
// how a tracker becomes the thing agents route around.
func (s *Store) withLock(name task.Name, fn func() error) error {
	if name.Zero() {
		return fault.Internal{Where: "store.withLock", Detail: "no task named"}
	}
	if err := s.refuseWrite(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ops.mkdirAll(s.taskDir(name), dirMode); err != nil {
		return fault.IO{Op: "create", Path: s.taskDir(name), Err: err}
	}

	path := s.lockPath(name)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, fileMode)
	if err != nil {
		return fault.IO{Op: "open the lock at", Path: path, Err: err}
	}
	defer func() { _ = f.Close() }()

	if err := lockFileHandle(f); err != nil {
		return fault.IO{Op: "lock", Path: path, Err: err}
	}
	defer func() { _ = unlockFileHandle(f) }()

	return fn()
}

// Names lists every task in the store, in name order.
func (s *Store) Names() ([]task.Name, error) {
	dir := filepath.Join(s.root, tasksDir)

	entries, err := s.ops.readDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fault.IO{Op: "list", Path: dir, Err: err}
	}
	if len(entries) > MaxTasks {
		return nil, fault.Parse{Path: dir, Reason: "store holds more than " + itoa(MaxTasks) + " tasks"}
	}

	var out []task.Name
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name, err := task.ParseName(e.Name())
		if err != nil {
			// A directory that is not a valid task name was not put there by
			// Macmuffin. Skipping it is right; `verify` is what reports it.
			continue
		}
		// Only a directory with a creation record is a task; one without is a
		// lock directory left by an interrupted create.
		if ok, err := s.Has(name); err != nil {
			return nil, err
		} else if ok {
			out = append(out, name)
		}
	}
	// ReadDir sorts, and every name that survives ParseName sorts the same way,
	// so the result is deterministic without a second sort.
	return out, nil
}

// Has reports whether a task exists.
func (s *Store) Has(name task.Name) (bool, error) {
	if name.Zero() {
		return false, fault.Internal{Where: "store.Has", Detail: "no task named"}
	}
	_, err := s.ops.stat(s.recordPath(name))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fault.IO{Op: "check for", Path: s.recordPath(name), Err: err}
}

// Small local helpers, so this package does not import strconv for two
// conversions and so their failure modes are the ones it wants.

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func atoi(s string) (int, error) {
	if s == "" {
		return 0, fault.Parse{Reason: "empty number"}
	}
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, fault.Parse{Reason: "not a number"}
		}
		n = n*10 + int(c-'0')
		if n > 1<<40 {
			return 0, fault.Parse{Reason: "number is too large"}
		}
	}
	return n, nil
}

func quote(s string) string { return `"` + s + `"` }
