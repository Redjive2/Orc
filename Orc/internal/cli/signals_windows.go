package cli

import "os"

// attachSignals are what an attached console listens for.
//
// Windows has no window-change signal at all: a console program that wants to
// follow a resize polls for it. So this list is only the ones that mean leave,
// and a console resized while attached keeps the size it was attached at until
// the operator detaches and comes back.
//
// That is a real limitation rather than an oversight, and it is small: the
// session is on the machine being attached *to*, and its size is corrected on
// every attach.
func attachSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

// resized reports whether a signal means the terminal changed size. Nothing here
// does, so leaving is the only thing a signal can mean.
func resized(os.Signal) bool { return false }
