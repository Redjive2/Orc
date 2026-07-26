// Package source is the boundary between cq and the tools it mirrors.
//
// It is deliberately the smallest swappable thing — the same move Mailman's
// plan makes with its identity package. The shipping implementation shells out
// to `mailman` and `muff`; if either grows a Go library API instead, this
// interface is the one file that changes.
//
// cq reads those tools through their CLIs rather than their files, for three
// reasons: their on-disk formats stay private to them, their validated read
// paths are reused rather than reimplemented, and their authentication comes
// along for free. The cost is one addition to each tool — a `--json` output
// mode — because parsing box-drawn tables would be building on a presentation
// format.
package source

import (
	"context"

	"orc/cq/internal/fault"
	"orc/cq/internal/protocol"
)

// Options say what a snapshot should contain.
type Options struct {
	// Machine names the agent machine this snapshot comes from.
	Machine protocol.MachineID
	// Admin includes the whole-Mailman view. Off means the admin panel has
	// nothing to show for this machine.
	Admin bool
	// AdminBodies includes other users' message bodies. Off keeps the panel to
	// metadata, which answers every operational question with a smaller blast
	// radius if the server is ever somewhere less contained.
	AdminBodies bool
	// Library is the repository to mirror for reading, empty for none.
	//
	// It is a path rather than a flag because there is nothing for cq to guess:
	// the agent machine may hold several checkouts, and the one worth reading
	// from a browser is a choice the operator makes once.
	Library string
}

// Validate checks the options are usable.
func (o Options) Validate() error {
	return o.Machine.Validate()
}

// Source is everything cq needs from the machine it mirrors.
//
// Both methods take a context: they shell out to other programs, and a sync
// that hangs because `mailman` is wedged should be cancellable rather than
// permanent.
type Source interface {
	// Snapshot collects the machine's whole state. A snapshot replaces its
	// predecessor wholesale on the server, so it must stand alone.
	Snapshot(ctx context.Context, opts Options) (protocol.Snapshot, error)

	// Apply performs one action the user queued in the browser. It returns nil
	// only if the action definitely happened.
	Apply(ctx context.Context, action protocol.Action) error
}

// ErrUnsupported reports an operation the adapter cannot perform. It is a
// distinct value so a caller can tell "this tool cannot do that" from "that
// went wrong".
var ErrUnsupported = fault.Usage{Reason: "the source does not support this operation"}
