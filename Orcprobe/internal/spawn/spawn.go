// Package spawn is the only package in Orcprobe that starts another process.
//
// That is the point of it. Rule 1 of this tool is that no agent is ever brought
// to life inside a probe, and the way that is kept is structural rather than
// careful: every exec in the tree goes through here, so "does orcprobe ever
// start an agent?" is answered by reading one file rather than by trusting a
// review.
//
// Two things are started, ever: the operator's own shell, and a command the
// operator named. Both run inside a probe, with a probe's environment. Neither
// is chosen by orcprobe.
package spawn

import (
	"errors"
	"io"
	"os/exec"

	"orc/orcprobe/internal/fault"
)

// Request is one process to run.
type Request struct {
	// Path is the resolved binary. It is a path, never a name: resolution
	// happens in the caller, against a PATH the caller composed, so this package
	// never consults an ambient one.
	Path string
	Args []string
	// Env is the complete environment, already composed. It replaces the
	// parent's rather than extending it, so nothing leaks in unnoticed.
	Env []string
	Dir string

	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Run starts a process, waits for it, and returns its exit code.
//
// A non-zero exit is not an error here: a command run inside a probe failing is
// an ordinary outcome that the operator wants to see the status of, not a
// failure of orcprobe. Only a process that could not be started at all is.
func Run(r Request) (int, error) {
	if r.Path == "" {
		return 0, fault.Internal{Where: "spawn.Run", Detail: "no binary given"}
	}

	cmd := exec.Command(r.Path, r.Args...)
	cmd.Env = r.Env
	cmd.Dir = r.Dir
	cmd.Stdin = r.Stdin
	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr

	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), nil
		}
		return 0, fault.IO{Op: "run", Path: r.Path, Err: err}
	}
	return 0, nil
}
