//go:build !windows

package sysmetrics

// Sample mirrors the Windows struct.
type Sample struct {
	CPU       int    `json:"cpu"`
	RAMUsed   uint64 `json:"ramUsed"`
	RAMTotal  uint64 `json:"ramTotal"`
	DiskFree  uint64 `json:"diskFree"`
	DiskTotal uint64 `json:"diskTotal"`
}

// Collect is Windows-only in Phase 1; other platforms get zeroes.
func Collect() Sample { return Sample{} }
