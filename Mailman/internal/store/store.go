// Package store is Mailman's filesystem: the only package that knows where
// anything lives.
//
// The layout is chosen for how it fails rather than for how it reads. Several
// agent processes write here at once, none of them coordinate, and any of them
// may be killed at any moment. So: messages are write-once and never edited,
// per-user mutable state is an append-only journal replayed on every command,
// and read receipts are one file per reader so two recipients never contend.
//
// Every one of those choices trades a little space or a little speed for a
// failure mode that is recoverable. A half-written journal loses its last line;
// a half-written message never becomes visible at all.
package store

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/sandbox"
	"orc/common/user"
)

// Layout constants.
const (
	// Version is the store format this package reads and writes. An unknown
	// version is a hard, clear error rather than a best effort: guessing at a
	// layout a newer Mailman wrote is how mail gets lost.
	Version = 1

	versionFile  = "version"
	usersDir     = "users"
	messagesDir  = "messages"
	convosDir    = "convos"
	lockFileName = "lock"

	userFile    = "user.json"
	journalFile = "journal.jsonl"

	messageExt = ".msg"
	receiptExt = ".rcpt"
	convoExt   = ".jsonl"
)

// Environment variables consulted when no root is given.
const (
	EnvHome    = "MAILMAN_HOME"
	EnvXDGData = "XDG_DATA_HOME"
)

// Bounds. Each turns a damaged or hostile store into a clear message rather
// than an out-of-memory kill.
const (
	// MaxJournalLine bounds one journal event.
	MaxJournalLine = 4 << 10

	// MaxJournalSize bounds a whole journal. At roughly 150 bytes an event this
	// is millions of events, far past any real mailbox.
	MaxJournalSize = 256 << 20

	// MaxMessageSize bounds a stored message file: the body limit plus room for
	// a header.
	MaxMessageSize = (16 << 20) + (64 << 10)

	// MaxUsers bounds a store's user list.
	MaxUsers = 4096
)

// Store is an opened mail store. It is safe for concurrent use within a
// process, and safe against other processes through the advisory lock.
type Store struct {
	root  string
	clock clock.Clock
	ops   ops

	// mu serialises this process's own writers. The file lock handles other
	// processes; this handles goroutines, which the file lock would not, since
	// flock is per open file description and not per thread.
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
// The order is the usual one: an explicit override, then the XDG data
// directory, then a dot-directory in the home. home is passed in rather than
// looked up so the resolution is testable without touching the real one.
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
		return filepath.Join(filepath.Clean(data), "mailman"), nil
	}
	if home == "" {
		return "", fault.Usage{Reason: "no home directory found; set " + EnvHome + " to say where the mail store is"}
	}
	return filepath.Join(home, ".mailman"), nil
}

// Open opens a store, creating the layout if it is not there yet.
//
// Creating on open is deliberate: an agent's first command should work rather
// than fail with an instruction to run some setup step it has no way to know
// about. What is *not* created is a user — that is Orc's job, and an
// auto-created mailbox would be an account nobody authorised.
func Open(root string, c clock.Clock) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fault.Usage{Reason: "empty store path"}
	}
	if c == nil {
		return nil, fault.Internal{Where: "store.Open", Detail: "no clock was given"}
	}

	// Before anything is created. Opening a store creates its layout, so a
	// process inside an Orcprobe probe that resolved a real path — by an
	// absolute path, a restored MAILMAN_HOME, or a hardcoded location — would
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

// init creates the layout and checks the version.
func (s *Store) init() error {
	for _, dir := range []string{"", usersDir, messagesDir, convosDir} {
		path := filepath.Join(s.root, dir)
		if err := s.ops.mkdirAll(path, dirMode); err != nil {
			return fault.IO{Op: "create", Path: path, Err: err}
		}
	}
	return s.checkVersion()
}

// checkVersion reads the version marker, writing it if the store is new.
//
// A store whose version this build does not understand is refused outright. The
// alternative — reading what it recognises and ignoring the rest — silently
// drops whatever the newer format added, which for a mail store could be the
// recipients of a message.
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
		return "store is format version " + itoa(got) + ", but this mailman understands version " +
			itoa(Version) + "; upgrade mailman rather than letting it guess"
	}
	return "store is format version " + itoa(got) + ", which this mailman (version " +
		itoa(Version) + ") can no longer read"
}

// withLock runs fn holding both the process mutex and the cross-process file
// lock. Every mutating operation goes through it.
//
// Readers deliberately do not take the lock. A torn journal tail is already
// handled by replay, and blocking every `inbox` behind every `send` is how a
// mail tool becomes the thing agents route around.
func (s *Store) withLock(fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.root, lockFileName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, fileMode)
	if err != nil {
		return fault.IO{Op: "open the lock at", Path: path, Err: err}
	}
	defer func() { _ = f.Close() }()

	if err := lockFile(f); err != nil {
		return fault.IO{Op: "lock", Path: path, Err: err}
	}
	defer func() { _ = unlockFile(f) }()

	return fn()
}

// userDir returns a user's directory. The name is already validated — that is
// what a user.Name means — so this cannot escape the store.
func (s *Store) userDir(name user.Name) string {
	return filepath.Join(s.root, usersDir, name.String())
}

func (s *Store) userPath(name user.Name) string {
	return filepath.Join(s.userDir(name), userFile)
}

func (s *Store) journalPath(name user.Name) string {
	return filepath.Join(s.userDir(name), journalFile)
}

// Authenticate verifies a key against a user's stored record.
//
// This runs on every command. It fails closed on every path: a missing user, a
// damaged record, an unreadable file, and a wrong key all produce the same
// visible message, with the real cause carried in the fault's Detail for logs
// and tests.
func (s *Store) Authenticate(name user.Name, key string) error {
	if name.Zero() {
		return fault.Auth{Reason: "authentication failed", Detail: "no user given"}
	}

	rec, err := s.userRecord(name)
	if err != nil {
		return fault.Auth{Reason: "authentication failed", Detail: err.Error()}
	}
	// The record names itself; if that disagrees with the directory it was found
	// in, the store has been tampered with or hand-edited.
	if rec.Name().String() != name.String() {
		return fault.Auth{
			Reason: "authentication failed",
			Detail: "record in " + name.String() + "'s directory names " + rec.Name().String(),
		}
	}
	return rec.Verify(key)
}

// userRecord loads a user's stored identity.
func (s *Store) userRecord(name user.Name) (user.Record, error) {
	path := s.userPath(name)
	data, err := s.readFile(path)
	if err != nil {
		return user.Record{}, err
	}
	return user.Decode(path, data)
}

// HasUser reports whether a mailbox exists.
//
// It is used to refuse a send to a non-existent recipient before anything is
// written, so mail is never addressed into a void. It says nothing about
// whether the caller may read that mailbox.
func (s *Store) HasUser(name user.Name) (bool, error) {
	if name.Zero() {
		return false, fault.Internal{Where: "store.HasUser", Detail: "no user given"}
	}
	_, err := s.ops.stat(s.userPath(name))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fault.IO{Op: "check for", Path: s.userPath(name), Err: err}
}

// Users lists every mailbox, in name order.
func (s *Store) Users() ([]user.Name, error) {
	dir := filepath.Join(s.root, usersDir)
	entries, err := s.ops.readDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fault.IO{Op: "list", Path: dir, Err: err}
	}
	if len(entries) > MaxUsers {
		return nil, fault.Parse{Path: dir, Reason: "store holds more than " + itoa(MaxUsers) + " users"}
	}

	var out []user.Name
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name, err := user.Parse(e.Name())
		if err != nil {
			// A directory that is not a valid user name was not put there by
			// Mailman. Skipping it is right; doing so silently is not.
			continue
		}
		ok, err := s.HasUser(name)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, name)
		}
	}
	// ReadDir already sorts, and every name that survives Parse sorts the same
	// way, so the result is deterministic without a second sort.
	return out, nil
}

// CreateUser provisions a mailbox.
//
// Account control belongs to Orc; this exists so the tool is usable and
// testable before Orc's remote auth lands, and so that when it does land there
// is one function for it to call. It refuses to overwrite an existing user,
// because that would silently revoke a key someone is still using.
func (s *Store) CreateUser(name user.Name, key string) error {
	if name.Zero() {
		return fault.Usage{Reason: "no user name given"}
	}
	if err := user.CheckKey(key); err != nil {
		return err
	}

	return s.withLock(func() error {
		exists, err := s.HasUser(name)
		if err != nil {
			return err
		}
		if exists {
			return fault.Conflict{Path: s.userPath(name), Reason: "user " + name.String() + " already exists"}
		}

		rec, err := user.NewRecord(name, key, s.clock.Now(), nil)
		if err != nil {
			return err
		}
		data, err := rec.Encode()
		if err != nil {
			return err
		}
		if err := s.ops.mkdirAll(s.userDir(name), dirMode); err != nil {
			return fault.IO{Op: "create", Path: s.userDir(name), Err: err}
		}
		return s.writeNew(s.userPath(name), data)
	})
}

// DeleteUser removes a mailbox and its journal.
//
// Messages the user sent or received are left alone: they belong to their other
// participants too, and deleting them would silently edit other people's
// mailboxes. The mail becomes unaddressable, which is what "the account is
// gone" should mean.
func (s *Store) DeleteUser(name user.Name) error {
	if name.Zero() {
		return fault.Usage{Reason: "no user name given"}
	}
	return s.withLock(func() error {
		exists, err := s.HasUser(name)
		if err != nil {
			return err
		}
		if !exists {
			return fault.NotFound{Target: name.String()}
		}
		if err := s.ops.removeAll(s.userDir(name)); err != nil {
			return fault.IO{Op: "remove", Path: s.userDir(name), Err: err}
		}
		return nil
	})
}

// Small local helpers, kept here so this package does not import strconv for
// two conversions and so their failure modes are the ones this package wants.

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
