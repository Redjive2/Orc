// Package proc is the thin layer over the operating system's processes.
//
// It is internal/pty's sibling: that one is the thin layer over terminals, this
// one is the thin layer over the things that run in them. Both exist so that the
// packages above can be written once, and so that the places where the platforms
// genuinely differ are few enough to read in one sitting.
//
// The differences are real and this package does not paper over them. Unix has
// process groups and signals, so a supervisor can ask a session and everything
// it started to leave, and wait to see whether it did. Windows has neither: a
// detached process cannot be sent a polite signal at all, and killing a tree
// needs a job object rather than a negative pid. What that costs is written down
// on each function that pays it, because a caller that thinks it asked politely
// and did not is a caller that will be surprised.
//
// Nothing here logs, and nothing here decides policy. How long to wait, whether
// to retry, and what to tell the operator all belong above.
package proc
