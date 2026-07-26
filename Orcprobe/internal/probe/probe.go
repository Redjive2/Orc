// Package probe is Orcprobe's filesystem: the only package that knows where a
// probe lives and what one is made of.
//
// A probe is a world: copied state, a repo, an environment, shims, and a
// manifest saying how it came to be. It is created whole or not at all, and it
// is removable whole — nothing about a probe is ever left in a place the
// operator has to remember.
//
// The layout is chosen for how it fails. probe.json is written last, so a probe
// without one is an interrupted creation and is refused by every command rather
// than half-used. The manifest is append-only, so a crash mid-creation still
// says how far it got.
package probe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"orc/common/sandbox"
	"orc/orcprobe/internal/clock"
	"orc/orcprobe/internal/fault"
	"orc/orcprobe/internal/snapshot"
	"orc/orcprobe/internal/source"
)

// Layout constants.
const (
	// Version is the probe-store format this package reads and writes. An
	// unknown version is a hard, clear error rather than a best effort.
	Version = 1

	versionFile = "version"
	currentFile = "current"
	probesDir   = "probes"

	// RecordFile names a probe, and its absence means the probe is unfinished.
	RecordFile = "probe.json"

	ManifestFile   = "manifest.jsonl"
	IdentitiesFile = "identities.json"
	EnvFile        = "env"
	BinDir         = "bin"
	StateDir       = "state"
	RepoDir        = "repo"
	ClaudeDir      = "claude"
	LogDir         = "log"
	SessionLog     = "log/session.jsonl"
)

// The stamp orcprobe writes and the other tools check.
//
// Both names, and the variable that turns the guard on, are defined once in
// orc/common/sandbox: the tool that stamps and the tools that refuse an
// unstamped root have to agree on the spelling, and two definitions of a
// security boundary is one too many.
const (
	StampFile  = sandbox.StampFile
	ProbeStamp = sandbox.ProbeStamp
	// EnvActive names the probe a process is running inside. It is the tripwire
	// every guard keys off, and the one variable whose absence means "this is
	// the real world".
	EnvActive = sandbox.EnvActive
)

// Environment variables consulted when no root is given.
const (
	EnvHome    = "ORCPROBE_HOME"
	EnvXDGData = "XDG_DATA_HOME"
)

// Bounds on a probe name. Names are path elements and appear in prompts, so
// they are held to the same shape Mailman holds a mailbox to.
const (
	MinNameLen = 1
	MaxNameLen = 40
)

// reserved names would collide with the store's own layout.
var reserved = map[string]bool{
	".": true, "..": true,
	"version": true, "current": true, "probes": true, "all": true, "none": true,
}

// Store is an opened probe store: the directory probes live in.
type Store struct {
	root  string
	clock clock.Clock
}

// DefaultRoot resolves where probes live when no path is given: an explicit
// override, then the XDG data directory, then a dot-directory in the home.
//
// It is deliberately *not* beside the repo. A `destroy` that could reach
// project files is a `destroy` that will eventually reach them.
func DefaultRoot(env source.Env, home string) (string, error) {
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
		return filepath.Join(filepath.Clean(data), "orcprobe"), nil
	}
	if home == "" {
		return "", fault.Usage{Reason: "no home directory found; set " + EnvHome + " to say where probes live"}
	}
	return filepath.Join(home, ".orcprobe"), nil
}

// Open opens a probe store, creating the layout if it is not there yet.
func Open(root string, c clock.Clock) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fault.Usage{Reason: "empty probe store path"}
	}
	if c == nil {
		return nil, fault.Internal{Where: "probe.Open", Detail: "no clock was given"}
	}
	s := &Store{root: filepath.Clean(root), clock: c}
	if err := s.init(); err != nil {
		return nil, err
	}
	return s, nil
}

// Root returns the directory probes live in.
func (s *Store) Root() string { return s.root }

func (s *Store) init() error {
	for _, dir := range []string{"", probesDir} {
		path := filepath.Join(s.root, dir)
		if err := os.MkdirAll(path, snapshot.DirMode); err != nil {
			return fault.IO{Op: "create", Path: path, Err: err}
		}
	}
	return s.checkVersion()
}

