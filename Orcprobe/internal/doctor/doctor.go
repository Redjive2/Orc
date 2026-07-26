// Package doctor checks a probe's guards and says which are actually in force.
//
// This is the package that makes the tool's honesty operational rather than
// documentary. Everything else in Orcprobe describes the wall; this one goes and
// looks at it, and its whole value is that it is willing to come back and say a
// guard is missing.
//
// Two kinds of check, and the difference matters:
//
//   - **Structural** checks read the probe: are the stores stamped, does the env
//     file redirect everything, are the shims installed, did the repo copy lose
//     its remotes. These are facts about this probe, and they are cheap.
//   - **Behavioural** checks run the real tools and watch what they do. The
//     stamp guard lives in Mailman, Macmuffin, and cq, so whether it is in force
//     is a fact about *the binaries on this machine* — a build from before it
//     landed will silently not have it. Asserting the guard exists because the
//     plan says so is exactly the failure this package exists to prevent, so it
//     is measured: point the tool at an unstamped directory with the tripwire
//     set, and see whether it refuses.
//
// A behavioural check is skipped, never guessed, when the tool is not
// installed. "Absent" and "not checked" are different answers and are reported
// differently.
package doctor

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"orc/common/sandbox"
	"orc/orcprobe/internal/env"
	"orc/orcprobe/internal/fault"
	"orc/orcprobe/internal/shim"
	"orc/orcprobe/internal/spawn"
)

// State is a check's outcome.
type State string

const (
	// InForce is a guard that was checked and is working.
	InForce State = "in force"
	// Absent is a guard that was checked and is not there. This is the one the
	// whole package exists to be able to say.
	Absent State = "absent"
	// Skipped is a guard that could not be checked. It is not reassurance.
	Skipped State = "not checked"
	// Warn is a guard that is present but weaker than it should be.
	Warn State = "partial"
)

// Check is one guard and what was found.
type Check struct {
	// Guard names the layer, matching the table in the reference.
	Guard string
	// What is the specific thing checked.
	What  string
	State State
	// Detail explains the outcome, and for anything but InForce says what to do
	// about it.
	Detail string
}

// Report is every check, in the order they were run.
type Report struct {
	Checks []Check
}

// Add records one outcome.
func (r *Report) Add(guard, what string, state State, detail string) {
	r.Checks = append(r.Checks, Check{Guard: guard, What: what, State: state, Detail: detail})
}

// Sound reports whether every guard that was checked is in force. A skipped
// check does not make a probe unsound — it makes it unmeasured, which `--strict`
// treats differently.
func (r Report) Sound() bool {
	for _, c := range r.Checks {
		if c.State == Absent {
			return false
		}
	}
	return true
}

// Measured reports whether every check produced an answer.
func (r Report) Measured() bool {
	for _, c := range r.Checks {
		if c.State == Skipped {
			return false
		}
	}
	return true
}

// Counts summarises the report.
func (r Report) Counts() (inForce, absent, skipped, partial int) {
	for _, c := range r.Checks {
		switch c.State {
		case InForce:
			inForce++
		case Absent:
			absent++
		case Skipped:
			skipped++
		case Warn:
			partial++
		}
	}
	return
}

// Spec is everything a run needs. Every path is inside the probe.
type Spec struct {
	ProbeID    string
	ProbeDir   string
	StateDirs  map[string]string // tool command → copied store, for stamp checks
	RepoDir    string
	BinDir     string
	EnvFile    string
	Identities string

	// Environ is the environment the behavioural checks run tools in. It is the
	// real one, minus anything that would point a tool at a probe.
	Environ []string
	// Path is where to look for the tools.
	Path string
	// Stdout and Stderr swallow a checked tool's output: doctor reports what a
	// tool *did*, not what it said.
	Quiet bool
}

// Run performs every check.
func Run(s Spec) (Report, error) {
	var r Report

	if err := stamps(s, &r); err != nil {
		return r, err
	}
	if err := redirection(s, &r); err != nil {
		return r, err
	}
	shims(s, &r)
	detachment(s, &r)
	secrets(s, &r)
	guards(s, &r)

	return r, nil
}

