// Package store is Orc's filesystem: the only package that knows where anything
// lives.
//
// The layout is chosen for how it fails, and the reasoning is Macmuffin's
// because the situation is the same: several agents run `orc` at once, none of
// them coordinate, and any of them may be killed at any moment. So a creation
// record is written once and never touched again, everything mutable is an
// append-only journal replayed on every command, and there is one lock per
// entity rather than one for the store.
//
// Orc differs from every other store in the tree in one way, and it drives the
// permissions on everything here: **it holds plaintext keys**. Orc is the only
// thing that must hand a credential out later — on populate, on refresh, on
// every restart — and a digest cannot be turned back into a key. So the store is
// 0700, every file in it is 0600, and that directory mode is what protects every
// credential in the fleet. See Claude/Docs/Orc/Plan.md §4.2 and §7.5 for what
// that does and does not stop.
package store

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"orc/common/clock"
	"orc/common/fault"
	"orc/common/sandbox"
	"orc/common/user"
	"orc/orc/internal/model"
)

// Layout constants.
const (
	// Version is the store format this package reads and writes. An unknown
	// version is a hard, clear error rather than a best effort: guessing at a
	// layout a newer Orc wrote is how an authority level gets misread.
	Version = 1

	versionFile  = "version"
	operatorFile = "operator"

	permissionsDir = "permissions"
	rolesDir       = "roles"
	identitiesDir  = "identities"

	roleFile     = "role.json"
	identityFile = "identity.json"
	journalFile  = "journal.jsonl"
	entityLock   = "lock"

	// The credential pair. user.json holds the salt and digest, as every other
	// Orc tool stores it; keyFile holds the plaintext, which only Orc has.
	userFile = "user.json"
	keyFile  = "key"

	// Per-identity directories. claude/ is the session's CLAUDE_CONFIG_DIR and
	// workspace/ is its working directory; both are made with the identity, so
	// populating one later is not also a provisioning step.
	claudeDir    = "claude"
	workspaceDir = "workspace"
	sessionDir   = "session"
)

// Environment variables consulted when no root is given.
const (
	EnvHome    = "ORC_HOME"
	EnvXDGData = "XDG_DATA_HOME"
)

// Bounds. Each turns a damaged or hostile store into a clear message rather than
// an out-of-memory kill.
const (
	// MaxJournalLine bounds one event.
	MaxJournalLine = 8 << 10

	// MaxJournalSize bounds one entity's journal.
	MaxJournalSize = 16 << 20

	// MaxRecordSize bounds a creation record.
	MaxRecordSize = 64 << 10

	// MaxIdentities bounds a fleet: far past what one machine can run, and small
	// enough that the quadratic parts of the derivation stay instant.
	MaxIdentities = 512

	// MaxRoles and MaxPermissions bound the policy side of the store.
	MaxRoles       = 512
	MaxPermissions = 512
)

// Store is an opened Orc store. It is safe for concurrent use within a process,
// and safe against other processes through the per-entity advisory lock.
type Store struct {
	root  string
	clock clock.Clock
	ops   ops

	// readOnly refuses every write path. It exists for the same reason
	// Macmuffin's does: `orc-hook` fires on every tool call in a live session and
	// must be a bystander, so it opens a door that creates nothing.
	readOnly bool

	// mu serialises this process's own writers. The file lock handles other
	// processes; flock is per open file description rather than per thread, so it
	// would not.
	mu sync.Mutex
}

// Root returns the directory the store lives in.
func (s *Store) Root() string { return s.root }

// Now returns the store's clock, so a caller building an event stamps it with
// the same time source the store does.
func (s *Store) Now() time.Time { return s.clock.Now() }

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
		return filepath.Join(filepath.Clean(data), "orc"), nil
	}
	if home == "" {
		return "", fault.Usage{Reason: "no home directory found; set " + EnvHome + " to say where the fleet is"}
	}
	return filepath.Join(home, ".orc"), nil
}

// Create opens a store, making the layout if it is not there yet.
//
// Unlike every other tool in the tree, this is *not* what an ordinary command
// does. `orc bootstrap` calls it; everything else calls Open, which refuses a
// store that does not exist. The reason is that a store is not the whole of what
// bootstrap makes — there is also an operator identity, a key, and a mailbox —
// so a store conjured into existence by `orc status` would be a fleet with no
// operator, which is a state no command can do anything with.
func Create(root string, c clock.Clock) (*Store, error) {
	s, err := prepare(root, c, "store.Create")
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{"", permissionsDir, rolesDir, identitiesDir} {
		path := filepath.Join(s.root, dir)
		if err := s.ops.mkdirAll(path, dirMode); err != nil {
			return nil, fault.IO{Op: "create", Path: path, Err: err}
		}
	}
	if err := s.checkVersion(); err != nil {
		return nil, err
	}
	return s, nil
}

// Open opens an existing store for writing.
//
// A store that is not there is reported as not there, with the command that
// makes one — the alternative is a fleet that quietly appears in whatever
// directory a mistyped ORC_HOME pointed at.
func Open(root string, c clock.Clock) (*Store, error) {
	s, err := prepare(root, c, "store.Open")
	if err != nil {
		return nil, err
	}
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	return s, nil
}

// Read opens an existing store without touching it.
//
// It creates nothing and every write path refuses. This is the door `orc-hook`
// and `orc introspect` use: both run inside somebody's live session, and neither
// has any business creating a fleet.
func Read(root string, c clock.Clock) (*Store, error) {
	s, err := prepare(root, c, "store.Read")
	if err != nil {
		return nil, err
	}
	s.readOnly = true
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	return s, nil
}

// prepare does what all three doors share, including the guard.
func prepare(root string, c clock.Clock, where string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fault.Usage{Reason: "empty store path"}
	}
	if c == nil {
		return nil, fault.Internal{Where: where, Detail: "no clock was given"}
	}

	// Before anything is created, and before anything is read. Orcprobe redirects
	// ORC_HOME at a probe's copy; a process that resolved a real path anyway — by
	// an absolute path, a restored environment variable, or a hardcoded location
	// — would otherwise touch the real fleet from inside what the operator
	// believes is a sandbox. This is the third of the four rows Orcprobe's plan
	// left open, and it costs one map lookup outside a probe.
	if err := sandbox.Guard(sandbox.OSEnv, root); err != nil {
		return nil, err
	}
	return &Store{root: filepath.Clean(root), clock: c, ops: realOps()}, nil
}

// requireStore refuses a root that is not an Orc store.
func (s *Store) requireStore() error {
	path := filepath.Join(s.root, versionFile)
	if _, err := s.ops.readFile(path); err != nil {
		if os.IsNotExist(err) {
			return fault.NotFound{Target: "an orc fleet at " + s.root + "; `orc bootstrap` makes one"}
		}
		return fault.IO{Op: "read", Path: path, Err: err}
	}
	return s.checkVersion()
}

// refuseWrite is the guard on every path that would change the store.
func (s *Store) refuseWrite() error {
	if !s.readOnly {
		return nil
	}
	return fault.Denied{Actor: "this process", Action: "write", Target: s.root,
		Reason: "the store was opened read-only"}
}

// checkVersion reads the version marker, writing it if the store is new.
//
// A store whose version this build does not understand is refused outright.
// Reading what it recognises and ignoring the rest would silently drop whatever
// the newer format added, which here could be an authority level or an expiry.
func (s *Store) checkVersion() error {
	path := filepath.Join(s.root, versionFile)

	data, err := s.ops.readFile(path)
	if os.IsNotExist(err) {
		if s.readOnly {
			return fault.NotFound{Target: "an orc fleet at " + s.root}
		}
		return s.writeFile(path, []byte(itoa(Version)+"\n"))
	}
	if err != nil {
		return fault.IO{Op: "read", Path: path, Err: err}
	}

	text := strings.TrimSpace(string(data))
	got, convErr := parseInt(text)
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
		return "fleet is format version " + itoa(got) + ", but this orc understands version " +
			itoa(Version) + "; upgrade orc rather than letting it guess"
	}
	return "fleet is format version " + itoa(got) + ", which this orc (version " +
		itoa(Version) + ") can no longer read"
}

// Paths. A model.Name and a user.Name are both already validated — that is what
// the types mean — so none of these can escape the store.

func (s *Store) permissionPath(name model.Name) string {
	return filepath.Join(s.root, permissionsDir, name.String()+".json")
}

func (s *Store) roleDir(name model.Name) string {
	return filepath.Join(s.root, rolesDir, name.String())
}

func (s *Store) identityDir(name user.Name) string {
	return filepath.Join(s.root, identitiesDir, name.String())
}

// IdentityDir returns where an identity's own files live. It is exported because
// provisioning writes the Claude configuration and the workspace, and a second
// package computing the same path from the root is a second definition of the
// layout.
func (s *Store) IdentityDir(name user.Name) string { return s.identityDir(name) }

// ClaudeDir returns an identity's CLAUDE_CONFIG_DIR.
func (s *Store) ClaudeDir(name user.Name) string {
	return filepath.Join(s.identityDir(name), claudeDir)
}

// WorkspaceDir returns an identity's working directory.
//
// Almost always the derived path, `<root>/identities/<name>/workspace`, which is
// where every identity starts. An identity that has been pointed somewhere else
// carries the exception in its journal, and this is where that is honoured — rather
// than at each of the eight places that ask, which include the supervisor's `cmd.Dir`
// and the hook's path resolution. One of them missing the exception would be an agent
// working in one directory while its permissions were checked against another.
//
// An identity that will not load falls back to the derived path rather than failing.
// The signature is the reason and the behaviour is right anyway: the hook asks this
// on every tool call and must fail open, and an identity Orc cannot read is one no
// command is about to succeed at regardless.
func (s *Store) WorkspaceDir(name user.Name) string {
	derived := filepath.Join(s.identityDir(name), workspaceDir)

	got, err := s.Identity(name)
	if err != nil || got.Workspace() == "" {
		return derived
	}
	return got.Workspace()
}

// SessionDir returns where an identity's live session state goes. Nothing writes
// there in milestone 1; the path is defined here so that milestone 2 adds a
// supervisor rather than a second opinion about the layout.
func (s *Store) SessionDir(name user.Name) string {
	return filepath.Join(s.identityDir(name), sessionDir)
}

// withLock runs fn holding both the process mutex and an entity's file lock.
//
// The lock is per entity, so two agents changing different identities never wait
// for each other. Readers deliberately do not take it: a torn journal tail is
// already handled by replay, and blocking every `status` behind every `assign`
// is how a tool becomes the thing agents route around.
func (s *Store) withLock(dir string, fn func() error) error {
	if strings.TrimSpace(dir) == "" {
		return fault.Internal{Where: "store.withLock", Detail: "no entity directory named"}
	}
	if err := s.refuseWrite(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ops.mkdirAll(dir, dirMode); err != nil {
		return fault.IO{Op: "create", Path: dir, Err: err}
	}

	path := filepath.Join(dir, entityLock)
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

// Operator returns the name bootstrap recorded for the operator identity.
//
// It is kept in a file of its own as well as being derivable — the operator is
// the identity with no boss — because bootstrap has to know whether it has
// already run *before* there is a tree to derive anything from, and because
// `orc verify` comparing the two catches a store where an identity's boss was
// hand-edited away.
func (s *Store) Operator() (user.Name, error) {
	data, err := s.ops.readFile(filepath.Join(s.root, operatorFile))
	if err != nil {
		if os.IsNotExist(err) {
			return user.Name{}, fault.NotFound{Target: "an operator; `orc bootstrap` makes one"}
		}
		return user.Name{}, fault.IO{Op: "read", Path: filepath.Join(s.root, operatorFile), Err: err}
	}
	name, err := user.Parse(strings.TrimSpace(string(data)))
	if err != nil {
		return user.Name{}, fault.Parse{Path: filepath.Join(s.root, operatorFile),
			Reason: "operator name: " + err.Error()}
	}
	return name, nil
}

// SetOperator records who the operator is. It refuses to overwrite: a fleet has
// one root, and a second bootstrap must be a message rather than a coup.
func (s *Store) SetOperator(name user.Name) error {
	if name.Zero() {
		return fault.Internal{Where: "store.SetOperator", Detail: "no name given"}
	}
	path := filepath.Join(s.root, operatorFile)
	if existing, err := s.Operator(); err == nil {
		if existing.String() == name.String() {
			return nil
		}
		return fault.Conflict{Path: path, Reason: fmt.Sprintf(
			"this fleet's operator is %s; there is one operator and it cannot be replaced", existing)}
	}
	return s.writeNew(path, []byte(name.String()+"\n"))
}

// names lists the entries of a directory that parse as names, in sorted order.
func (s *Store) names(dir string, limit int, what string) ([]string, error) {
	entries, err := s.ops.readDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fault.IO{Op: "list", Path: dir, Err: err}
	}
	if len(entries) > limit {
		return nil, fault.Parse{Path: dir, Reason: "fleet holds more than " + itoa(limit) + " " + what}
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	slices.Sort(out)
	return out, nil
}
