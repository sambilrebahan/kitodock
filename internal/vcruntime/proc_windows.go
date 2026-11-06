//go:build windows

package vcruntime

import "syscall"

// hiddenWindow keeps the smoke-run console from flashing on screen.
func hiddenWindow() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
