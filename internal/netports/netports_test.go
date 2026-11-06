package netports

import (
	"bufio"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// spawnListener builds and starts the fixture, returning its port, the
// command (for cleanup) and the process name KillPort must allow.
func spawnListener(t *testing.T) (uint16, *exec.Cmd, string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("the snapshot path needs Windows")
	}
	exe := filepath.Join(t.TempDir(), "listener.exe")
	if out, err := exec.Command("go", "build", "-o", exe, "testdata/listener/main.go").CombinedOutput(); err != nil {
		t.Skipf("cannot build the listener fixture here: %v: %s", err, out)
	}
	cmd := exec.Command(exe)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	portLine, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		cmd.Process.Kill()
		t.Fatalf("fixture printed no port: %v", err)
	}
	port, err := strconv.Atoi(stringsTrim(portLine))
	if err != nil {
		cmd.Process.Kill()
		t.Fatalf("fixture port line %q: %v", portLine, err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		cmd.Wait()
	})
	return uint16(port), cmd, "listener.exe"
}

func stringsTrim(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

func TestListenersFindsOurOwnSocket(t *testing.T) {
	port, _, name := spawnListener(t)

	found := Listener{}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		all, err := Listeners()
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range all {
			if l.Port == port {
				found = l
				break
			}
		}
		if found.Port == port {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if found.Port != port {
		t.Fatalf("port %d never showed up in Listeners()", port)
	}
	if found.Addr != "127.0.0.1" {
		t.Errorf("Addr = %q, want 127.0.0.1", found.Addr)
	}
	if got := ProcessName(found.PID); got != name {
		t.Errorf("ProcessName(%d) = %q, want %q", found.PID, got, name)
	}
}

func TestKillPortRefusesWithoutAllowlist(t *testing.T) {
	port, _, _ := spawnListener(t)

	if err := KillPort(port); err == nil {
		t.Fatal("an empty allowlist must refuse to kill anything")
	}
}

func TestKillPortSparesForeignExecutables(t *testing.T) {
	port, cmd, _ := spawnListener(t)

	// The allowlist names something else entirely: the listener must live.
	if err := KillPort(port, "totally-other.exe"); err == nil {
		t.Fatal("killing a port held by a foreign executable should report failure")
	}
	if cmd.ProcessState != nil {
		t.Fatal("the listener was killed despite not being on the allowlist")
	}

	// Now allow it and expect the port back.
	if err := KillPort(port, "listener.exe"); err != nil {
		t.Fatalf("KillPort with the right allowlist: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		// The kill may land a hair after KillPort returns; only a process
		// still alive this much later is a real failure.
		cmd.Process.Kill()
		t.Fatal("the listener survived an allowed KillPort")
	}
}

func TestKillPortOnAFreePortFails(t *testing.T) {
	// Port 1 is privileged noise on Windows; nothing of ours listens there.
	if err := KillPort(1, "listener.exe"); err == nil {
		t.Fatal("killing a port with no matching listener must fail")
	}
}
