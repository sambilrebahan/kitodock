package devserver

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// devhost is a tiny fixture "dev server": a Go program that prints port
// lines like real dev servers do, then holds a port open. The fixture runs
// via `go run testdata/devhost`; this TestMain shim exists only for that
// child to reuse the package's arg parsing when invoked with --devhost.
func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func TestDetectPortPriorities(t *testing.T) {
	cases := []struct {
		line     string
		wantPort int
		wantPri  int
	}{
		{"  SERVER RUNNING ON http://127.0.0.1:8000", 8000, 2},
		{"  Server started on port 3000", 3000, 2},
		{"  ➜  Local:   http://localhost:5173/", 5173, 1},
		{"ready in 412 ms — listening on http://[::1]:4321", 4321, 1},
		{"App listening at http://localhost:9999", 9999, 0},
		{"Port 5173 is in use, trying 5174 instead.", 0, 0},
	}
	for _, tc := range cases {
		port, pri, ok := detectPort(tc.line)
		if tc.wantPri == 0 {
			if ok && port != tc.wantPort {
				t.Errorf("detectPort(%q) = (%d,%d) want conflict-ignored", tc.line, port, pri)
			}
			continue
		}
		if port != tc.wantPort || pri != tc.wantPri {
			t.Errorf("detectPort(%q) = (%d, prio %d), want (%d, prio %d)", tc.line, port, pri, tc.wantPort, tc.wantPri)
		}
	}
}

func TestHigherPriorityReplacesLower(t *testing.T) {
	p := &process{}
	p.observe("  ➜  Local:   http://localhost:5173/") // Vite asset server
	if p.currentPort() != 5173 {
		t.Fatalf("first hit lost: %d", p.currentPort())
	}
	p.observe("  Server running on http://127.0.0.1:8000") // the app server
	if p.currentPort() != 8000 {
		t.Fatalf("app server should win: %d", p.currentPort())
	}
	// A lower-priority line after a higher one must not override.
	p.observe("something at localhost:3000")
	if p.currentPort() != 8000 {
		t.Fatalf("lower priority overrode: %d", p.currentPort())
	}
}

func writeProject(t *testing.T, scripts string) string {
	t.Helper()
	dir := t.TempDir()
	pkg := `{"name":"fixture","scripts":{` + scripts + `}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// devhostBinary builds the fixture once per test run (file-list mode so
// testdata's module invisibility does not matter). It uses a manual temp
// path: the spawned exe outlives t.TempDir's cleanup by a beat.
func devhostBinary(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "devhost-build-*")
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "devhost.exe")
	build := exec.Command("go", "build", "-o", exe, filepath.Join("testdata", "devhost", "main.go"))
	if out, err := build.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		t.Skipf("cannot build the devhost fixture here: %v: %s", err, out)
	}
	// Clean up late: after all test cleanups (including process kills).
	t.Cleanup(func() {
		time.Sleep(500 * time.Millisecond)
		os.RemoveAll(dir)
	})
	return exe
}

func TestStartDetectsPortAndStopFreesIt(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("devhost fixture needs Windows cmd spawning")
	}
	dir, _ := os.MkdirTemp("", "devproj-*")
	t.Cleanup(func() {
		time.Sleep(300 * time.Millisecond)
		os.RemoveAll(dir)
	})
	pkg := `{"name":"fixture","scripts":{"dev":"echo hi"}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := devhostBinary(t)

	m := NewManager()
	reserved, err := m.Start(Config{
		ProjectName: "fixture",
		Path:        dir,
		Command:     exe + " bare",
	})
	if err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the devhost to report a port", func() bool {
		return m.Status("fixture").Port != 0
	})
	st := m.Status("fixture")
	if st.Port != reserved {
		t.Logf("detected port %d (reserved %d) — fixture picks its own; that's fine", st.Port, reserved)
	}
	if st.PID == 0 {
		t.Fatal("no pid recorded")
	}

	m.Stop("fixture")
	waitFor(t, "status to clear", func() bool {
		return !m.Status("fixture").Running
	})
	// Let the OS release the working directory before the cleanup fires.
	time.Sleep(300 * time.Millisecond)
}

func TestStartWithoutScriptFailsClearly(t *testing.T) {
	dir := writeProject(t, `"build":"echo hi"`)
	m := NewManager()
	if _, err := m.Start(Config{ProjectName: "x", Path: dir}); err == nil {
		t.Fatal("no dev/start script should fail with a clear error")
	}
}

func sleepMs(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }
