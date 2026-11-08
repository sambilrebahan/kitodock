package queueworker

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func laravelProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "artisan"), []byte("stub"), 0o644)
	return dir
}

func testManager(t *testing.T, projectDir string) *Manager {
	t.Helper()
	return NewManager(Config{
		PhpExe:  func(string) string { return "php.exe" },
		WorkDir: func(string) string { return projectDir },
	})
}

func TestStartWithoutArtisanFails(t *testing.T) {
	dir := t.TempDir() // no artisan
	m := testManager(t, dir)
	if err := m.Start("x", ""); err == nil {
		t.Fatal("non-Laravel project accepted")
	}
}

func TestStartWithoutPhpFails(t *testing.T) {
	dir := laravelProject(t)
	m := NewManager(Config{
		PhpExe:  func(string) string { return "" },
		WorkDir: func(string) string { return dir },
	})
	if err := m.Start("x", ""); err == nil {
		t.Fatal("missing PHP accepted")
	}
}

func TestStatusLifecycle(t *testing.T) {
	dir := laravelProject(t)
	m := testManager(t, dir)

	if st := m.Status("x"); st.Running {
		t.Fatal("worker running before start")
	}

	// Start will fail because php.exe doesn't exist, but the error path
	// is what we're testing.
	if err := m.Start("x", "--queue=default"); err == nil {
		if runtime.GOOS != "windows" {
			t.Skip("php.exe check differs off Windows")
		}
		t.Fatal("php.exe stub should fail to start")
	}

	// After a failed start, status is still not running.
	if st := m.Status("x"); st.Running {
		t.Fatal("status running after failed start")
	}
}

func TestStopAllCleansUp(t *testing.T) {
	dir := laravelProject(t)
	m := testManager(t, dir)
	m.StopAll() // no-op
	if st := m.Status("x"); st.Running {
		t.Fatal("stopall left something running")
	}
}

func TestLogsReturnNilWhenNotRunning(t *testing.T) {
	dir := laravelProject(t)
	m := testManager(t, dir)
	if logs := m.Logs("x"); logs != nil {
		t.Fatal("logs returned when not running")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
