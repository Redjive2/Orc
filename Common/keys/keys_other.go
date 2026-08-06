//go:build !darwin && !linux && !windows

package keys

import "os"

// Everywhere else, there are no keys.
//
// A build for a platform this has no terminal handling for still compiles and
// still runs the thing the keys were a shortcut for — the cycles keep their own
// time either way. Returning ErrNotATerminal rather than failing to build is what
// makes that true: the caller already has to handle a redirected stdin, and this
// is the same case.
func cbreak(*os.File) (func() error, error) { return nil, ErrNotATerminal }
