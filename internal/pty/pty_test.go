package pty

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLivePathContainsExtraDirs(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("registry PATH needs Windows")
	}
	path := LivePath([]string{`D:\php\8.4`, `D:\nodejs\24`})
	if !strings.HasPrefix(strings.ToLower(path), `d:\php\8.4`) {
		t.Errorf("extra dirs not first: %s…", path[:min(60, len(path))])
	}
	if !strings.Contains(path, `D:\nodejs\24`) {
		t.Error("second extra dir missing")
	}
}

func TestDedupIsCaseInsensitive(t *testing.T) {
	got := dedup([]string{`C:\A`, `c:\a`, `C:\B`, ``, `C:\B`})
	if len(got) != 2 || got[0] != `C:\A` || got[1] != `C:\B` {
		t.Errorf("dedup = %v", got)
	}
}

func TestReplaceOrAppend(t *testing.T) {
	env := []string{"PATH=C:\\old", "FOO=bar"}
	env = replaceOrAppend(env, "PATH", "C:\\new")
	if len(env) != 2 || env[0] != "PATH=C:\\new" {
		t.Errorf("replace: %v", env)
	}
	env = replaceOrAppend(env, "NEW", "val")
	if len(env) != 3 || env[2] != "NEW=val" {
		t.Errorf("append: %v", env)
	}
}

func TestSpawnWriteReadExit(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("ConPTY needs Windows")
	}
	// GitHub Actions runners have no interactive session; ConPTY's
	// pseudoconsole needs one to function correctly.
	if os.Getenv("GITHUB_ACTIONS") == "true" || os.Getenv("CI") == "true" {
		t.Skip("ConPTY needs an interactive session (not available on CI runners)")
	}
	r := NewRegistry()
	defer r.CloseAll()

	id, err := r.StartCommand(Spawn{
		Exe: "cmd", Args: []string{"/c", "echo KITODOCK-PTY-OK"},
		Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}

	session, ok := r.Get(id)
	if !ok {
		t.Fatal("session not in registry")
	}

	// Read until we see the marker or the session closes.
	buf := make([]byte, 4096)
	var output string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		n, err := session.Read(buf)
		if n > 0 {
			output += string(buf[:n])
			if strings.Contains(output, "KITODOCK-PTY-OK") {
				break
			}
		}
		if err != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(output, "KITODOCK-PTY-OK") {
		t.Fatalf("marker not seen in output: %q", output)
	}
	t.Logf("got output: %q", strings.TrimSpace(output))

	r.Close(id)
	if _, still := r.Get(id); still {
		t.Fatal("session survived Close")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
