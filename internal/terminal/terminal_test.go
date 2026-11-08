package terminal

import (
	"runtime"
	"strings"
	"testing"
)

func TestDetectFindsBuiltInTerminals(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("terminal detection needs Windows")
	}
	profiles := Detect("")
	kinds := map[Kind]bool{}
	for _, p := range profiles {
		kinds[p.Kind] = true
	}
	if !kinds[PowerShell] {
		t.Error("PowerShell not detected")
	}
	if !kinds[Cmd] {
		t.Error("cmd not detected")
	}
}

func TestDetectFindsGitBash(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("terminal detection needs Windows")
	}
	profiles := Detect("")
	for _, p := range profiles {
		if p.Kind == GitBash && p.Path != "" {
			t.Logf("Git Bash found at %s", p.Path)
			if !strings.Contains(strings.ToLower(p.Path), "bash") {
				t.Errorf("unexpected Git Bash path: %s", p.Path)
			}
			return
		}
	}
	t.Log("Git Bash not installed on this machine (acceptable)")
}

func TestOpenWithoutBinaryFails(t *testing.T) {
	if err := Open(PowerShell, "", "C:\\", nil); err == nil {
		t.Fatal("missing binary accepted")
	}
}

func TestUnknownKindFails(t *testing.T) {
	if err := Open("unknown", "C:\\some.exe", "C:\\", nil); err == nil {
		t.Fatal("unknown kind accepted")
	}
}