// checkVersion reads the version marker, writing it if the store is new. A
// store whose version this build does not understand is refused outright rather
// than read partially.
func (s *Store) checkVersion() error {
	path := filepath.Join(s.root, versionFile)

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return writeFile(path, []byte(fmt.Sprintf("%d\n", Version)), snapshot.FileMode)
	}
	if err != nil {
		return fault.IO{Op: "read", Path: path, Err: err}
	}

	text := strings.TrimSpace(string(data))
	var got int
	if _, err := fmt.Sscanf(text, "%d", &got); err != nil {
		return fault.Parse{Path: path, Reason: "store version is " + quote(text) + ", which is not a number"}
	}
	if got != Version {
		if got > Version {
			return fault.Parse{Path: path, Reason: fmt.Sprintf(
				"probe store is format version %d, but this orcprobe understands version %d; upgrade orcprobe rather than letting it guess", got, Version)}
		}
		return fault.Parse{Path: path, Reason: fmt.Sprintf(
			"probe store is format version %d, which this orcprobe (version %d) can no longer read", got, Version)}
	}
	return nil
}

// CheckName validates a probe name.
func CheckName(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case name == "":
		return "", fault.Usage{Reason: "probe name is empty"}
	case len(name) > MaxNameLen:
		return "", fault.Usage{Reason: fmt.Sprintf("probe name %q is longer than %d characters", raw, MaxNameLen)}
	case reserved[name]:
		return "", fault.Usage{Reason: fmt.Sprintf("probe name %q is reserved", name)}
	}
	for i, r := range name {
		if !allowed(r) {
			return "", fault.Usage{Reason: fmt.Sprintf(
				"probe name %q contains %q at position %d; use letters, digits, and . _ -", raw, r, i+1)}
		}
	}
	if !alphanumeric(rune(name[0])) {
		return "", fault.Usage{Reason: fmt.Sprintf("probe name %q must start with a letter or digit", raw)}
	}
	return name, nil
}

func allowed(r rune) bool { return alphanumeric(r) || r == '.' || r == '_' || r == '-' }

func alphanumeric(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

// Source is what one tool's state amounted to in a probe.
type Source struct {
	Tool    string `json:"tool"`
	Command string `json:"command"`
	// From is where it was copied from. It is recorded so drift can be checked
	// later, and so a probe can always say what world it is a picture of.
	From    string `json:"from"`
	Present bool   `json:"present"`
	Dir     string `json:"dir,omitempty"`
	Files   int    `json:"files"`
	Dirs    int    `json:"dirs"`
	Bytes   int64  `json:"bytes"`
	Digest  string `json:"digest,omitempty"`
}

// Record is a probe's creation record, written once and never edited.
type Record struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Created string `json:"created"`
	// Tool is the orcprobe build that made this probe.
	Tool string `json:"tool"`
	// Sources is every tool's state, present or not.
	Sources []Source `json:"sources"`
	// Repo is the copied working tree, when there is one.
	Repo *Source `json:"repo,omitempty"`
	// Claude is the copied hook configuration, when there is one.
	Claude *Source `json:"claude,omitempty"`
	// Identities is how many mailboxes were reminted into this probe.
	Identities int `json:"identities"`
	// Neutered says whether liveness was scrubbed. Recorded rather than
	// assumed: a probe that kept its claims must be able to say so years later.
	Neutered bool `json:"neutered"`
	// Unreleased counts the tasks that still show an owner after a scrub. A
	// probe with any is neutered only in part, and says so wherever it says
	// anything about its own liveness.
	Unreleased int `json:"unreleased,omitempty"`
}

// Probe is one opened probe.
type Probe struct {
	Record
	dir string
}

// Dir returns the probe's directory.
func (p *Probe) Dir() string { return p.dir }

// Path joins a path inside the probe.
func (p *Probe) Path(parts ...string) string {
	return filepath.Join(append([]string{p.dir}, parts...)...)
}

// CreatedAt parses the creation time.
func (p *Probe) CreatedAt() (time.Time, error) { return clock.Parse(p.Created) }

