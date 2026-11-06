//go:build windows

// Package sysmetrics samples the machine for the dashboard: CPU, memory and
// the environment drive. Everything talks to Windows directly (PDH, kernel32)
// — no metrics dependency to vendor or audit.
package sysmetrics

import (
	"sync"
	"syscall"
	"unsafe"
)

// Sample is one point-in-time reading. Percentages are 0–100 ints: the UI
// draws bars, not charts, and a decimal of a percent is noise.
type Sample struct {
	CPU       int    `json:"cpu"`
	RAMUsed   uint64 `json:"ramUsed"`
	RAMTotal  uint64 `json:"ramTotal"`
	DiskFree  uint64 `json:"diskFree"`
	DiskTotal uint64 `json:"diskTotal"`
}

var (
	pdh                 = syscall.NewLazyDLL("pdh.dll")
	procOpenQuery       = pdh.NewProc("PdhOpenQuery")
	procAddEnglishCount = pdh.NewProc("PdhAddEnglishCounterW")
	procCollect         = pdh.NewProc("PdhCollectQueryData")
	procFormattedValue  = pdh.NewProc("PdhGetFormattedCounterValue")
	procCloseQuery      = pdh.NewProc("PdhCloseQuery")

	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procGlobalMemoryEx = kernel32.NewProc("GlobalMemoryStatusEx")
	procDiskFreeEx     = kernel32.NewProc("GetDiskFreeSpaceExW")
)

const pdhFmtDouble = 0x00000200

// cpuReader keeps one open PDH query alive: rate counters need two collect
// points, so the query remembers the previous one between calls.
type cpuReader struct {
	mu      sync.Mutex
	query   uintptr
	counter uintptr
	lastCPU int
	primed  bool
}

var cpu = &cpuReader{}

const cpuCounterPath = `\Processor(_Total)\% Processor Time`

type pdhValueDouble struct {
	CStatus     uint32
	DoubleValue float64
}

func (r *cpuReader) percent() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.query == 0 {
		var query uintptr
		if rc, _, _ := procOpenQuery.Call(0, 0, uintptr(unsafe.Pointer(&query))); rc != 0 {
			return 0
		}
		path16, err := syscall.UTF16PtrFromString(cpuCounterPath)
		if err != nil {
			return 0
		}
		var counter uintptr
		if rc, _, _ := procAddEnglishCount.Call(query, uintptr(unsafe.Pointer(path16)), 0, uintptr(unsafe.Pointer(&counter))); rc != 0 {
			procCloseQuery.Call(query)
			return 0
		}
		r.query, r.counter = query, counter
	}
	// First collect after open has no previous point and returns
	// PDH_INVALID_DATA; the second onwards yields the rate.
	if rc, _, _ := procCollect.Call(r.query); rc != 0 {
		return r.lastCPU
	}
	var value pdhValueDouble
	if rc, _, _ := procFormattedValue.Call(r.counter, pdhFmtDouble, 0, uintptr(unsafe.Pointer(&value))); rc != 0 || value.CStatus != 0 {
		return r.lastCPU
	}
	if !r.primed {
		r.primed = true
		return r.lastCPU
	}
	percent := int(value.DoubleValue + 0.5)
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	r.lastCPU = percent
	return percent
}

type memoryStatus struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// Collect takes one sample of everything.
func Collect() Sample {
	sample := Sample{CPU: cpu.percent()}

	var mem memoryStatus
	mem.Length = uint32(unsafe.Sizeof(mem))
	if ok, _, _ := procGlobalMemoryEx.Call(uintptr(unsafe.Pointer(&mem))); ok != 0 {
		sample.RAMTotal = mem.TotalPhys
		sample.RAMUsed = mem.TotalPhys - mem.AvailPhys
	}

	free, total := diskSpace("C:\\")
	sample.DiskFree, sample.DiskTotal = free, total
	return sample
}

// diskSpace reports free and total bytes of the volume holding path.
func diskSpace(path string) (free, total uint64) {
	path16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0
	}
	var freeBytes, totalBytes, totalFree int64
	if ok, _, _ := procDiskFreeEx.Call(
		uintptr(unsafe.Pointer(path16)),
		uintptr(unsafe.Pointer(&freeBytes)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFree)),
	); ok != 0 {
		return uint64(freeBytes), uint64(totalBytes)
	}
	return 0, 0
}
