// Command dock reads documentation without reading all of it.
//
// See Docs/Dock for the section syntax and the reasoning behind it.
package main

import (
	"fmt"
	"os"

	"orc/common/fault"
	"orc/dock/internal/anno"
	"orc/dock/internal/cli"
	"orc/dock/internal/style"
	"orc/theme"
)

func main() {
	// Colour is decided per stream: a piped index stays clean while the errors
	// beside it on a terminal stay legible. Both decisions come from the shared
	// scheme, so `dock` and every other Orc tool answer the same environment in
	// the same way — including ORC_AGENT, which forces plain output because an
	// agent's input is another program and escape sequences in it are
	// corruption rather than decoration.
	out, err := theme.ForStream(os.Stdout, os.LookupEnv)
	if err != nil {
		// A misspelled theme is worth stopping for. Falling back to the default
		// would leave the operator believing the setting does nothing.
		_, _ = fmt.Fprintf(os.Stderr, "dock: %v\n", err)
		os.Exit(fault.CodeUsage)
	}
	errs, err := theme.ForStream(os.Stderr, os.LookupEnv)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "dock: %v\n", err)
		os.Exit(fault.CodeUsage)
	}

	app := cli.App{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Out:    style.New(out.Palette),
		Err:    style.New(errs.Palette),
		Stat:   cli.Regular,
		Anno:   anno.New(),
	}
	os.Exit(app.Main(os.Args[1:]))
}