// stamps checks that every copied store carries this probe's mark. Without it
// the tools' guard has nothing to check against, so it is first.
func stamps(s Spec, r *Report) error {
	dirs := append([]string{s.ProbeDir}, sortedValues(s.StateDirs)...)
	if s.RepoDir != "" {
		dirs = append(dirs, s.RepoDir)
	}

	for _, dir := range dirs {
		if _, err := os.Stat(dir); err != nil {
			if os.IsNotExist(err) {
				continue // nothing was copied there
			}
			return fault.IO{Op: "look at", Path: dir, Err: err}
		}
		name := filepath.Base(dir)
		if err := sandbox.Guard(sandbox.MapEnv(map[string]string{sandbox.EnvActive: s.ProbeID}), dir); err != nil {
			r.Add("stamp", name, Absent,
				"this directory is not stamped for this probe, so the tools' guard will refuse it: "+err.Error())
			continue
		}
		r.Add("stamp", name, InForce, "stamped "+s.ProbeID)
	}
	return nil
}

// redirection checks that the env file points every store inside the probe.
func redirection(s Spec, r *Report) error {
	vars, err := env.Load(s.EnvFile)
	if err != nil {
		r.Add("redirection", "env file", Absent, "cannot be read: "+err.Error())
		return nil
	}

	for _, key := range []string{env.Active, env.MailmanHome, env.MacmuffinHome, env.CQHome, env.OrcHome,
		env.XDGData, env.XDGState, env.NoNudge, env.Path} {
		value, ok := env.Lookup(vars, key)
		switch {
		case !ok:
			r.Add("redirection", key, Absent, "is not set, so that tool resolves its real store")
		case key == env.NoNudge:
			r.Add("redirection", key, InForce, "mailman and muff will not spawn `cq sync`")
		case key == env.Active:
			r.Add("redirection", key, InForce, "the tripwire the tools' guard keys off")
		case key == env.Path:
			if strings.HasPrefix(value, s.BinDir) {
				r.Add("redirection", key, InForce, "the probe's shims come first")
			} else {
				r.Add("redirection", key, Absent, "does not start with the probe's bin directory")
			}
		case !strings.HasPrefix(value, s.ProbeDir):
			r.Add("redirection", key, Absent, "points outside the probe: "+value)
		default:
			r.Add("redirection", key, InForce, "→ "+strings.TrimPrefix(value, s.ProbeDir+"/"))
		}
	}
	return nil
}

// shims checks the wrappers are installed and can find the real binaries.
func shims(s Spec, r *Report) {
	missing := []string{}
	for _, command := range shim.Commands() {
		if _, err := os.Stat(filepath.Join(s.BinDir, command)); err != nil {
			missing = append(missing, command)
		}
	}
	switch {
	case len(missing) == len(shim.Commands()):
		r.Add("shims", "installed", Absent,
			"no shims at all: `cq sync`, `git push`, and `orc` are refused only by orcprobe itself")
	case len(missing) > 0:
		r.Add("shims", "installed", Warn, "missing for "+strings.Join(missing, ", "))
	default:
		r.Add("shims", "installed", InForce, strings.Join(shim.Commands(), " "))
	}
}

// detachment checks the repo copy has nowhere to send work.
func detachment(s Spec, r *Report) {
	if s.RepoDir == "" {
		return
	}
	config := filepath.Join(s.RepoDir, ".git", "config")
	data, err := os.ReadFile(config)
	if err != nil {
		if os.IsNotExist(err) {
			r.Add("detachment", "git remotes", InForce, "no git config in the copy")
			return
		}
		r.Add("detachment", "git remotes", Skipped, "cannot read the copy's git config")
		return
	}
	if strings.Contains(string(data), "[remote ") {
		r.Add("detachment", "git remotes", Absent, "the repo copy still has a remote; `git push` has a target")
		return
	}
	r.Add("detachment", "git remotes", InForce, "no remotes in the copy")

	if _, err := os.Stat(filepath.Join(s.RepoDir, ".git", "worktrees")); err == nil {
		r.Add("detachment", "git worktrees", Absent, "the copy still registers worktrees, which may point at real checkouts")
	} else {
		r.Add("detachment", "git worktrees", InForce, "none registered")
	}
}

