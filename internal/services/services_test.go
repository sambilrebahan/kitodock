package services

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixture builds fakejob once and hands out definitions bound to it.
type fixture struct {
	t   *testing.T
	exe string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	// GitHub Actions runners are shared and process management tests
	// (spawn/kill/stray detection) are inherently flaky there.
	if os.Getenv("GITHUB_ACTIONS") == "true" || os.Getenv("CI") == "true" {
		t.Skip("process management tests are flaky on CI runners")
	}
	exe := filepath.Join(t.TempDir(), "fakejob.exe")
	if out, err := exec.Command("go", "build", "-o", exe, "testdata/fakejob/main.go").CombinedOutput(); err != nil {
		t.Skipf("cannot build the fakejob fixture here: %v: %s", err, out)
	}
	return &fixture{t: t, exe: exe}
}

// def builds a Definition; vary the options per test.
type defOptions struct {
	name     string
	group    string
	args     []string
	deps     []string
	internal bool
	setup    func(ctx context.Context, log func(string)) error
	needs    func() bool
}

func (f *fixture) def(o defOptions) Definition {
	return Definition{
		Name:       o.name,
		LabelKey:   "services.names." + o.name,
		Exe:        func() string { return f.exe },
		Args:       func() []string { return o.args },
		Group:      o.group,
		Deps:       o.deps,
		Internal:   o.internal,
		Setup:      o.setup,
		NeedsSetup: o.needs,
	}
}

func statusOf(t *testing.T, m *Manager, name string) Status {
	t.Helper()
	for _, st := range m.Snapshot() {
		if st.Name == name {
			return st
		}
	}
	t.Fatalf("service %q never registered", name)
	return Status{}
}

func waitFor(t *testing.T, what string, every time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(every)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestStartStopLifecycleWithLogs(t *testing.T) {
	f := newFixture(t)
	m := New()
	m.Register(f.def(defOptions{name: "hold", args: []string{"hold"}}))

	if err := m.Start(context.Background(), "hold"); err != nil {
		t.Fatal(err)
	}
	st := statusOf(t, m, "hold")
	if !st.Running || !st.Installed || len(st.PIDs) == 0 {
		t.Fatalf("status after start = %+v", st)
	}
	waitFor(t, "heartbeat in the log ring", 50*time.Millisecond, func() bool {
		for _, line := range m.Logs("hold") {
			if strings.HasPrefix(line, "beat ") {
				return true
			}
		}
		return false
	})

	if err := m.Stop("hold"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the service to stop", 50*time.Millisecond, func() bool {
		return !statusOf(t, m, "hold").Running
	})
	if pid := st.PIDs[0]; processAlive(pid) {
		t.Fatalf("pid %d survived the stop", pid)
	}
}

func TestGroupMembersRefuseToShare(t *testing.T) {
	f := newFixture(t)
	m := New()
	m.Register(f.def(defOptions{name: "web-a", group: "web", args: []string{"hold"}}))
	m.Register(f.def(defOptions{name: "web-b", group: "web", args: []string{"hold"}}))

	if err := m.Start(context.Background(), "web-a"); err != nil {
		t.Fatal(err)
	}
	err := m.Start(context.Background(), "web-b")
	var clash *GroupConflictError
	if !errors.As(err, &clash) {
		t.Fatalf("starting a group sibling = %v, want a GroupConflictError", err)
	}
	if clash.Blocking != "web-a" {
		t.Errorf("conflict names %q, want web-a", clash.Blocking)
	}

	if err := m.Stop("web-a"); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background(), "web-b"); err != nil {
		t.Fatalf("after stopping the sibling, start should work: %v", err)
	}
	m.StopAll()
}

func TestMissingBinaryIsAStateNotACrash(t *testing.T) {
	m := New()
	m.Register(Definition{Name: "ghost", LabelKey: "services.names.ghost",
		Exe: func() string { return "" }})

	err := m.Start(context.Background(), "ghost")
	var missing *NotInstalledError
	if !errors.As(err, &missing) {
		t.Fatalf("start = %v, want NotInstalledError", err)
	}
	if statusOf(t, m, "ghost").Installed {
		t.Error("a service with no binary must not count as installed")
	}

	err = m.Start(context.Background(), "never-registered")
	var unknown *UnknownServiceError
	if !errors.As(err, &unknown) {
		t.Fatalf("start = %v, want UnknownServiceError", err)
	}
}

func TestFirstRunSetupRunsExactlyOnce(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "initialized")
	calls := 0
	m := New()

	m.Register(f.def(defOptions{
		name: "needs-setup",
		args: []string{"hold"},
		needs: func() bool {
			_, err := os.Stat(marker)
			return err != nil
		},
		setup: func(ctx context.Context, log func(string)) error {
			calls++
			log("initializing…")
			return os.WriteFile(marker, []byte("1"), 0o644)
		},
	}))

	if err := m.Start(context.Background(), "needs-setup"); err != nil {
		t.Fatal(err)
	}
	m.Stop("needs-setup")
	if calls != 1 {
		t.Fatalf("setup ran %d times, want exactly once", calls)
	}
	if err := m.Start(context.Background(), "needs-setup"); err != nil {
		t.Fatal(err)
	}
	m.Stop("needs-setup")
	if calls != 1 {
		t.Fatalf("second start re-ran setup (%d calls)", calls)
	}
	found := false
	for _, line := range m.Logs("needs-setup") {
		if line == "initializing…" {
			found = true
		}
	}
	if !found {
		t.Error("setup logging never reached the service ring")
	}
}

