//go:build darwin

package keys

import "syscall"

// The BSD spellings. TIOCSETA applies immediately; the draining variants wait for
// output to flush, which is a stall at exactly the moment somebody is trying to
// get their terminal back.
func getAttr(fd uintptr) (syscall.Termios, error) { return getAttrWith(fd, syscall.TIOCGETA) }
func setAttr(fd uintptr, t syscall.Termios) error { return setAttrWith(fd, syscall.TIOCSETA, t) }
