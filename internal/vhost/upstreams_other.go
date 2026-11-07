//go:build !windows

package vhost

import "syscall"

func hideCertutilWindow() *syscall.SysProcAttr { return nil }
