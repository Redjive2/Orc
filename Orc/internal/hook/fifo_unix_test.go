//go:build unix

package hook_test

import "syscall"

func makeFIFO(path string) error { return syscall.Mkfifo(path, 0o600) }
