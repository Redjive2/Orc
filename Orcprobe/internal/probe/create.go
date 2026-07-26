package probe

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"orc/orcprobe/internal/clock"
	"orc/orcprobe/internal/env"
	"orc/orcprobe/internal/fault"
	"orc/orcprobe/internal/mint"
	"orc/orcprobe/internal/neuter"
	"orc/orcprobe/internal/repo"
	"orc/orcprobe/internal/shim"
	"orc/orcprobe/internal/snapshot"
	"orc/orcprobe/internal/source"
)

// Build is the orcprobe version stamped into every probe. It is a constant
// rather than a build flag because a probe must be able to say what made it
// even when it is read years later by a build that no longer exists.
const Build = "orcprobe/1 (milestone 2)"

// Spec is a creation request.
type Spec struct {
	// Name is the probe's name.
	Name string
	// Env and Home are how the real world is located. They are injected so a
	// test can create a probe of a synthetic world without a home directory.
	Env  source.Env
	Home string

	// Repo is the working tree to copy. Empty means "find one from Cwd";
	// NoRepo means "do not copy one at all".
	Repo   string
	NoRepo bool
	Cwd    string

	// ClaudeDir is the user's Claude configuration directory, copied so hooks
	// behave inside a probe as they do outside.
	ClaudeDir string

	// FakeHome redirects HOME into the probe.
	FakeHome bool

	// LiveState keeps claims, collaborators, worktree bindings, and pending
	// notifications exactly as they were, for reproducing a real situation
	// rather than a clean one. It changes nothing about rule 1: no agent is
	// ever started either way.
	LiveState bool

	// ShimPath is the orcprobe-shim binary. Empty means it could not be found,
	// which is a guard that will be reported absent rather than a failure.
	ShimPath string
	// BasePath is the PATH the probe's own bin directory is prepended to.
	BasePath string
}

// Creation is what a create amounted to, for the operator to read.
//
// It carries no key material. identities.json is the only place plaintext probe
// keys exist, and a report that carried them would end up in a terminal
// scrollback, a log, or a paste.
type Creation struct {
	Probe *Probe
	// Drops is everything the copy left behind, already recorded in the
	// manifest and counted here so the summary can point at it.
	Drops int
	// Identities is how many mailboxes have probe keys.
	Identities int
	// Skipped names mailboxes whose records could not be reminted.
	Skipped []mint.Skip
	// Remotes and Worktrees are what detaching the repo removed.
	Remotes   []string
	Worktrees int
	// Scrub is what neutering did, or the zero value when --live-state kept
	// everything.
	Scrub neuter.Report
	// Shims reports whether the wrappers were installed, and which had to be
	// copied rather than linked.
	Shims  bool
	Copied []string
	// Deferred lists guarantees this build does not yet make. Every one is also
	// a manifest line, so a probe can say on its own which of the tool's
	// promises were true when it was made.
	Deferred []string
}

