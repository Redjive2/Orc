// Package shim decides what a probe refuses to run, and installs the wrappers
// that enforce it.
//
// A shim is one binary hard-linked under every tool's name into the probe's
// bin/, which the probe's PATH puts first. On every invocation it re-asserts the
// isolation environment, refuses the handful of commands that would reach the
// real world, records what ran, and execs the real binary. Nothing stays
// resident.
//
// The refusals are deliberately few. A probe exists to be used, and a wall made
// of a hundred special cases is a wall nobody trusts and everybody works around.
// These are the commands that leave the machine or start a life of their own:
//
//	cq sync          the one path to the real server
//	cq serve         allowed, but only bound to loopback
//	git push/fetch/pull/clone-over-network   the other way out
//	orc employ/…    orc brings agents to life; its reading verbs are allowed
//
// Everything else — mailman, muff, anno, dock, and the rest of git — runs
// untouched, against probe state, which is the point of the exercise.
package shim

import (
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"orc/orcprobe/internal/fault"
	"orc/orcprobe/internal/snapshot"
)

// Commands are the binaries a probe shims. Shimming a tool that is not
// installed costs nothing — the wrapper simply reports that the real binary is
// missing, which is a better message than "command not found" anyway.
func Commands() []string {
	return []string{"mailman", "muff", "cq", "anno", "dock", "orc", "git"}
}

// Check reports whether this invocation is refused.
//
// It takes the command by name rather than by path so it is a pure function of
// the command line, testable without a filesystem, and identical whether it is
// called by the shim or by orcprobe itself when it validates a `--` command.
func Check(command string, args []string) error {
	switch filepath.Base(command) {
	case "cq":
		return checkCQ(args)
	case "git":
		return checkGit(args)
	case "orc":
		return checkOrc(args)
	default:
		return nil
	}
}

func checkCQ(args []string) error {
	switch first(args) {
	case "sync":
		return fault.Escape{
			Attempt: "cq sync",
			Target:  "the real Communiqué server",
			Reason: "sync is the only thing that crosses out of an Orc machine, so a probe never runs it.\n" +
				"  The probe's cursor was reset and its queue dropped; there is nothing here to send.",
		}
	case "serve":
		addr, ok := flagValue(args, "--addr")
		if !ok {
			return nil // the default binding is local
		}
		local, err := loopback(addr)
		if err != nil {
			return fault.Escape{Attempt: "cq serve --addr " + addr, Reason: err.Error()}
		}
		if !local {
			return fault.Escape{
				Attempt: "cq serve --addr " + addr,
				Reason: "a probe may serve its own copy to this machine, but not to the network.\n" +
					"  Bind 127.0.0.1 or localhost instead.",
			}
		}
		return nil
	default:
		return nil
	}
}

// orcReading is what a probe may ask Orc: the museum verbs.
//
// Everything here reads and reports. They are safe in a probe for the same
// reason `mailman inbox` is — the state they read is the probe's copy, already
// redirected and stamped — and they are the verbs that make a probe worth
// having, since "what did this fleet look like" is the question it exists to
// answer.
var orcReading = map[string]bool{
	"status":        true,
	"introspect":    true,
	"check-control": true,
	"verify":        true,
	"doctor":        true,
	"env":           true,
	"help":          true,
	"-h":            true,
	"--help":        true,
}

// checkOrc narrows what was once a blanket refusal.
//
// Orcprobe refused `orc` wholesale for four milestones, and its own plan said
// why: "It does not exist yet, so there is no list of spawn verbs to
// enumerate." Now there is, and the list runs the other way — an allow-list of
// verbs that only read, and a refusal for everything else.
//
// The direction matters. A deny-list would let a verb Orc grows next week into
// a probe unexamined, and the one thing that must never happen here is a probe
// bringing an agent to life. So an unknown verb is refused, and the refusal
// names what is allowed rather than what is not.
func checkOrc(args []string) error {
	verb := first(args)
	if verb == "" || orcReading[verb] {
		return nil
	}

	allowed := make([]string, 0, len(orcReading))
	for name := range orcReading {
		if strings.HasPrefix(name, "-") {
			continue
		}
		allowed = append(allowed, name)
	}
	sort.Strings(allowed)

	return fault.Escape{
		Attempt: "orc " + verb,
		Reason: "orc employs agents and runs Claude sessions, and a probe never brings one to life.\n" +
			"  Reading is fine: " + strings.Join(allowed, ", ") + ".\n" +
			"  Anything that populates, attaches, or wakes an identity is refused — rule 1 of the tool.",
	}
}

