//go:build !unix

package store

import "io/fs"

// Without unix file ownership there is no way to ask whose the store is, so the
// answer is "cannot tell" — and the caller treats that as a refusal rather than as
// permission. A fallback that assumed the best on a platform it could not check
// would be a fallback nobody could reason about.
func ownerUID(fs.FileInfo) (int, bool) { return 0, false }
