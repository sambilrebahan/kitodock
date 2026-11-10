//go:build windows

package sysmetrics

// GPU utilization through the "GPU Engine" performance counters. Engines
// are multi-instance (one per engine type, sometimes per app), so the
// value is read as a counter *array* and summed. Machines without the
// counter family — no GPU driver, stripped Windows, CI sessions — make
// the reader fail once and report 0 forever after; a missing GPU is a
// state, not an error stream.

import (
	"sync"
	"syscall"
	"unsafe"
)

// pdhMoreData is PDH_MORE_DATA: the array needs a bigger buffer.
const pdhMoreData = 0x800007D2

var procFormattedArray = pdh.NewProc("PdhGetFormattedCounterArrayW")

// gpuCounterPath covers every engine type; 3D-only undersells machines
// whose work lands on compute/video engines.
const gpuCounterPath = `\GPU Engine(*)\Utilization Percentage`

// pdhItem mirrors PDH_FMT_COUNTERVALUE_ITEM_W on amd64: a wide-string
// pointer followed by the 16-byte value union (CStatus + double).
type pdhItem struct {
	Name        uintptr
	CStatus     uint32
	_           uint32
	DoubleValue float64
}

type gpuReader struct {
	mu      sync.Mutex
	query   uintptr
	counter uintptr
	lastGPU int
	primed  bool
	// unsupported latches the first failure so absence is discovered
	// once, not per sample.
	unsupported bool
}

var gpu = &gpuReader{}

func (r *gpuReader) percent() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.unsupported {
		return 0
	}
	if r.query == 0 {
		var query uintptr
		if rc, _, _ := procOpenQuery.Call(0, 0, uintptr(unsafe.Pointer(&query))); rc != 0 {
			r.unsupported = true
			return 0
		}
		path16, err := syscall.UTF16PtrFromString(gpuCounterPath)
		if err != nil {
			procCloseQuery.Call(query)
			r.unsupported = true
			return 0
		}
		var counter uintptr
		if rc, _, _ := procAddEnglishCount.Call(query, uintptr(unsafe.Pointer(path16)), 0, uintptr(unsafe.Pointer(&counter))); rc != 0 {
			// No GPU engine counter family on this machine.
			procCloseQuery.Call(query)
			r.unsupported = true
			return 0
		}
		r.query, r.counter = query, counter
	}
	if rc, _, _ := procCollect.Call(r.query); rc != 0 {
		return r.lastGPU
	}

	// Size the array buffer, retrying once after PDH_MORE_DATA.
	var bufSize uint32
	var count uint32
	rc, _, _ := procFormattedArray.Call(
		r.counter, pdhFmtDouble,
		uintptr(unsafe.Pointer(&bufSize)), uintptr(unsafe.Pointer(&count)), 0)
	if rc != pdhMoreData && rc != 0 {
		return r.lastGPU
	}
	buf := make([]byte, bufSize)
	rc, _, _ = procFormattedArray.Call(
		r.counter, pdhFmtDouble,
		uintptr(unsafe.Pointer(&bufSize)), uintptr(unsafe.Pointer(&count)),
		uintptr(unsafe.Pointer(&buf[0])))
	if rc != 0 || count == 0 {
		return r.lastGPU
	}
	items := (*[1 << 16]pdhItem)(unsafe.Pointer(&buf[0]))[:count:count]
	sum := 0.0
	for _, item := range items {
		if item.CStatus == 0 {
			sum += item.DoubleValue
		}
	}
	if !r.primed {
		r.primed = true
		return r.lastGPU
	}
	percent := int(sum + 0.5)
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	r.lastGPU = percent
	return percent
}
