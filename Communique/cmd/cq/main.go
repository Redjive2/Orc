// Command cq is Communiqué: my window into Orc.
//
// See Docs/Communique for what it is, and Claude/Docs/Communique/Plan.md for
// why it is shaped this way.
package main

import (
	"fmt"
	"os"

	"orc/cq/internal/cli"
	"orc/cq/internal/style"
	"orc/theme"
)

func main() {
	// Windows first: a console there decodes bytes with the machine's OEM code
	// page and does not interpret escape sequences until asked, so without this
	// the box rules arrive as mojibake and the colour as literal noise. It does
	// nothing anywhere else.
	theme.PrepareConsole(os.Stdout, os.Stderr)

	// Colour is decided per stream and by the same settings as every other Orc
	// tool, so a piped report stays clean while the errors beside it on a
	// terminal stay legible. An unreadable ORC_THEME is reported rather than
	// ignored: a typo that silently fell back would look like a setting that
	// does not work.
	out, outErr := theme.ForStream(os.Stdout, theme.OSLook)
	errStream, errErr := theme.ForStream(os.Stderr, theme.OSLook)
	if outErr != nil {
		fmt.Fprintf(os.Stderr, "cq: %v\n", outErr)
	} else if errErr != nil {
		fmt.Fprintf(os.Stderr, "cq: %v\n", errErr)
	}

	os.Exit(cli.Main(cli.App{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Env:    os.LookupEnv,
		Out:    style.New(out.Palette),
		Err:    style.New(errStream.Palette),
	}, os.Args[1:]))
}