// Liveness describes how inert a probe is, in one word.
//
// Three states rather than two, because "the scrub ran" and "the scrub worked"
// are different facts and a probe that conflated them would be claiming more
// than it did.
func (p *Probe) Liveness() string {
	switch {
	case !p.Neutered:
		return "verbatim"
	case p.Unreleased > 0:
		return "partial"
	default:
		return "neutered"
	}
}

// dirFor returns where a named probe lives. The name is already validated, so
// this cannot escape the store.
func (s *Store) dirFor(name string) string { return filepath.Join(s.root, probesDir, name) }

// Get opens one probe by name.
func (s *Store) Get(name string) (*Probe, error) {
	clean, err := CheckName(name)
	if err != nil {
		return nil, err
	}
	dir := s.dirFor(clean)

	data, err := os.ReadFile(filepath.Join(dir, RecordFile))
	if err != nil {
		if os.IsNotExist(err) {
			// Distinguish "no such probe" from "a probe that never finished
			// being made": the second is cleanable, and saying so is the
			// difference between a dead end and a next step.
			if _, statErr := os.Stat(dir); statErr == nil {
				return nil, fault.Conflict{Path: dir, Reason: "probe " + clean +
					" was never finished; remove it with `orcprobe destroy " + clean + "`"}
			}
			near, _ := s.names()
			return nil, fault.NotFound{Target: clean, Near: near}
		}
		return nil, fault.IO{Op: "read", Path: filepath.Join(dir, RecordFile), Err: err}
	}

	rec, err := decodeRecord(filepath.Join(dir, RecordFile), data)
	if err != nil {
		return nil, err
	}
	if rec.Name != clean {
		return nil, fault.Conflict{Path: dir, Reason: "record names probe " + quote(rec.Name) + " but sits in " + quote(clean)}
	}
	return &Probe{Record: rec, dir: dir}, nil
}

// List returns every finished probe, oldest first, with unfinished ones
// reported separately so they can be cleaned up rather than silently ignored.
func (s *Store) List() ([]*Probe, []string, error) {
	names, err := s.names()
	if err != nil {
		return nil, nil, err
	}

	var probes []*Probe
	var unfinished []string
	for _, name := range names {
		p, err := s.Get(name)
		if err != nil {
			// An unreadable probe must not hide the readable ones, but a list
			// that quietly shows three of four is worse than one that shows
			// three and says so.
			unfinished = append(unfinished, name)
			continue
		}
		probes = append(probes, p)
	}
	sort.Slice(probes, func(i, j int) bool { return probes[i].Created < probes[j].Created })
	return probes, unfinished, nil
}

func (s *Store) names() ([]string, error) {
	dir := filepath.Join(s.root, probesDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fault.IO{Op: "list", Path: dir, Err: err}
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := CheckName(e.Name()); err != nil {
			continue // not something orcprobe put here
		}
		out = append(out, e.Name())
	}
	return out, nil
}

// Destroy removes a probe whole.
//
// This is the only irreversible command in the tool, so it refuses anything it
// cannot prove is a probe: the path must be inside this store, must be a
// directory, and must carry the probe stamp. A destroy that trusted its
// argument would be one typo away from removing a real store.
func (s *Store) Destroy(name string) error {
	clean, err := CheckName(name)
	if err != nil {
		return err
	}
	dir := s.dirFor(clean)

	if !source.Contains(filepath.Join(s.root, probesDir), dir) {
		return fault.Internal{Where: "probe.Destroy", Detail: "refusing to remove " + dir + ", which is not inside the probe store"}
	}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			near, _ := s.names()
			return fault.NotFound{Target: clean, Near: near}
		}
		return fault.IO{Op: "look at", Path: dir, Err: err}
	}
	if !info.IsDir() {
		return fault.Conflict{Path: dir, Reason: "is not a directory"}
	}
	if _, err := os.Stat(filepath.Join(dir, ProbeStamp)); err != nil {
		return fault.Conflict{Path: dir, Reason: "has no " + ProbeStamp +
			" file, so orcprobe cannot prove it made this directory and will not remove it"}
	}

	if err := os.RemoveAll(dir); err != nil {
		return fault.IO{Op: "remove", Path: dir, Err: err}
	}
	// A destroyed probe must not stay the default, or the next bare command
	// recreates the illusion that it is still there.
	if current, err := s.Current(); err == nil && current == clean {
		_ = os.Remove(filepath.Join(s.root, currentFile))
	}
	return nil
}

