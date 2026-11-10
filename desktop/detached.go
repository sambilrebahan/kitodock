package main

// spawnDetachedWindow launches a satellite terminal process. Credentials
// travel through the child's environment, never the command line.

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func spawnDetachedWindow(ptyID, ownerURL, token string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "--terminal-window", ptyID)
	cmd.Env = append(os.Environ(),
		"KITODOCK_OWNER_URL="+ownerURL,
		"KITODOCK_OWNER_TOKEN="+token,
		"KITODOCK_SKIP_ELEVATE=1",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: false, CreationFlags: 0x08000000}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start terminal window: %w", err)
	}
	// Reap on exit so the child never lingers as a zombie entry.
	go func() { _ = cmd.Wait() }()
	return nil
}
