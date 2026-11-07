//go:build windows

package devserver

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

func hide(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

func killTree(pid int) {
	out, _ := exec.Command("taskkill", "/F", "/T", "/PID", fmt.Sprint(pid)).CombinedOutput()
	_ = out
}

// killPort ends listeners on port, but only the stack's own binaries.
func killPort(port int, allowExes ...string) {
	holders := portHolders(port)
	for pid, name := range holders {
		allowed := false
		for _, allow := range allowExes {
			if strings.EqualFold(name, allow) {
				allowed = true
				break
			}
		}
		if allowed {
			_ = exec.Command("taskkill", "/F", "/T", "/PID", fmt.Sprint(pid)).Run()
		}
	}
}

// portHolders maps pid → image name for listeners on port (via netstat;
// internal/netports stays out to keep this package dependency-free).
func portHolders(port int) map[uint32]string {
	out, err := exec.Command("netstat", "-ano", "-p", "tcp").CombinedOutput()
	if err != nil {
		return nil
	}
	holders := map[uint32]string{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 5 || !strings.HasPrefix(fields[3], "LISTENING") {
			continue
		}
		addr := fields[1]
		if !strings.HasSuffix(addr, fmt.Sprintf(":%d", port)) {
			continue
		}
		var pid uint32
		if _, err := fmt.Sscanf(fields[4], "%d", &pid); err == nil && pid > 0 {
			holders[pid] = imageName(pid)
		}
	}
	return holders
}

// imageName resolves a pid to its executable name via tasklist.
func imageName(pid uint32) string {
	out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH").CombinedOutput()
	if err != nil {
		return ""
	}
	fields := strings.Split(strings.TrimSpace(string(out)), "\",\"")
	if len(fields) >= 1 {
		return strings.Trim(fields[0], "\"")
	}
	return ""
}

func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }
