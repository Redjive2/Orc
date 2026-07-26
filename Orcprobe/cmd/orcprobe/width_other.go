//go:build !unix

package main

import (
	"os"
	"strconv"
)

// terminalWidth reads COLUMNS where there is no ioctl to ask. Returning zero
// leaves the renderer on its default width, which is a readable table rather
// than a broken one.
func terminalWidth() int {
	if v, ok := os.LookupEnv("COLUMNS"); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 0
}