// Create takes a snapshot of the real world and lays it down as a new probe.
//
// The order is chosen so that the most likely failures happen before anything
// expensive: names and collisions first, then the refusals that protect the
// real world, then the copy. probe.json is written last, so a creation that
// dies anywhere above leaves a directory every command refuses to open and
// `destroy` will clean up.
func (s *Store) Create(spec Spec) (*Creation, error) {
	name, err := CheckName(spec.Name)
	if err != nil {
		return nil, err
	}
	if _, err := s.Get(name); err == nil {
		return nil, fault.Conflict{Path: s.dirFor(name), Reason: "probe " + name + " already exists"}
	} else if !isNotFound(err) {
		return nil, err
	}

	// A probe inside the thing it copies is a loop and a foot-gun: destroying it
	// would reach real state, and copying it would copy itself.
	if real, inside, err := source.Real(spec.Env, spec.Home, s.root); err != nil {
		return nil, err
	} else if inside {
		return nil, fault.Escape{
			Attempt: "create a probe at " + s.root,
			Target:  real,
			Reason:  "the probe store is inside real tool state; move " + EnvHome + " somewhere else",
		}
	}

	id, err := newID(s.clock)
	if err != nil {
		return nil, err
	}
	dir := s.dirFor(name)
	if err := os.MkdirAll(dir, snapshot.DirMode); err != nil {
		return nil, fault.IO{Op: "create", Path: dir, Err: err}
	}

	// From here, any failure removes the whole probe. A half-made world is worse
	// than none: it looks usable.
	created := false
	defer func() {
		if !created {
			_ = os.RemoveAll(dir)
		}
	}()

	if err := writeNew(filepath.Join(dir, ProbeStamp), []byte(id+"\n"), snapshot.FileMode); err != nil {
		return nil, err
	}
	man := OpenManifest(dir, s.clock)
	if err := man.Add(ActStamp, name, "probe "+id); err != nil {
		return nil, err
	}

	report := &Creation{}
	record := Record{
		Version: Version,
		ID:      id,
		Name:    name,
		Created: clock.Format(s.clock.Now()),
		Tool:    Build,
	}

	// State.
	roots, err := source.Find(spec.Env, spec.Home)
	if err != nil {
		return nil, err
	}
	for _, root := range roots {
		src, err := copySource(dir, root, man, report)
		if err != nil {
			return nil, err
		}
		record.Sources = append(record.Sources, src)
	}

	// The repo.
	if !spec.NoRepo {
		src, detached, err := copyRepo(dir, spec, man, report)
		if err != nil {
			return nil, err
		}
		record.Repo = src
		if detached != nil {
			report.Remotes = detached.Remotes
			report.Worktrees = detached.Worktrees
		}
	} else if err := man.Add(ActSkip, "repo", "--no-repo"); err != nil {
		return nil, err
	}

	// The Claude configuration.
	claude, err := copyClaude(dir, spec, man, report)
	if err != nil {
		return nil, err
	}
	record.Claude = claude

	// Liveness. Everything above is a copy; this is where the copy stops
	// looking like somewhere agents are working.
	if err := scrub(dir, spec, record, man, report); err != nil {
		return nil, err
	}
	record.Neutered = !spec.LiveState
	record.Unreleased = len(report.Scrub.Unreleased)

	// Credentials. Nothing real ever comes across, so every mailbox is given a
	// key this probe knows and the real digests are left behind.
	mailmanDir := filepath.Join(dir, source.Of(source.Mailman).Dir)
	orcDir := filepath.Join(dir, source.Of(source.Orc).Dir)
	result, err := mint.Fleet(mailmanDir, orcDir, s.clock)
	if err != nil {
		return nil, err
	}
	if err := mint.Save(filepath.Join(dir, IdentitiesFile), id, result.Identities); err != nil {
		return nil, err
	}
	for _, skip := range result.Skipped {
		if err := man.Add(ActNote, "identity "+skip.Name, "not reminted: "+skip.Why); err != nil {
			return nil, err
		}
	}
	if err := man.Add(ActMint, "identities", fmt.Sprintf(
		"%d identities given probe keys across mailman and orc; no real credential was copied, "+
			"and orc's plaintext keyring was rewritten rather than carried", len(result.Identities))); err != nil {
		return nil, err
	}
	record.Identities = len(result.Identities)
	report.Identities = len(result.Identities)
	report.Skipped = result.Skipped

	// Shims.
	if spec.ShimPath != "" {
		copied, err := shim.Install(filepath.Join(dir, BinDir), spec.ShimPath)
		if err != nil {
			return nil, err
		}
		report.Shims = true
		report.Copied = copied
		if err := man.Add(ActNote, "shims", "installed for "+strings.Join(shim.Commands(), ", ")); err != nil {
			return nil, err
		}
		if len(copied) > 0 {
			if err := man.Add(ActNote, "shims", "copied rather than linked: "+strings.Join(copied, ", ")+
				" — these go stale when orcprobe is rebuilt"); err != nil {
				return nil, err
			}
		}
	} else {
		report.Deferred = append(report.Deferred, "shims are not installed: the orcprobe-shim binary was not found, so `cq sync`, `git push`, and `orc` are refused only by orcprobe itself, not inside a probe shell")
	}

	// The environment.
	if err := os.MkdirAll(filepath.Join(dir, LogDir), snapshot.DirMode); err != nil {
		return nil, fault.IO{Op: "create", Path: filepath.Join(dir, LogDir), Err: err}
	}
	vars, err := composeEnv(dir, id, name, spec)
	if err != nil {
		return nil, err
	}
	if err := env.Write(filepath.Join(dir, EnvFile), vars); err != nil {
		return nil, err
	}

	// What this build does not yet do. These are promises the tool makes in its
	// documentation, so a probe that does not keep them must say so itself.
	if spec.LiveState {
		report.Deferred = append(report.Deferred,
			"--live-state: claims, collaborators, worktree bindings, and pending notifications came across untouched; nothing in here was scrubbed")
	}
	// The stamp guard is the one that catches an absolute path, and it lives in
	// the other tools rather than here. Whether it is in force is a fact about
	// the binaries on this machine, not about this probe — but a probe that
	// could not say either way would be claiming a wall it has not seen. So it
	// is recorded as a note, and `doctor` is what will check it (milestone 4).
	if err := man.Add(ActNote, "stamp guard",
		"every copied store is stamped; mailman, muff, and cq refuse an unstamped root when "+
			EnvActive+" is set. A build of those tools from before that landed will not."); err != nil {
		return nil, err
	}
	for _, note := range report.Deferred {
		if err := man.Add(ActDefer, "guarantee", note); err != nil {
			return nil, err
		}
	}

	// Last: the record that makes this a probe.
	data, err := record.Encode()
	if err != nil {
		return nil, err
	}
	if err := writeNew(filepath.Join(dir, RecordFile), data, snapshot.FileMode); err != nil {
		return nil, err
	}
	created = true

	report.Probe = &Probe{Record: record, dir: dir}
	return report, nil
}

