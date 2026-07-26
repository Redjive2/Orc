package hook_test

import "errors"

// makeFIFO exists so this test package compiles on Windows, which has no
// mkfifo. The caller already treats a failure as "there is nothing to test".
//
// A runtime guard beside the call would not be enough on its own: a symbol used
// in a package has to exist at compile time whether its branch runs or not.
func makeFIFO(string) error { return errors.ErrUnsupported }
