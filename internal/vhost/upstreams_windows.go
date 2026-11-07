//go:build windows

package vhost

import "syscall"

func hideCertutilWindow() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
