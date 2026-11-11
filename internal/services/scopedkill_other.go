//go:build !windows

package services

// killImageUnderRoot on non-Windows platforms: no-op (the services
// manager is Windows-only in this phase).
func killImageUnderRoot(root, base string) error {
	return nil
}
