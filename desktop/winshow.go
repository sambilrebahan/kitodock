package main

// Minimal Win32 window activation, written for what Phase 0 actually needs:
// surfacing the already-running main window when the exe is launched again
// (single-instance). The heavier machinery a detached command bar will need
// (foreground-lock games, thread-attached focus handoff) is intentionally
// absent until that window exists — see docs/progress/fase-0.md.

import (
	"context"
	"syscall"
	"unsafe"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

var (
	user32        = syscall.NewLazyDLL("user32.dll")
	findWindow    = user32.NewProc("FindWindowW")
	showWindow    = user32.NewProc("ShowWindow")
	setForeground = user32.NewProc("SetForegroundWindow")
)

// windowTitle is the exact main-window title FindWindowW matches.
const windowTitle = "Kitodock"

const swRestore = 9

// bringMainWindowUp restores and foregrounds the main window. The restore
// step matters: a hidden-to-tray window (Phase 1) has no visible state to
// raise until SW_RESTORE brings it back.
func bringMainWindowUp(ctx context.Context) {
	if ctx == nil {
		return
	}
	wruntime.WindowShow(ctx)
	wruntime.WindowUnminimise(ctx)
	go foregroundByTitle(windowTitle)
}

// foregroundByTitle finds the top-level window by exact title and asks Win32
// to restore + foreground it. Best effort by design: if the window is mid-
// creation the find simply misses and the click-through still works.
func foregroundByTitle(title string) {
	titlePtr, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	hwnd, _, _ := findWindow.Call(0, uintptr(unsafe.Pointer(titlePtr)))
	if hwnd == 0 {
		return
	}
	showWindow.Call(hwnd, swRestore)
	setForeground.Call(hwnd)
}