// Current returns the default probe's name, or "" when none is set.
func (s *Store) Current() (string, error) {
	data, err := os.ReadFile(filepath.Join(s.root, currentFile))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fault.IO{Op: "read", Path: filepath.Join(s.root, currentFile), Err: err}
	}
	name := strings.TrimSpace(string(data))
	if name == "" {
		return "", nil
	}
	return CheckName(name)
}

// SetCurrent makes a probe the default, after checking it exists.
func (s *Store) SetCurrent(name string) error {
	p, err := s.Get(name)
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(s.root, currentFile), []byte(p.Name+"\n"), snapshot.FileMode)
}

// Resolve picks the probe a command should act on: the one named, else the
// default. The error when neither exists says how to fix it, because "no probe"
// is the state every new user is in.
func (s *Store) Resolve(name string) (*Probe, error) {
	if strings.TrimSpace(name) != "" {
		return s.Get(name)
	}
	current, err := s.Current()
	if err != nil {
		return nil, err
	}
	if current == "" {
		names, _ := s.names()
		if len(names) == 0 {
			return nil, fault.NotFound{Target: "any probe",
				Near: []string{"orcprobe create <name>   — take one from the current world"}}
		}
		return nil, fault.Usage{Reason: "no default probe; use `orcprobe use <name>` or pass --probe <name>\n  probes: " +
			strings.Join(names, ", ")}
	}
	return s.Get(current)
}

// Stamp writes the marker that says a directory belongs to a probe.
//
// The write goes through orc/common/sandbox rather than through this package's
// own atomic helper, so the bytes the tools read are written by the code that
// defines how to read them. A stamp is small, is written once at creation, and
// is worthless if it disagrees with the reader.
func Stamp(dir, id string) error { return sandbox.Stamp(dir, id) }

// ReadStamp reads a stamp, mapping a missing one to a clear refusal rather than
// an i/o error: "this is not part of a probe" is the answer the caller wants.
func ReadStamp(dir string) (string, error) {
	for _, name := range []string{ProbeStamp, StampFile} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err == nil {
			return strings.TrimSpace(string(data)), nil
		}
		if !os.IsNotExist(err) {
			return "", fault.IO{Op: "read", Path: filepath.Join(dir, name), Err: err}
		}
	}
	return "", fault.Escape{
		Attempt: "use " + dir + " as a probe",
		Reason:  "it carries no probe stamp, so it is not part of one",
	}
}

// Encode renders a record for storage.
func (r Record) Encode() ([]byte, error) {
	if r.Version != Version {
		return nil, fault.Internal{Where: "probe.Record.Encode", Detail: fmt.Sprintf("record version %d", r.Version)}
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, fault.Internal{Where: "probe.Record.Encode", Detail: err.Error()}
	}
	return append(data, '\n'), nil
}

// decodeRecord reads a stored record. Unknown fields are refused rather than
// ignored: a field this version does not understand means a newer orcprobe
// wrote the probe, and acting on a partial understanding of what a probe
// contains is how a guard gets skipped.
func decodeRecord(path string, data []byte) (Record, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var r Record
	if err := dec.Decode(&r); err != nil {
		return Record{}, fault.Parse{Path: path, Reason: "probe record: " + err.Error()}
	}
	if dec.More() {
		return Record{}, fault.Parse{Path: path, Reason: "probe record has trailing content"}
	}
	if r.Version != Version {
		return Record{}, fault.Parse{Path: path, Reason: fmt.Sprintf(
			"probe record is version %d, this orcprobe writes version %d", r.Version, Version)}
	}
	if _, err := clock.Parse(r.Created); err != nil {
		return Record{}, fault.Parse{Path: path, Reason: "probe record created: " + err.Error()}
	}
	if _, err := CheckName(r.Name); err != nil {
		return Record{}, fault.Parse{Path: path, Reason: "probe record name: " + err.Error()}
	}
	return r, nil
}

func quote(s string) string { return `"` + s + `"` }
