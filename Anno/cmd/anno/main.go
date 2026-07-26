// Command anno indexes, reads, and writes annotated regions of files.
//
// See Docs/Anno for the annotation syntax and the reasoning behind it.
package main

import (
	"fmt"
	"os"

	"orc/anno/internal/cli"
	"orc/anno/internal/style"
	"orc/theme"
)

func main() {
	// Colour is decided per stream: a piped index stays clean while the errors
	// beside it on a terminal stay legible. Both decisions come from the shared
	// scheme, so `anno` and every other Orc tool answer the same environment in
	// the same way.
	out, err := theme.ForStream(os.Stdout, os.LookupEnv)
	if err != nil {
		// A misspelled theme is worth stopping for. Falling back to the default
		// would leave the operator believing the setting does nothing.
		_, _ = fmt.Fprintf(os.Stderr, "anno: %v\n", err)
		os.Exit(cli.CodeUsage)
	}
	errs, err := theme.ForStream(os.Stderr, os.LookupEnv)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "anno: %v\n", err)
		os.Exit(cli.CodeUsage)
	}

	os.Exit(cli.Main(cli.App{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Out:    style.New(out.Palette),
		Err:    style.New(errs.Palette),
	}, os.Args[1:]))
}
