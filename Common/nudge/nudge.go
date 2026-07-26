// Package nudge tells Communiqué that something changed.
//
// Mailman and Macmuffin own the state; cq only mirrors it, and it has no way to
// know a message arrived until someone tells it. This is that telling: one
// fire-and-forget call after any command that changed something, so the website
// is current a second later rather than whenever the next timer fires.
//
// Four rules, and each exists because the alternative makes a mail tool worse:
//
//   - It never blocks. The child is started and abandoned; the caller exits.
//   - It never fails the caller. Mail was already delivered by the time this
//     runs. A mirror that cannot be reached is not a reason to tell an agent
//     its message failed, so Fire reports nothing the caller must handle.
//   - It is silent. Nothing is written to either stream. An agent parsing
//     Mailman's output must not have to know cq exists.
//   - It is never the only path. A dropped nudge is invisible by design, which
//     is why `cq sync --watch` exists as the backstop.
package nudge

import (
	"os"
	"os/exec"
	"strings"
)

// Server is the variable that says a mirror exists on this machine. It is the
// one cq sync itself needs, so a machine that can nudge is exactly a machine
// that is set up to sync.
const Server = "CQ_SERVER"

// Suppress is set by cq while it applies a queued action.
//
// Applying an action means running `mailman send`, which would nudge, which
// would sync, which would apply — cq's lock and coalescing bound that, but the
// work still doubles for no gain. The tool that asked for the change already
// knows about it.
const Suppress = "ORC_NO_NUDGE"

// Binary is the command to run. It is a variable only so a machine that
// installs cq under another name can say so.
const Binary = "CQ_BIN"

// Mirrored names the account cq mirrors, OrcUser names the account the calling
// tool authenticated as, and MirrorKey is the mirrored account's own credential.
//
// The three together answer one question: is this change worth a sync?
//
// A nudge inherits the environment of whatever changed something, and on the
// agent machine that is usually an agent running under its own name. If cq has
// its own credential it reads the operator's mailbox regardless of who triggered
// it, and every agent's change is worth announcing — which is the point, since
// agents are what send the operator mail. Without that credential cq can only
// read whoever the environment names, so a change by anyone else is not the
// operator's mail and syncing on it would publish an agent's inbox as theirs.
const (
	Mirrored  = "CQ_USER"
	MirrorKey = "CQ_KEY"
	OrcUser   = "ORC_USER"
)

// Nudger holds the three things this package touches, so the decision to fire
// can be tested without spawning anything.
//
// The zero value is the real one: every field falls back to the operating
// system when it is nil.
type Nudger struct {
	Look  func(string) (string, bool)
	Find  func(string) (string, error)
	Start func(path string, args []string) error
}

// Fire asks the mirror to catch up, and reports whether it started anything.
//
// The bool is for tests and for a caller that wants to log it. Nothing about a
// false is an error: not being mirrored is the normal case.
func (n Nudger) Fire() bool {
	look, find, start := n.Look, n.Find, n.Start
	if look == nil {
		look = os.LookupEnv
	}
	if find == nil {
		find = exec.LookPath
	}
	if start == nil {
		start = spawn
	}

	if v, ok := look(Suppress); ok && v != "" && v != "0" {
		return false
	}
	// No server configured means no mirror on this machine, which is most
	// machines. Checking this first is what keeps the common case to one
	// environment lookup and no process work at all.
	if v, ok := look(Server); !ok || v == "" {
		return false
	}

	if !worthSyncing(look) {
		return false
	}

	name := "cq"
	if v, ok := look(Binary); ok && v != "" {
		name = v
	}

	path, err := find(name)
	if err != nil {
		// cq is not installed here. That is a configuration the operator chose,
		// not a fault to report in the middle of someone else's command.
		return false
	}
	return start(path, []string{"sync", "--nudge"}) == nil
}

// worthSyncing reports whether a change by this caller is a change to the
// mirrored account's own state. See the constants above for why.
func worthSyncing(look func(string) (string, bool)) bool {
	if key, ok := look(MirrorKey); ok && key != "" {
		return true // cq authenticates as the mirrored account itself
	}
	mine, ok := look(Mirrored)
	if !ok || mine == "" {
		return true // nothing to compare against; let cq decide
	}
	who, ok := look(OrcUser)
	if !ok || who == "" {
		return true // likewise
	}
	return strings.EqualFold(who, mine)
}

// After fires a nudge if this machine mirrors. It is what a command calls once
// it has successfully changed something.
func After() { Nudger{}.Fire() }

// spawn starts the child and walks away.
//
// Its streams go to the null device rather than being inherited: a nudge that
// wrote to standard output would corrupt `mailman inbox --json`, and one that
// held the stream open would hang a shell waiting on the pipe.
func spawn(path string, args []string) error {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	// The parent does not wait, so the child must not hold the only handle the
	// parent needs to close.
	defer func() { _ = null.Close() }()

	cmd := exec.Command(path, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	// The child is told not to nudge in turn. Nothing it runs should come back
	// round to here, and saying so costs one string.
	cmd.Env = append(os.Environ(), Suppress+"=1")
	detach(cmd)

	if err := cmd.Start(); err != nil {
		return err
	}
	// Deliberately no Wait. The child outlives this process and is reaped by
	// init; waiting is the one thing this function must not do.
	return nil
}