// scrub takes the liveness out of the copy.
//
// It runs after everything has been copied and before anything is minted or
// installed, so it acts on a complete world and so a failure here still leaves
// nothing behind. With --live-state it does nothing at all — but it still says
// so in the manifest, because "this probe was never scrubbed" is exactly the
// fact someone reading it a month later needs.
func scrub(dir string, spec Spec, record Record, man *Manifest, report *Creation) error {
	if spec.LiveState {
		return man.Add(ActNote, "liveness",
			"--live-state: claims, collaborators, worktree bindings, and pending notifications kept as they were")
	}

	rep, err := neuter.Run(neuter.Spec{
		MacmuffinDir: presentDir(dir, record, source.Of(source.Macmuffin)),
		CQDir:        presentDir(dir, record, source.Of(source.CQ)),
		OrcDir:       presentDir(dir, record, source.Of(source.Orc)),
		ClaudeDir:    claudeDir(dir, record),
		BinDir:       filepath.Join(dir, BinDir),
		ProbeDir:     dir,
		Clock:        clockOf(man),
	})
	if err != nil {
		return err
	}
	report.Scrub = rep

	for _, change := range rep.Changes {
		if err := man.Add(change.Act, change.What, change.Detail); err != nil {
			return err
		}
	}
	return man.Add(ActNote, "liveness", fmt.Sprintf(
		"scrubbed: %d task(s) released, %d still owned, %d collaborator(s) removed, %d worktree binding(s) dropped, "+
			"%d notification(s) dropped, %d session claim(s) cut, %d hook(s) disabled",
		len(rep.Released), len(rep.Unreleased), rep.Collaborators, rep.Worktrees, rep.Outbox, rep.Sessions, len(rep.Hooks)))
}

// presentDir returns a copied tool's directory inside the probe, or "" when
// that tool had nothing to copy. Scrubbing a directory that is not there would
// be harmless, but reporting on one would not.
func presentDir(dir string, record Record, tool source.Tool) string {
	for _, src := range record.Sources {
		if src.Tool == tool.Name && src.Present {
			return filepath.Join(dir, filepath.FromSlash(tool.Dir))
		}
	}
	return ""
}

func claudeDir(dir string, record Record) string {
	if record.Claude == nil || !record.Claude.Present {
		return ""
	}
	return filepath.Join(dir, ClaudeDir)
}

// clockOf reaches the clock the store was opened with, so a scrub's timestamps
// come from the same source as everything else in the probe.
func clockOf(man *Manifest) clock.Clock { return man.clock }