func TestDependenciesRideAlongAndStopWhenUnneeded(t *testing.T) {
	f := newFixture(t)
	m := New()
	m.Register(f.def(defOptions{name: "worker", args: []string{"hold"}}))
	m.Register(f.def(defOptions{name: "app-a", args: []string{"hold"}, deps: []string{"worker"}}))
	m.Register(f.def(defOptions{name: "app-b", args: []string{"hold"}, deps: []string{"worker"}}))

	if err := m.Start(context.Background(), "app-a"); err != nil {
		t.Fatal(err)
	}
	if !statusOf(t, m, "worker").Running {
		t.Fatal("starting app-a must bring worker up")
	}

	// A second dependent keeps the dependency alive when the first stops.
	if err := m.Start(context.Background(), "app-b"); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop("app-a"); err != nil {
		t.Fatal(err)
	}
	if !statusOf(t, m, "worker").Running {
		t.Fatal("worker must stay while app-b still needs it")
	}

	if err := m.Stop("app-b"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "worker to stop with its last dependent", 50*time.Millisecond, func() bool {
		return !statusOf(t, m, "worker").Running
	})
}

func TestInternalCrashLoopGivesUp(t *testing.T) {
	f := newFixture(t)
	m := New()
	m.TuneBackoff(5*time.Millisecond, 20*time.Millisecond, 3)
	var mu sync.Mutex
	failures := 0
	m.Notify(nil, func(name string, err error) { mu.Lock(); failures++; mu.Unlock() })
	m.Register(f.def(defOptions{name: "pool", args: []string{"quit"}, internal: true}))

	if err := m.Start(context.Background(), "pool"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the crash loop to give up", 50*time.Millisecond, func() bool {
		return !statusOf(t, m, "pool").Running
	})
	// The give-up state lands a hair before the error callback runs; wait
	// for the report, not just the state.
	waitFor(t, "the give-up verdict to be reported", 50*time.Millisecond, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return failures > 0
	})
}

// A user-facing service must NOT be resurrected behind the user's back.
func TestUserServiceDeathIsReportedNotRestarted(t *testing.T) {
	f := newFixture(t)
	m := New()
	m.TuneBackoff(5*time.Millisecond, 20*time.Millisecond, 3)
	reported := make(chan error, 4)
	m.Notify(nil, func(name string, err error) { reported <- err })
	m.Register(f.def(defOptions{name: "fragile", args: []string{"quit"}}))

	if err := m.Start(context.Background(), "fragile"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-reported:
		if err == nil {
			t.Fatal("the crash must be reported")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the crash was never reported")
	}
	waitFor(t, "status to settle on stopped", 50*time.Millisecond, func() bool {
		return !statusOf(t, m, "fragile").Running
	})
	// And it stays down.
	time.Sleep(150 * time.Millisecond)
	if statusOf(t, m, "fragile").Running {
		t.Fatal("a user-facing service resurrected itself")
	}
}

func TestStopKillsTheWholeTree(t *testing.T) {
	if runtimeNotWindows() {
		t.Skip("tree-kill semantics need Windows taskkill")
	}
	f := newFixture(t)
	m := New()
	m.Register(f.def(defOptions{name: "parent", args: []string{"child"}}))

	if err := m.Start(context.Background(), "parent"); err != nil {
		t.Fatal(err)
	}
	// The fixture prints "child pid N" once the child exists.
	childPID := 0
	waitFor(t, "the child to spawn", 50*time.Millisecond, func() bool {
		for _, line := range m.Logs("parent") {
			if strings.HasPrefix(line, "child pid ") {
				n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "child pid ")))
				if err == nil {
					childPID = n
					return true
				}
			}
		}
		return false
	})

	if err := m.Stop("parent"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the child to die with its parent", 50*time.Millisecond, func() bool {
		return !processAlive(childPID)
	})
}

func TestKillStraysCleansLeftovers(t *testing.T) {
	if runtimeNotWindows() {
		t.Skip("stray cleanup needs Windows taskkill")
	}
	f := newFixture(t)
	// Start a raw process the manager knows nothing about… then teach the
	// manager its definition and sweep.
	stray := exec.Command(f.exe, "hold")
	stdout, err := stray.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := stray.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdout)
	first, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(first, "pid") {
		stray.Process.Kill()
		t.Fatalf("stray did not announce itself: %q (%v)", first, err)
	}
	t.Cleanup(func() { stray.Process.Kill(); stray.Wait() })

	m := New()
	m.Register(f.def(defOptions{name: "hold", args: []string{"hold"}}))
	m.KillStrays()
	waitFor(t, "the stray to be swept", 50*time.Millisecond, func() bool {
		return !processAlive(stray.Process.Pid)
	})
}

func processAlive(pid int) bool {
	if runtimeNotWindows() {
		proc, err := os.FindProcess(pid)
		return err == nil && proc != nil
	}
	// taskkill /PID without /F on an alive process reports a refusal
	// mentioning access; a dead one reports not found.
	out, err := exec.Command("taskkill", "/PID", strconv.Itoa(pid)).CombinedOutput()
	text := strings.ToLower(string(out))
	if err == nil {
		return true
	}
	return !strings.Contains(text, "not found")
}

func runtimeNotWindows() bool {
	return os.PathSeparator != '\\'
}
