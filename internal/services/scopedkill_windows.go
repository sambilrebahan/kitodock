//go:build windows

package services

// Scoped process cleanup: KillStrays must only end processes whose
// executable actually lives inside the Kitodock environment — a plain
// killImage("nginx.exe") would take down every nginx on the machine,
// including system services and other dev stacks.

import (
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// killImageUnderRoot ends every process whose image name matches base
// AND whose executable path is under root. Falls back to a no-op when
// the query interface is unavailable.
func killImageUnderRoot(root, base string) error {
	pids, err := findPidsUnderRoot(root, base)
	if err != nil {
		return err
	}
	if len(pids) == 0 {
		return nil // nothing ours to kill: good news, not an error
	}
	var firstErr error
	for _, pid := range pids {
		if err := killTree(pid); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// findPidsUnderRoot lists PIDs whose exe is under root and whose image
// base name matches. The process image path comes from a per-PID query.
func findPidsUnderRoot(root, base string) ([]int, error) {
	// Enumerate all processes with their image names via a simpler
	// approach: PowerShell-free, using OpenProcess+QueryFullProcessImageName.
	pids := listPidsByImageName(base)
	var ours []int
	rootLower := strings.ToLower(filepath.Clean(root))
	for _, pid := range pids {
		if exe := processImagePath(pid); exe != "" {
			if strings.HasPrefix(strings.ToLower(filepath.Clean(exe)), rootLower) {
				ours = append(ours, pid)
			}
		}
	}
	return ours, nil
}

// listPidsByImageName is a lightweight snapshot via the toolhelp API.
func listPidsByImageName(base string) []int {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	create := kernel32.NewProc("CreateToolhelp32Snapshot")
	first := kernel32.NewProc("Process32FirstW")
	next := kernel32.NewProc("Process32NextW")
	closeSnap := kernel32.NewProc("CloseHandle")

	type processEntry32 struct {
		Size        uint32
		Usage       uint32
		ProcessID   uint32
		DefaultHeap uintptr
		ModuleID    uint32
		Threads     uint32
		ParentPID   uint32
		PriClass    int32
		Flags       uint32
		ExeFile     [260]uint16
	}
	const th32csSnapProcess = 0x2

	snap, _, _ := create.Call(th32csSnapProcess, 0)
	if snap == 0 || snap == ^uintptr(0) {
		return nil
	}
	defer closeSnap.Call(snap)

	var pids []int
	var entry processEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	handle, _, _ := first.Call(snap, uintptr(unsafe.Pointer(&entry)))
	for handle != 0 {
		name := syscall.UTF16ToString(entry.ExeFile[:])
		if strings.EqualFold(name, base) {
			pids = append(pids, int(entry.ProcessID))
		}
		handle, _, _ = next.Call(snap, uintptr(unsafe.Pointer(&entry)))
	}
	return pids
}

// processImagePath returns the full executable path of one PID ("" on failure).
func processImagePath(pid int) string {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	open := kernel32.NewProc("OpenProcess")
	query := kernel32.NewProc("QueryFullProcessImageNameW")
	closeProc := kernel32.NewProc("CloseHandle")

	const processQueryLimitedInformation = 0x1000
	handle, _, _ := open.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if handle == 0 {
		return ""
	}
	defer closeProc.Call(handle)

	buf := make([]uint16, 1024)
	size := uint32(len(buf))
	ret, _, _ := query.Call(handle, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if ret == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:size])
}
