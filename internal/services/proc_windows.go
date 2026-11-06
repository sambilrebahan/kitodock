//go:build windows

package services

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"kitodock/internal/vcruntime"
)

// applyHiddenWindow keeps supervision invisible: no console windows for any
// child, ever.
func applyHiddenWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

// killTree ends a pid and its descendants. taskkill's /T follows the process
// tree, which matters for dev servers that spawn workers.
func killTree(pid int) error {
	out, err := exec.Command("taskkill", "/F", "/T", "/PID", fmt.Sprint(pid)).CombinedOutput()
	if err != nil && !strings.Contains(strings.ToLower(string(out)), "not found") {
		return fmt.Errorf("taskkill pid %d: %s", pid, strings.TrimSpace(string(out)))
	}
	return nil
}

// killImage ends every process running one executable by image name.
func killImage(base string) error {
	out, err := exec.Command("taskkill", "/F", "/T", "/IM", base).CombinedOutput()
	if err != nil && !strings.Contains(strings.ToLower(string(out)), "not found") {
		return fmt.Errorf("taskkill /IM %s: %s", base, strings.TrimSpace(string(out)))
	}
	return nil
}

func parentDir(exe string) string { return filepath.Dir(exe) }

func baseName(exe string) string { return strings.ToLower(filepath.Base(exe)) }

// smokeCheck runs the redistributable probe on a binary before the manager
// commits to supervising it, so the UI can offer the right download.
func smokeCheck(exe, redistURL string) error {
	return vcruntime.Check(exe, redistURL)
}
