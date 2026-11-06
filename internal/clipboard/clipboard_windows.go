//go:build windows

package clipboard

import (
	"fmt"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	procOpen         = user32.NewProc("OpenClipboard")
	procClose        = user32.NewProc("CloseClipboard")
	procEmpty        = user32.NewProc("EmptyClipboard")
	procGetData      = user32.NewProc("GetClipboardData")
	procSetData      = user32.NewProc("SetClipboardData")
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procGlobalAlloc  = kernel32.NewProc("GlobalAlloc")
	procGlobalLock   = kernel32.NewProc("GlobalLock")
	procGlobalUnlock = kernel32.NewProc("GlobalUnlock")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
	busyAttempts  = 10
	busyPause     = 20 * time.Millisecond
)

// withClipboard opens the clipboard (retrying briefly: it is a system-wide
// lock and other programs do hold it) and runs body with it owned.
func withClipboard(body func() error) error {
	var last error
	for attempt := 0; attempt < busyAttempts; attempt++ {
		open, _, _ := procOpen.Call(0)
		if open != 0 {
			err := body()
			_, _, _ = procClose.Call()
			return err
		}
		last = fmt.Errorf("clipboard stayed busy after %d attempts", busyAttempts)
		time.Sleep(busyPause)
	}
	return last
}

// ReadText returns the clipboard's current text, or an empty string when it
// holds no text.
func ReadText() (string, error) {
	var text string
	err := withClipboard(func() error {
		handle, _, _ := procGetData.Call(cfUnicodeText)
		if handle == 0 {
			return nil // no text on the clipboard; not an error
		}
		ptr, _, _ := procGlobalLock.Call(handle)
		if ptr == 0 {
			return fmt.Errorf("lock clipboard text: %w", syscall.GetLastError())
		}
		defer procGlobalUnlock.Call(handle)
		// The block is NUL-terminated UTF-16; find the terminator.
		encoded := (*[1 << 20]uint16)(unsafe.Pointer(ptr))
		length := 0
		for encoded[length] != 0 {
			length++
		}
		text = string(utf16.Decode(encoded[:length]))
		return nil
	})
	return text, err
}

// WriteText replaces the clipboard's contents with text.
func WriteText(text string) error {
	encoded := utf16.Encode([]rune(text))
	// +1 for the terminating NUL the format requires.
	size := uintptr((len(encoded) + 1) * 2)
	return withClipboard(func() error {
		empty, _, err := procEmpty.Call()
		if empty == 0 {
			return fmt.Errorf("empty clipboard: %w", err)
		}
		block, _, err := procGlobalAlloc.Call(gmemMoveable, size)
		if block == 0 {
			return fmt.Errorf("allocate clipboard buffer: %w", err)
		}
		ptr, _, _ := procGlobalLock.Call(block)
		if ptr == 0 {
			return fmt.Errorf("lock clipboard buffer: %w", syscall.GetLastError())
		}
		copy(unsafe.Slice((*uint16)(unsafe.Pointer(ptr)), len(encoded)+1), append(encoded, 0))
		procGlobalUnlock.Call(block)
		// After a successful SetClipboardData the system owns the block;
		// it must not be freed here.
		set, _, err := procSetData.Call(cfUnicodeText, block)
		if set == 0 {
			// GlobalFree/LocalFree are the same heap on modern Windows.
			windows.LocalFree(windows.Handle(block))
			return fmt.Errorf("publish clipboard text: %w", err)
		}
		return nil
	})
}
