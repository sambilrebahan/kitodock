//go:build !windows

package runtimeinventory

import "errors"

// Metadata mirrors the Windows struct; off Windows it stays zero-valued.
type Metadata struct {
	FullVersion string
	Prerelease  bool
}

var errUnavailable = errors.New("version resources are a Windows concept")

// Read is Windows-only; elsewhere an installed runtime cannot be
// interrogated and callers treat that as "unknown".
func Read(string) (Metadata, error) {
	return Metadata{}, errUnavailable
}
