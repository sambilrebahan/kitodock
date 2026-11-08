// Package terminal opens external terminal windows at a project folder —
// the user's own terminal, not the embedded PTY. Four kinds are detected
// from PATH and well-known install locations.
package terminal

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
)

// Kind identifies a terminal type.
type Kind string

const (
	PowerShell Kind = "powershell"
	Cmd        Kind = "cmd"
	GitBash    Kind = "gitbash"
	Cmder      Kind = "cmder"
)

// Profile is one detectable terminal.
type Profile struct {
	Kind Kind   `json:"kind"`
	Path string `json:"path"` // resolved binary path ("" = not found)
}

// Detect finds every terminal available on this machine.
func Detect(cmderRoot string) []Profile {
	if runtime.GOOS != "windows" {
		return nil
	}
	var out []Profile

	for _, candidate := range []struct {
		kind Kind
		exe  string
	}{
		{PowerShell, "powershell.exe"},
		{Cmd, "cmd.exe"},
	} {
		if path, err := exec.LookPath(candidate.exe); err == nil {
			out = append(out, Profile{Kind: candidate.kind, Path: path})
		}
	}

	// Git Bash: common install dirs + PATH.
	gitBash := findGitBash()
	if gitBash != "" {
		out = append(out, Profile{Kind: GitBash, Path: gitBash})
	}

	// Cmder: from our install root.
	if cmderRoot != "" {
		cmder := filepath.Join(cmderRoot, "cmder.exe")
		if _, err := os.Stat(cmder); err == nil {
			out = append(out, Profile{Kind: Cmder, Path: cmder})
		}
	}
	return out
}

func findGitBash() string {
	// Try PATH first.
	if path, err := exec.LookPath("bash.exe"); err == nil {
		return path
	}
	// Common install locations.
	for _, dir := range []string{
		`C:\Program Files\Git\bin\bash.exe`,
		`C:\Program Files (x86)\Git\bin\bash.exe`,
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Git", "bin", "bash.exe"),
	} {
		if _, err := os.Stat(dir); err == nil {
			return dir
		}
	}
	return ""
}

// Open launches an external terminal at dir with optional extra PATH dirs.
func Open(kind Kind, exePath, dir string, extraPathDirs []string) error {
	if exePath == "" {
		return fmt.Errorf("terminal %s not found", kind)
	}
	if _, err := os.Stat(exePath); err != nil {
		return fmt.Errorf("terminal %s binary missing: %s", kind, exePath)
	}

	var args []string
	switch kind {
	case PowerShell:
		args = []string{"-NoExit", "-Command", "Set-Location -LiteralPath '" + dir + "'"}
	case Cmd:
		args = []string{"/K", "cd /d " + dir}
	case GitBash:
		args = []string{"--login", "-i", "-c", "cd '" + filepath.ToSlash(dir) + "' && exec bash"}
	case Cmder:
		args = []string{"/start", dir}
	default:
		return fmt.Errorf("unknown terminal kind %q", kind)
	}

	cmd := exec.Command("cmd", append([]string{"/c", "start", "", exePath}, args...)...)
	if len(extraPathDirs) > 0 {
		path := ""
		for i, dir := range extraPathDirs {
			if i > 0 {
				path += string(os.PathListSeparator)
			}
			path += dir
		}
		cmd.Env = append(os.Environ(), "PATH="+path+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run()
}

// OpenWithCommand launches a terminal at dir and runs a command in it.
func OpenWithCommand(kind Kind, exePath, dir, command string, extraPathDirs []string) error {
	if exePath == "" {
		return fmt.Errorf("terminal %s not found", kind)
	}

	var args []string
	switch kind {
	case PowerShell:
		args = []string{"-NoExit", "-Command", "Set-Location -LiteralPath '" + dir + "'; " + command}
	case Cmd:
		args = []string{"/K", "cd /d " + dir + " && " + command}
	case GitBash:
		args = []string{"--login", "-i", "-c", "cd '" + filepath.ToSlash(dir) + "' && " + command + "; exec bash"}
	case Cmder:
		// Cmder doesn't support initial commands.
		args = []string{"/start", dir}
	default:
		return fmt.Errorf("unknown terminal kind %q", kind)
	}

	cmd := exec.Command("cmd", append([]string{"/c", "start", "", exePath}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run()
}