// copySource copies one tool's state, or records that there was none.
func copySource(dir string, root source.Root, man *Manifest, report *Creation) (Source, error) {
	src := Source{
		Tool:    root.Tool.Name,
		Command: root.Tool.Command,
		From:    root.Path,
		Present: root.Present,
		Dir:     root.Tool.Dir,
	}
	target := filepath.Join(dir, filepath.FromSlash(root.Tool.Dir))

	// A tool with nothing to copy still gets an empty directory, and that
	// directory still gets stamped.
	//
	// Two reasons, both learned the hard way. Minting creates the god mailbox
	// whether or not a mail store came across, so the directory appears anyway
	// — and an unstamped one is a store every tool refuses, which makes the
	// probe useless in a way that looks like a bug in Mailman. And a probe of a
	// machine where a tool has never run should still be a place that tool can
	// run: an empty stamped store is exactly what it would have made itself.
	if !root.Present {
		if err := os.MkdirAll(target, snapshot.DirMode); err != nil {
			return Source{}, fault.IO{Op: "create", Path: target, Err: err}
		}
		if err := Stamp(target, stampID(dir)); err != nil {
			return Source{}, err
		}
		return src, man.Add(ActSkip, root.Tool.Name, "nothing at "+root.Path+
			"; an empty stamped store was made instead, so the tool still works in the probe")
	}

	rep, err := snapshot.Copy(target, root.Path, snapshot.Options{})
	if err != nil {
		return Source{}, err
	}
	src.Files, src.Dirs, src.Bytes, src.Digest = rep.Files, rep.Dirs, rep.Bytes, rep.Digest

	if err := man.Add(ActCopy, root.Tool.Name, fmt.Sprintf("%s → %s (%d files, %d bytes)",
		root.Path, root.Tool.Dir, rep.Files, rep.Bytes)); err != nil {
		return Source{}, err
	}
	if err := recordDrops(man, root.Tool.Name, rep.Drops, report); err != nil {
		return Source{}, err
	}
	if err := Stamp(target, stampID(dir)); err != nil {
		return Source{}, err
	}
	return src, nil
}

// copyRepo copies the working tree and cuts every route out of it.
func copyRepo(dir string, spec Spec, man *Manifest, report *Creation) (*Source, *repo.Report, error) {
	path := strings.TrimSpace(spec.Repo)
	if path == "" {
		found, ok := repo.Find(spec.Cwd)
		if !ok {
			return nil, nil, man.Add(ActSkip, "repo", "no git working tree at or above "+spec.Cwd)
		}
		path = found
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, fault.IO{Op: "locate", Path: path, Err: err}
	}

	// A repo that contains the probe store would copy the probe into itself.
	if source.Contains(abs, dir) {
		return nil, nil, fault.Escape{
			Attempt: "copy the repo at " + abs,
			Target:  dir,
			Reason:  "the probe store is inside that repo, so the copy would contain itself",
		}
	}

	target := filepath.Join(dir, RepoDir)
	rep, err := snapshot.Copy(target, abs, snapshot.Options{})
	if err != nil {
		return nil, nil, err
	}
	if err := man.Add(ActCopy, "repo", fmt.Sprintf("%s → %s (%d files, %d bytes)", abs, RepoDir, rep.Files, rep.Bytes)); err != nil {
		return nil, nil, err
	}
	if err := recordDrops(man, "repo", rep.Drops, report); err != nil {
		return nil, nil, err
	}

	detached, err := repo.Detach(target)
	if err != nil {
		return nil, nil, err
	}
	for _, remote := range detached.Remotes {
		if err := man.Add(ActDrop, "repo remote "+remote, "a probe has nowhere to push"); err != nil {
			return nil, nil, err
		}
	}
	if detached.Worktrees > 0 {
		if err := man.Add(ActDrop, "repo worktrees",
			fmt.Sprintf("%d registration(s) removed; a probe worktree over a real checkout is the escape itself", detached.Worktrees)); err != nil {
			return nil, nil, err
		}
	}
	if err := Stamp(target, stampID(dir)); err != nil {
		return nil, nil, err
	}

	return &Source{
		Tool: "repo", Command: "git", From: abs, Present: true, Dir: RepoDir,
		Files: rep.Files, Dirs: rep.Dirs, Bytes: rep.Bytes, Digest: rep.Digest,
	}, &detached, nil
}

