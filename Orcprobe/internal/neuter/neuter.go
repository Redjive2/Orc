// Package neuter scrubs liveness out of a copied world.
//
// A probe keeps the history and loses the life. Everything here answers one
// question — "does this make a probe look like somewhere agents are currently
// working?" — and removes what does: claimed tasks, collaborators, worktree
// bindings, undelivered notifications, a sync cursor, hooks that point outside.
// Mail, read state, task history, and everything else stays exactly as it was.
// That is data, not liveness.
//
// Two rules shape all of it.
//
// **Nothing is rewritten; everything is appended.** Macmuffin's journals are
// append-only, and a probe that edited history would be a probe that lies about
// where it came from. So a claim is released by appending a release, exactly as
// an agent releasing it would have — which means the neutered probe is not a
// special state some tool has to know about. It is an ordinary one.
//
// **Every change is reported.** The caller writes each one into the probe's
// manifest, so a probe can always say what was done to it on the way in. A
// scrub nobody can audit is indistinguishable from a probe that quietly lost
// something.
package neuter

import (
	"orc/orcprobe/internal/clock"
	"orc/orcprobe/internal/fault"
)

// Spec says what to scrub and where the probe keeps it. Every path is inside
// the probe: this package is never handed a real one, and could not do anything
// with it if it were.
type Spec struct {
	// MacmuffinDir, CQDir, and ClaudeDir are the copied stores. An empty path
	// means that tool had nothing to copy, which is not an error.
	MacmuffinDir string
	CQDir        string
	ClaudeDir    string
	// OrcDir is the copied fleet store. Orc is the only tool whose state
	// includes a claim about a running process, which is why it needs a scrub
	// of its own.
	OrcDir string

	// BinDir is the probe's shim directory, which hooks are pointed at.
	BinDir string
	// ProbeDir is the probe's root, used to tell "inside" from "outside".
	ProbeDir string

	Clock clock.Clock
}

// Change is one thing that was done, in the manifest's vocabulary.
type Change struct {
	// Act is a probe manifest act: drop, note.
	Act    string
	What   string
	Detail string
}

// Unreleased is a task whose owner the scrub could not drop, and who holds it.
//
// It exists because the honest answer to "did you scrub this?" is sometimes no.
// A probe that reported a clean scrub while a task still showed an owner would
// be lying in the one direction this tool must never lie in.
type Unreleased struct {
	Task  string
	Owner string
}

// Report is everything the scrub did.
type Report struct {
	// Released names the tasks whose owner was dropped. It is empty until
	// Macmuffin gains a release op; see macmuffin.go.
	Released []string
	// Unreleased names the tasks that still show an owner, and why.
	Unreleased []Unreleased
	// Collaborators counts the memberships removed.
	Collaborators int
	// Worktrees counts the bindings removed.
	Worktrees int
	// Outbox counts the undelivered notifications removed.
	Outbox int
	// Hooks names the hook commands that were disabled.
	Hooks []string
	// Sessions and Sockets count the live-session claims taken out of Orc's
	// store: a probe must never look like somewhere an agent is running.
	Sessions int
	Sockets  int
	// Changes is the full list, for the manifest.
	Changes []Change
}

// Acts a change can carry, matching the probe manifest's vocabulary.
const (
	ActDrop = "drop"
	// ActDefer marks a guarantee the scrub could not keep, so it reads in the
	// manifest exactly like the other promises this build does not make.
	ActDefer = "defer"
	ActNote  = "note"
)

// Run scrubs a probe.
//
// It is one pass over three tools, and it stops at the first thing it cannot
// classify rather than doing most of the job. A partly-neutered probe would
// claim in its record to be inert while something in it was still live, and
// that is the one lie this tool must not tell.
func Run(s Spec) (Report, error) {
	var rep Report
	if s.Clock == nil {
		return rep, fault.Internal{Where: "neuter.Run", Detail: "no clock was given"}
	}

	if s.MacmuffinDir != "" {
		if err := macmuffin(s, &rep); err != nil {
			return rep, err
		}
	}
	if s.CQDir != "" {
		if err := communique(s, &rep); err != nil {
			return rep, err
		}
	}
	if s.OrcDir != "" {
		if err := orcState(s, &rep); err != nil {
			return rep, err
		}
	}
	if s.ClaudeDir != "" {
		if err := claude(s, &rep); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

func (r *Report) add(act, what, detail string) {
	r.Changes = append(r.Changes, Change{Act: act, What: what, Detail: detail})
}
