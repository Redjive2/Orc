// Package sandbox is how a tool knows it is running inside an Orcprobe probe,
// and refuses to touch anything that is not part of it.
//
// Orcprobe copies the whole Orc world into a disposable sandbox and redirects
// every tool at the copy with environment variables. That redirection is the
// wall, and it has exactly one hole: an absolute path. Anything that resolves
// `~/.mailman` by hand, or restores `MAILMAN_HOME` from a shell profile, or
// hardcodes a store location, walks straight past it and writes to the real
// world from inside what the operator believes is a sandbox.
//
// This package closes that hole, and it can only be closed here — in the tools
// themselves — because orcprobe is not in the room when `mailman send` runs.
// The contract is three lines:
//
//  1. Orcprobe sets $ORCPROBE_ACTIVE to the probe's id and stamps every
//     directory it copied with a matching `.orcprobe-stamp`.
//  2. A tool that opens a store calls Guard with the root it resolved.
//  3. With $ORCPROBE_ACTIVE set, an unstamped root — or one stamped for a
//     different probe — is refused rather than opened.
//
// Outside a probe the guard costs one map lookup and does nothing, which is the
// ordinary case and the one that must never be slowed or broken by this.
//
// The refusal is an escape (exit 11), not a permission problem. It means
// containment failed, which is the one thing a monitor watching a probe should
// alarm on.
package sandbox

import (
	"os"
	"path/filepath"
	"strings"

	"orc/common/fault"
)

// EnvActive names the probe a process is inside. Its absence means "this is the
// real world", which is why the guard's default is to do nothing: a tool run
// normally must behave exactly as it did before this package existed.
const EnvActive = "ORCPROBE_ACTIVE"

// StampFile marks a directory as part of a probe. It holds the probe's id, so a
// stamp cannot be copied from one probe into another and still pass.
const StampFile = ".orcprobe-stamp"

// ProbeStamp is the stamp at a probe's own root, in capitals because someone
// listing the directory should see it first. Guard accepts either name, so a
// tool pointed at a probe root rather than at a store inside one is not refused
// for the wrong reason.
const ProbeStamp = "STAMP"

// Env looks up an environment variable, reporting whether it was set. It is
// injected everywhere here so the guard is testable without touching the real
// environment — a test that had to set ORCPROBE_ACTIVE globally would make
// every other test in the package racy.
type Env func(key string) (string, bool)

// OSEnv reads the process environment.
func OSEnv(key string) (string, bool) { return os.LookupEnv(key) }

// MapEnv reads an injected environment, for tests.
func MapEnv(m map[string]string) Env {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

// Active reports the probe this process is inside, if any.
func Active(env Env) (string, bool) {
	if env == nil {
		env = OSEnv
	}
	id, ok := env(EnvActive)
	id = strings.TrimSpace(id)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

// Refusal is a store root that is not part of the probe this process is inside.
//
// It unwraps to fault.ErrEscape, so every tool exits 11 for it without having
// to classify it, and so a hook can tell "containment failed" from an ordinary
// scope refusal without parsing text.
type Refusal struct {
	// Root is the store that was refused.
	Root string
	// Probe is the probe this process is inside.
	Probe string
	// Stamp is the id the root carried, when it carried one. An empty Stamp
	// means the root was not stamped at all, which is the ordinary case and a
	// different mistake from a root belonging to another probe.
	Stamp string
}

func (e Refusal) Error() string {
	var b strings.Builder
	b.WriteString("refusing to open ")
	b.WriteString(e.Root)
	b.WriteString(": this process is inside probe ")
	b.WriteString(e.Probe)

	if e.Stamp == "" {
		// The phrase an operator greps for is kept whole on its own line: a
		// reassurance split across a line break is one nobody can search for.
		b.WriteString(", and that store is not part of it.\n")
		b.WriteString("  Nothing was written.\n")
		b.WriteString("  Something resolved a real path from inside a probe — an absolute path, a\n")
		b.WriteString("  restored environment variable, or a hardcoded store location. Leave the\n")
		b.WriteString("  probe, or point this at the probe's own copy.")
		return b.String()
	}
	b.WriteString(", but that store belongs to probe ")
	b.WriteString(e.Stamp)
	b.WriteString(".\n  Nothing was written.\n  Two probes' state must never be mixed.")
	return b.String()
}

func (e Refusal) Unwrap() error { return fault.ErrEscape }

// Guard refuses a store root that is not part of the active probe.
//
// It returns nil when the process is not inside a probe, which is every
// ordinary run of every tool. Inside one, the root must carry a stamp naming
// that probe.
//
// A stamp that cannot be read for any reason other than absence is also a
// refusal. The alternative — treating an unreadable stamp as "probably fine" —
// would make the guard fail open, and a guard that fails open is worse than no
// guard, because the operator believes it is there.
func Guard(env Env, root string) error {
	probe, inside := Active(env)
	if !inside {
		return nil
	}
	if strings.TrimSpace(root) == "" {
		return Refusal{Root: "an empty path", Probe: probe}
	}

	stamp, found, err := readStamp(root)
	switch {
	case err != nil:
		return Refusal{Root: root, Probe: probe}
	case !found:
		return Refusal{Root: root, Probe: probe}
	case stamp != probe:
		return Refusal{Root: root, Probe: probe, Stamp: stamp}
	default:
		return nil
	}
}

// readStamp reads a directory's stamp, reporting whether one was there.
func readStamp(dir string) (string, bool, error) {
	for _, name := range []string{StampFile, ProbeStamp} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err == nil {
			return strings.TrimSpace(string(data)), true, nil
		}
		if !os.IsNotExist(err) {
			return "", false, err
		}
	}
	return "", false, nil
}

// Stamp writes the marker. Orcprobe is the only thing that calls it — a tool
// stamping its own store would be a tool granting itself permission — but it
// lives here so the file's name and contents are defined once for everything
// that reads or writes them.
func Stamp(dir, probe string) error {
	if strings.TrimSpace(probe) == "" {
		return fault.Internal{Where: "sandbox.Stamp", Detail: "no probe id given"}
	}
	path := filepath.Join(dir, StampFile)
	if err := os.WriteFile(path, []byte(probe+"\n"), 0o600); err != nil {
		return fault.IO{Op: "write", Path: path, Err: err}
	}
	return nil
}
