// Package pty opens pseudo-terminals and puts a terminal into raw mode.
//
// It exists because Orc owns the Claude session it manages, and a Claude session
// is an interactive terminal program. A pipe would work for a headless run and
// nothing else: the TUI needs a terminal to draw to, `orc attach --direct` needs a
// stream it can hand to a real one, and both need window size to travel.
//
// Everything here is stdlib, on every platform: the ioctl numbers unix names, and
// the console calls Windows names instead. A pty costs a few build-tagged files
// rather than a dependency, and this tree has none.
//
// The platforms do not agree on what a pseudo-terminal *is*, so this file holds
// only what they share and each has its own. Unix hands out a master and a slave
// and lets a child adopt the slave as its controlling terminal. Windows has a
// pseudoconsole, which is a handle plus two pipes, and a child can only be given
// one at creation — through an attribute the standard library's os/exec cannot
// set. That difference is why pty_windows.go refuses to open one rather than
// pretending to.
//
// Nothing in this package logs, and nothing in it is safe to call twice on the
// same file without closing: it is the thin layer over the kernel, and every
// decision about restarts, buffering, and who may attach belongs above it in
// internal/session.
package pty

// WinSize is a terminal's size, in the kernel's own layout.
type WinSize struct {
	Rows uint16
	Cols uint16
	X    uint16
	Y    uint16
}

// Sane is a size to start a session at when nobody has said otherwise.
//
// A pty with a zero size is legal and useless: a TUI asked to draw into no columns
// draws nothing, and the first attach would be a blank screen that looked like a
// hung agent. So an unattended session gets an ordinary terminal's size, and the
// first attacher corrects it.
func Sane() WinSize { return WinSize{Rows: 40, Cols: 120} }