func checkGit(args []string) error {
	sub := first(args)
	switch sub {
	case "push", "fetch", "pull":
		return fault.Escape{
			Attempt: "git " + sub,
			Reason: "a probe's repo copy has no remotes, no credentials, and nowhere to send work.\n" +
				"  Take a patch out of the probe by hand instead.",
		}
	case "clone":
		for _, arg := range args[1:] {
			if remoteURL(arg) {
				return fault.Escape{
					Attempt: "git clone " + arg,
					Reason:  "cloning over the network would pull the outside world into a probe.",
				}
			}
		}
		return nil
	default:
		return nil
	}
}

// remoteURL reports whether an argument names something off this machine. It is
// deliberately generous about what counts: a false positive costs one refused
// clone, and a false negative is a probe with a live network handle.
func remoteURL(arg string) bool {
	if strings.Contains(arg, "://") {
		return true
	}
	// scp-style: user@host:path, but not a Windows-style drive or a local path.
	if at := strings.Index(arg, "@"); at > 0 {
		if colon := strings.Index(arg[at:], ":"); colon > 0 {
			return true
		}
	}
	return false
}

// loopback reports whether an address binds only to this machine.
func loopback(addr string) (bool, error) {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.TrimSpace(host)
	switch host {
	case "", "localhost":
		// An empty host in host:port form means every interface.
		return host == "localhost", nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false, fault.Usage{Reason: "cannot tell whether " + addr + " is local; bind 127.0.0.1 explicitly"}
	}
	return ip.IsLoopback(), nil
}

// flagValue finds --name <value> or --name=<value>.
func flagValue(args []string, name string) (string, bool) {
	for i, arg := range args {
		switch {
		case arg == name && i+1 < len(args):
			return args[i+1], true
		case strings.HasPrefix(arg, name+"="):
			return strings.TrimPrefix(arg, name+"="), true
		}
	}
	return "", false
}

func first(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

// Install links the shim binary into a probe's bin directory under every
// shimmed name.
//
// Hard links are tried first: they cost nothing and keep every name pointing at
// exactly the bytes that were verified at install time. A symlink is the
// fallback for a probe on a different filesystem from the binary, and a copy is
// the last resort. A copy is the weakest of the three — it goes stale when
// orcprobe is rebuilt — so it is reported, not silently chosen.
func Install(binDir, shimPath string) ([]string, error) {
	if err := os.MkdirAll(binDir, snapshot.DirMode); err != nil {
		return nil, fault.IO{Op: "create", Path: binDir, Err: err}
	}

	var copied []string
	for _, command := range Commands() {
		target := filepath.Join(binDir, command)
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return nil, fault.IO{Op: "replace", Path: target, Err: err}
		}
		if err := os.Link(shimPath, target); err == nil {
			continue
		}
		if err := os.Symlink(shimPath, target); err == nil {
			continue
		}
		if err := copyExecutable(target, shimPath); err != nil {
			return nil, err
		}
		copied = append(copied, command)
	}
	return copied, nil
}

func copyExecutable(dst, src string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fault.IO{Op: "read", Path: src, Err: err}
	}
	if err := os.WriteFile(dst, data, snapshot.ExecMode); err != nil {
		return fault.IO{Op: "write", Path: dst, Err: err}
	}
	return nil
}

// Find locates the shim binary: beside the running orcprobe first, then on the
// PATH. A missing shim is not a failure to create a probe — the environment
// layer still isolates it — but it is a guard that is absent, and the caller is
// expected to say so out loud.
func Find(exe string, path string) (string, bool) {
	if exe != "" {
		candidate := filepath.Join(filepath.Dir(exe), "orcprobe-shim")
		if executable(candidate) {
			return candidate, true
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, "orcprobe-shim")
		if executable(candidate) {
			return candidate, true
		}
	}
	return "", false
}

// Real resolves the binary a shim should exec: the first match on PATH that is
// not inside the probe's own bin directory, so a shim never finds itself.
func Real(command, path, binDir string) (string, error) {
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		if binDir != "" && sameDir(dir, binDir) {
			continue
		}
		candidate := filepath.Join(dir, command)
		if executable(candidate) {
			return candidate, nil
		}
	}
	return "", fault.NotFound{
		Target: command,
		Near:   []string{"the probe shims " + command + ", but no real " + command + " is on the PATH outside the probe"},
	}
}

func sameDir(a, b string) bool {
	ca, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	cb, err := filepath.Abs(b)
	if err != nil {
		return false
	}
	return filepath.Clean(ca) == filepath.Clean(cb)
}

func executable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}
