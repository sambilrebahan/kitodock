//go:build !windows

package atomicfile

import "os"

// replace relies on rename(2) semantics: POSIX rename atomically replaces an
// existing target on the same filesystem, which a same-directory temporary
// file guarantees.
func replace(tmp, target string) error {
	return os.Rename(tmp, target)
}
