//go:build !windows

package theme

import "os"

// PrepareConsole does nothing here.
//
// A terminal on Unix already decodes UTF-8 and already interprets escape
// sequences; there is no mode to ask for. The function exists so that callers
// do not have to know which platform they are on — the Windows build is where
// the work is, and a caller writing `if runtime.GOOS == "windows"` would be a
// second place that has to be kept in step with it.
func PrepareConsole(streams ...*os.File) { _ = streams }
