//go:build !windows

package nodejs

import "syscall"

func hideWindow() *syscall.SysProcAttr { return nil }