// secrets checks that what holds keys is readable only by its owner, and that
// nothing that looks like a real credential came across.
func secrets(s Spec, r *Report) {
	info, err := os.Stat(s.Identities)
	if err != nil {
		r.Add("credentials", "identities.json", Absent, "missing: this probe has no keys of its own")
		return
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		r.Add("credentials", "identities.json", Absent,
			fmt.Sprintf("is %v; the one file holding plaintext keys must be owner-only", perm))
		return
	}
	r.Add("credentials", "identities.json", InForce, "owner-only, and every key in it is probe-local")
}

// guards runs each tool against an unstamped directory and watches what it
// does. This is the only check that can tell a plan from a build.
func guards(s Spec, r *Report) {
	for _, command := range sortedKeys(s.StateDirs) {
		binary, err := shim.Real(command, s.Path, s.BinDir)
		if err != nil {
			r.Add("stamp guard", command, Skipped, "not installed on this machine, so its guard cannot be measured")
			continue
		}

		refuses, detail := probeGuard(command, binary, s)
		switch {
		case refuses:
			r.Add("stamp guard", command, InForce, "refuses an unstamped store (exit 11)")
		default:
			r.Add("stamp guard", command, Absent,
				"this build does not refuse an unstamped store: "+detail+
					". An absolute path from inside a probe reaches the real one.")
		}
	}
}

// probeGuard is the measurement: a scratch directory nothing has stamped, the
// tripwire set to a probe id that matches nothing, and the tool pointed at it.
// A tool with the guard refuses; one without it opens the directory happily.
//
// It runs in a temporary directory of orcprobe's own, never in a real store and
// never in the probe, so the worst a guardless tool can do here is create a
// store layout somewhere that is removed a moment later.
func probeGuard(command, binary string, s Spec) (bool, string) {
	scratch, err := os.MkdirTemp("", "orcprobe-doctor-")
	if err != nil {
		return false, "could not make a scratch directory: " + err.Error()
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	home, ok := homeVar(command)
	if !ok {
		return false, "orcprobe does not know how to point " + command + " at a store"
	}

	environ := append([]string{}, s.Environ...)
	environ = append(environ,
		sandbox.EnvActive+"=doctor-check-"+s.ProbeID,
		home+"="+scratch,
		// A credential the tool will never get to check: the guard runs before
		// authentication, so a refusal here is the guard and not a login.
		"ORC_USER=doctor", "ORC_KEY=doctor-doctor-doctor-doctor-doctor",
	)

	status, err := spawn.Run(spawn.Request{
		Path:   binary,
		Args:   probeArgs(command),
		Env:    environ,
		Dir:    scratch,
		Stdout: io.Discard,
		Stderr: io.Discard,
	})
	if err != nil {
		return false, "could not be run: " + err.Error()
	}
	if status == escapeStatus {
		return true, ""
	}
	return false, fmt.Sprintf("it exited %d rather than %d", status, escapeStatus)
}

// escapeStatus is the shared exit code for a containment failure. It is spelled
// out here rather than imported from cli to keep the dependency one way.
const escapeStatus = 11

// homeVar is the variable that points a tool at its store.
func homeVar(command string) (string, bool) {
	switch command {
	case "mailman":
		return "MAILMAN_HOME", true
	case "muff":
		return "MACMUFFIN_HOME", true
	case "cq":
		return "CQ_HOME", true
	case "orc":
		return "ORC_HOME", true
	default:
		return "", false
	}
}

// probeArgs is the cheapest command that opens the store and nothing else.
func probeArgs(command string) []string {
	switch command {
	case "mailman":
		return []string{"inbox"}
	case "muff":
		return []string{"pool"}
	case "cq":
		return []string{"status"}
	case "orc":
		// A reading verb, and one the shim allows inside a probe: doctor must
		// measure the guard without asking orc to do anything.
		return []string{"status"}
	default:
		return nil
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

func sortedValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, k := range sortedKeys(m) {
		out = append(out, m[k])
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
