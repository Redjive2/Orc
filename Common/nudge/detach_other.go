//go:build !unix

package nudge

import "os/exec"

// detach does nothing where sessions do not exist. The nudge still runs; it is
// only more easily interrupted, and an interrupted nudge is a stale website
// rather than a broken one.
func detach(*exec.Cmd) {}
