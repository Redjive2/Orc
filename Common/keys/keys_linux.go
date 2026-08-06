//go:build linux

package keys

import "syscall"

// The Linux spellings. TCSETS applies immediately, as TIOCSETA does on BSD.
func getAttr(fd uintptr) (syscall.Termios, error) { return getAttrWith(fd, syscall.TCGETS) }
func setAttr(fd uintptr, t syscall.Termios) error { return setAttrWith(fd, syscall.TCSETS, t) }
