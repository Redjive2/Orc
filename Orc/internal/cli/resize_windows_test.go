package cli_test

import "errors"

// raiseResize has nothing to raise here: Windows has no window-change signal, so
// a console program polls instead. The test that calls this skips.
func raiseResize() error { return errors.ErrUnsupported }
