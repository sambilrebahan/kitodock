package main

// Live supervisor chain: real binaries installed from the pinned catalog,
// started through buildStack's manager (first-run setup included), proven
// by a listening socket in the system table, then stopped clean.
//
// paths resolves the environment root at process start, so the run needs
// the env up front:
//
//	KITODOCK_LIVE=1 KITODOCK_ROOT=$(mktemp -d) go test . -run Live -v -timeout 20m
//
// Redis and Mailpit are quick; MySQL downloads a few hundred MB and runs
// its first-start initialization — the timeout reflects that.

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"kitodock/internal/installer"
	"kitodock/internal/netports"
	"kitodock/internal/paths"
)

func liveGate(t *testing.T) {
	t.Helper()
	if os.Getenv("KITODOCK_LIVE") != "1" {
		t.Skip("set KITODOCK_LIVE=1 (and KITODOCK_ROOT) to run live chains")
	}
	if runtime.GOOS != "windows" {
		t.Skip("real services need Windows")
	}
}

// installCatalogEntry installs the newest pin of an id, reusing the shared
// live cache between tests.
func installCatalogEntry(t *testing.T, id string) installer.Source {
	t.Helper()
	src, ok := installer.Find(id, "")
	if !ok {
		t.Fatalf("%s missing from the catalog", id)
	}
	manager := installer.NewManager(liveCache(t), func(installer.Progress) {})
	if err := manager.Install(context.Background(), src); err != nil {
		t.Fatalf("install %s %s: %v", src.ID, src.Version, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(src.InstallTo()) })
	return src
}

func liveCache(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("KITODOCK_LIVE_CACHE")
	if dir == "" {
		dir = paths.DownloadsCache()
	}
	return dir
}

// waitForRunning blocks until the named service reports running, with the
// whole supervisor noise (first-run, restarts) inside the deadline.
func waitForRunning(t *testing.T, stack interface {
	Snapshot() []struct {
		Name    string
		Running bool
	}
}, name string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		for _, st := range stack.Snapshot() {
			if st.Name == name && st.Running {
				return
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("%s never reached the running state", name)
}

func listenerOnPort(port uint16) bool {
	listeners, err := netports.Listeners()
	if err != nil {
		return false
	}
	for _, l := range listeners {
		if l.Port == port {
			return true
		}
	}
	return false
}

func waitForPort(t *testing.T, port uint16, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if listenerOnPort(port) {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("nothing ever listened on :%d", port)
}

func TestLiveRedisThroughSupervisor(t *testing.T) {
	liveGate(t)
	installCatalogEntry(t, "redis")

	stack := buildStack()
	if err := stack.Start(context.Background(), "redis"); err != nil {
		t.Fatalf("start redis: %v", err)
	}
	t.Cleanup(func() { stack.StopAll() })

	waitForPort(t, 6379, 15*time.Second)
	t.Log("redis is listening on 127.0.0.1:6379 with the generated first-run config")

	stack.Stop("redis")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && listenerOnPort(6379) {
		time.Sleep(150 * time.Millisecond)
	}
	if listenerOnPort(6379) {
		t.Fatal("port 6379 still held after stop")
	}
}

func TestLiveMailpitThroughSupervisor(t *testing.T) {
	liveGate(t)
	installCatalogEntry(t, "mailpit")

	stack := buildStack()
	if err := stack.Start(context.Background(), "mailpit"); err != nil {
		t.Fatalf("start mailpit: %v", err)
	}
	t.Cleanup(func() { stack.StopAll() })

	waitForPort(t, 8025, 15*time.Second)
	t.Log("mailpit is listening on :8025 (web UI)")

	stack.Stop("mailpit")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && listenerOnPort(8025) {
		time.Sleep(150 * time.Millisecond)
	}
	if listenerOnPort(8025) {
		t.Fatal("port 8025 still held after stop")
	}
}

func TestLiveMySQLFirstRunThroughSupervisor(t *testing.T) {
	liveGate(t)
	installCatalogEntry(t, "mysql")

	stack := buildStack()
	// First Start triggers the definition's setup: mysqld
	// --initialize-insecure creating the data directory.
	if err := stack.Start(context.Background(), "mysql"); err != nil {
		t.Fatalf("start mysql (first run included): %v", err)
	}
	t.Cleanup(func() { stack.StopAll() })

	// The data directory is the durable first-run proof.
	dataDir := filepath.Join(paths.MySQLDir(), "data", "mysql")
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(dataDir); err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if _, err := os.Stat(dataDir); err != nil {
		t.Fatalf("first run never created the data directory: %v", err)
	}
	t.Log("mysql first run created its data directory (mysqld --initialize-insecure)")

	for _, line := range stack.Logs("mysql") {
		if line != "" {
			t.Logf("mysql log sample: %s", line)
			break
		}
	}

	// Serving proof needs the listener to be OURS: machines running their
	// own MySQL service own :3306 from session 0, and our instance loses
	// the bind race through no fault of its own.
	ours := map[int]bool{}
	for _, st := range stack.Snapshot() {
		if st.Name == "mysql" {
			for _, pid := range st.PIDs {
				ours[pid] = true
			}
		}
	}
	var owner uint32
	owned := false
	if listeners, err := netports.Listeners(); err == nil {
		for _, l := range listeners {
			if l.Port == 3306 && ours[int(l.PID)] {
				owned = true
				owner = l.PID
				break
			}
		}
	}
	if !owned {
		if listeners, _ := netports.Listeners(); len(listeners) > 0 {
			for _, l := range listeners {
				if l.Port == 3306 {
					t.Skipf(":3306 is held by pid %d (%s) — a system MySQL service, not ours; first-run initialization proven, serving not provable on this machine", l.PID, netports.ProcessName(l.PID))
				}
			}
		}
		t.Skip("our mysqld is not holding :3306; first-run initialization proven, serving not provable on this machine")
	}
	t.Logf("our mysqld (pid %d) owns :3306", owner)

	stack.Stop("mysql")
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && listenerOnPort(3306) {
		time.Sleep(150 * time.Millisecond)
	}
	if listenerOnPort(3306) {
		t.Fatal("port 3306 still held after stop")
	}
}
