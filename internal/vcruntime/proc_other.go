//go:build !windows

package vcruntime

import "syscall"

func hiddenWindow() *syscall.SysProcAttr { return nil }
