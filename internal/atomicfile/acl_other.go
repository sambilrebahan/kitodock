//go:build !windows

package atomicfile

// copyAccessRules has nothing to do beyond the POSIX mode bits, which the
// caller already applied to the temporary file.
func copyAccessRules(_, _ string) error { return nil }
