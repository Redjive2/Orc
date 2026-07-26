//go:build windows

package atomic_test

import "errors"

// makeFIFO exists so the test file compiles here. Windows has no mkfifo, and
// the caller already treats a failure as "there is nothing to test".
//
// The runtime guard beside the call is not enough on its own: a symbol used in
// a package has to exist at compile time whether the branch runs or not, and
// without this the whole test package fails to build on Windows.
func makeFIFO(string) error { return errors.ErrUnsupported }
