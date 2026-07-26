// Command orcprobe copies the Orc world into a sandbox that cannot reach back.
//
// See Docs/Orcprobe for what a probe is and what it guarantees.
package main

import (
	"os"

	"orc/orcprobe/internal/cli"
	"orc/theme"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		// Not fatal: a home directory is only one of the places state can live,
		// and every root can be named explicitly. The commands that need one
		// will say so.
		home = ""
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	exe, err := os.Executable()
	if err != nil {
		// Only used to find orcprobe-shim beside this binary. Without it the
		// PATH is still searched, and a missing shim is reported rather than
		// fatal.
		exe = ""
	}
	path, _ := os.LookupEnv("PATH")
	shell, _ := os.LookupEnv("SHELL")

	os.Exit(cli.Main(cli.App{
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Env:     os.LookupEnv,
		Environ: os.Environ(),
		Home:    home,
		Cwd:     cwd,
		Exe:     exe,
		Path:    path,
		Shell:   shell,
		Width:   terminalWidth(),
		Colour:  true,
		// Each stream is asked separately: `orcprobe shell > log` writes its
		// banner to a terminal while stdout is a file, and `2> log` is the
		// reverse. One answer for both would either lose the colour where a
		// person is reading or put escape codes in a file.
		Terminal:    theme.IsTerminal(os.Stdout),
		ErrTerminal: theme.IsTerminal(os.Stderr),
	}, os.Args[1:]))
}