// copyClaude copies the hook configuration a probe runs under.
func copyClaude(dir string, spec Spec, man *Manifest, report *Creation) (*Source, error) {
	path := strings.TrimSpace(spec.ClaudeDir)
	if path == "" {
		return nil, man.Add(ActSkip, "claude", "no configuration directory given")
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, man.Add(ActSkip, "claude", "nothing at "+path)
		}
		return nil, fault.IO{Op: "look at", Path: path, Err: err}
	}
	if !info.IsDir() {
		return nil, man.Add(ActSkip, "claude", path+" is not a directory")
	}

	target := filepath.Join(dir, ClaudeDir)
	// Only the configuration comes across. A Claude directory holds session
	// transcripts, caches, and project state that would balloon a probe and has
	// nothing to do with how a hook behaves.
	rep, err := snapshot.Copy(target, path, snapshot.Options{
		Exclude: func(rel string) bool {
			switch {
			case rel == "settings.json", rel == "settings.local.json":
				return false
			case strings.HasPrefix(rel, "hooks"):
				return false
			default:
				return true
			}
		},
	})
	if err != nil {
		return nil, err
	}
	if err := man.Add(ActCopy, "claude", fmt.Sprintf("%s → %s (settings and hooks only, %d files)", path, ClaudeDir, rep.Files)); err != nil {
		return nil, err
	}
	if err := Stamp(target, stampID(dir)); err != nil {
		return nil, err
	}
	return &Source{
		Tool: "Claude", Command: "claude", From: path, Present: true, Dir: ClaudeDir,
		Files: rep.Files, Dirs: rep.Dirs, Bytes: rep.Bytes, Digest: rep.Digest,
	}, nil
}

func recordDrops(man *Manifest, what string, drops []snapshot.Drop, report *Creation) error {
	for _, d := range drops {
		if d.Why == "excluded" {
			continue // an exclusion is a rule, not a surprise
		}
		report.Drops++
		if err := man.Add(ActDrop, what+": "+d.Rel, d.Why); err != nil {
			return err
		}
	}
	return nil
}

// composeEnv builds the environment for a probe directory.
func composeEnv(dir, id, name string, spec Spec) ([]env.Var, error) {
	spec2 := env.Spec{
		ProbeID:      id,
		ProbeName:    name,
		ProbeDir:     dir,
		MailmanDir:   filepath.Join(dir, filepath.FromSlash(source.Of(source.Mailman).Dir)),
		MacmuffinDir: filepath.Join(dir, filepath.FromSlash(source.Of(source.Macmuffin).Dir)),
		CQDir:        filepath.Join(dir, filepath.FromSlash(source.Of(source.CQ).Dir)),
		OrcDir:       filepath.Join(dir, filepath.FromSlash(source.Of(source.Orc).Dir)),
		XDGDir:       filepath.Join(dir, StateDir, "xdg"),
		BinDir:       filepath.Join(dir, BinDir),
		ClaudeDir:    filepath.Join(dir, ClaudeDir),
		RepoDir:      filepath.Join(dir, RepoDir),
		GitConfig:    filepath.Join(dir, RepoDir, repo.ProbeConfig),
		BasePath:     spec.BasePath,
	}
	if spec.FakeHome {
		spec2.FakeHome = filepath.Join(dir, StateDir, "home")
		if err := os.MkdirAll(spec2.FakeHome, snapshot.DirMode); err != nil {
			return nil, fault.IO{Op: "create", Path: spec2.FakeHome, Err: err}
		}
	}
	// The XDG backstop must exist, or a tool that falls back to it will create
	// it — and a tool creating a directory is a tool that resolved a path, which
	// is exactly what should have been redirected.
	if err := os.MkdirAll(spec2.XDGDir, snapshot.DirMode); err != nil {
		return nil, fault.IO{Op: "create", Path: spec2.XDGDir, Err: err}
	}
	return env.Compose(spec2)
}

// stampID reads the probe's own stamp, so every nested stamp agrees with it.
func stampID(dir string) string {
	id, err := ReadStamp(dir)
	if err != nil {
		return ""
	}
	return id
}

// newID mints a probe identifier: sortable by creation time without a lookup,
// unique across processes without coordination. The form is Mailman's message
// id, for the same reasons.
func newID(c clock.Clock) (string, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", fault.Internal{Where: "probe.newID", Detail: "no entropy: " + err.Error()}
	}
	return fmt.Sprintf("%x-%s", c.Now().UnixMicro(), hex.EncodeToString(buf)), nil
}

func isNotFound(err error) bool {
	_, ok := err.(fault.NotFound)
	return ok
}
