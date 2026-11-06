//go:build !windows

package credentials

import "errors"

// Kitodock is Windows-first; on other platforms sealing is unavailable and
// callers must treat that as an error rather than storing plaintext.
func protect([]byte) ([]byte, error) { return nil, errors.New("DPAPI sealing is Windows-only") }

func unprotect([]byte) ([]byte, error) {
	return nil, errors.New("DPAPI sealing is Windows-only")
}
