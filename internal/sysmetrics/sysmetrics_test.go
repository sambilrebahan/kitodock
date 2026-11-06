package sysmetrics

import (
	"runtime"
	"testing"
)

func TestSampleIsSane(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native counters need Windows")
	}
	// First call primes the CPU counter; take two.
	Collect()
	sample := Collect()

	if sample.RAMTotal == 0 {
		t.Error("total memory reported as zero")
	}
	if sample.RAMUsed > sample.RAMTotal {
		t.Errorf("used memory %d exceeds total %d", sample.RAMUsed, sample.RAMTotal)
	}
	if sample.DiskTotal == 0 {
		t.Error("disk total reported as zero")
	}
	if sample.DiskFree > sample.DiskTotal {
		t.Errorf("free disk %d exceeds total %d", sample.DiskFree, sample.DiskTotal)
	}
	if sample.CPU < 0 || sample.CPU > 100 {
		t.Errorf("CPU percent %d out of range", sample.CPU)
	}
}
