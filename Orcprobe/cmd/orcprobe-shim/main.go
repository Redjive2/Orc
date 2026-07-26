// Command orcprobe-shim is the wrapper a probe puts on its PATH.
//
// One binary, hard-linked into a probe's bin/ under every shimmed tool's name.
// On every invocation it:
//
//  1. refuses unless it is inside the probe it belongs to;
//  2. re-asserts the isolation environment, so a subshell that clobbered
//     MAILMAN_HOME is corrected rather than obeyed;
//  3. refuses the handful of commands that would reach the real world;
//  4. records the invocation and its status in the probe's session log;
//  5. runs the real binary and exits with its status.
//
// Identity is deliberately not re-asserted: `orcprobe as bob` sets ORC_USER on
// purpose, and a shim that "corrected" that would break the god-agent's main
// verb. The shim protects isolation, never identity.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"orc/orcprobe/internal/clock"
	"orc/orcprobe/internal/env"
	"orc/orcprobe/internal/fault"
	"orc/orcprobe/internal/probe"
	"orc/orcprobe/internal/shim"
	"orc/orcprobe/internal/snapshot"
	"orc/orcprobe/internal/spawn"
)

// Exit codes, agreeing with orcprobe's own.
const (
	codeOK       = 0
	codeUsage    = 1
	codeNotFound = 2
	codeIO       = 5
	codeEscape   = 11
	codeInternal = 70
)

func main() {
	command := filepath.Base(os.Args[0])
	args := os.Args[1:]

	code, err := run(command, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", command, err)
	}
	os.Exit(code)
}

func run(command string, args []string) (int, error) {
	dir, err := probeDir()
	if err != nil {
		return codeEscape, err
	}

	// The tripwire. A shim copied out of a probe, or run in a shell that never
	// entered one, is inert: it has no idea what state to point at, and
	// guessing would be the escape itself.
	active, ok := os.LookupEnv(probe.EnvActive)
	if !ok {
		return codeEscape, fault.Escape{
			Attempt: "run " + command + " through a probe shim",
			Reason: probe.EnvActive + " is not set, so this is not a probe shell.\n" +
				"  Enter one with `orcprobe shell`, or call the real " + command + " directly.",
		}
	}
	stamp, err := probe.ReadStamp(dir)
	if err != nil {
		return codeEscape, err
	}
	if stamp != active {
		return codeEscape, fault.Escape{
			Attempt: "run " + command + " in " + dir,
			Reason:  "this shim belongs to probe " + stamp + " but the shell is inside " + active,
		}
	}

	// Isolation is restored from the probe's own env file, whatever the current
	// environment says.
	vars, err := env.Load(filepath.Join(dir, probe.EnvFile))
	if err != nil {
		return codeIO, err
	}
	environ := env.Apply(os.Environ(), env.Enforced(vars))

	if err := shim.Check(command, args); err != nil {
		record(dir, command, args, codeEscape)
		return codeEscape, err
	}

	path, err := shim.Real(command, pathFrom(environ), filepath.Join(dir, probe.BinDir))
	if err != nil {
		record(dir, command, args, codeNotFound)
		return codeNotFound, err
	}

	status, err := spawn.Run(spawn.Request{
		Path:   path,
		Args:   args,
		Env:    environ,
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	})
	if err != nil {
		record(dir, command, args, codeInternal)
		return codeInternal, err
	}
	record(dir, command, args, status)
	return status, nil
}

// probeDir works out which probe this shim belongs to from where it sits:
// <probe>/bin/<command>. The environment is not consulted for this, and that is
// the point — a shim that trusted ORCPROBE_DIR could be pointed anywhere.
func probeDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fault.Escape{
			Attempt: "locate the probe this shim belongs to",
			Reason:  "the shim cannot find its own path: " + err.Error(),
		}
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	bin := filepath.Dir(exe)
	dir := filepath.Dir(bin)
	if filepath.Base(bin) != probe.BinDir {
		return "", fault.Escape{
			Attempt: "run a probe shim from " + bin,
			Reason:  "a shim only works from inside a probe's " + probe.BinDir + " directory",
		}
	}
	return dir, nil
}

func pathFrom(environ []string) string {
	for _, entry := range environ {
		if len(entry) > len(env.Path)+1 && entry[:len(env.Path)+1] == env.Path+"=" {
			return entry[len(env.Path)+1:]
		}
	}
	return ""
}

// record appends one line to the probe's session log.
//
// Logging never fails a command. A probe whose log cannot be written is still a
// probe, and refusing to run `mailman inbox` because a log line would not land
// would be the tool getting in the way of the work it exists to allow.
func record(dir, command string, args []string, status int) {
	entry := struct {
		At      string   `json:"at"`
		Command string   `json:"command"`
		Args    []string `json:"args,omitempty"`
		Status  int      `json:"status"`
	}{
		At:      clock.Format(time.Now()),
		Command: command,
		Args:    args,
		Status:  status,
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}

	path := filepath.Join(dir, filepath.FromSlash(probe.SessionLog))
	if err := os.MkdirAll(filepath.Dir(path), snapshot.DirMode); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, snapshot.FileMode)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(line, '\n'))
}
