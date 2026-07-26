//go:build !windows

package source_test

import "syscall"

func makeFIFO(path string) error { return syscall.Mkfifo(path, 0o600) }
