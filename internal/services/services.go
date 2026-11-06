// Package services supervises the local development stack: web servers,
// databases, caches and worker pools, each an ordinary child process that
// must start hidden, restart sensibly when it dies, and never leak children
// behind its back.
//
// The Manager owns the whole lifecycle of every registered Definition. It
// knows nothing about where binaries come from (the installer does) or how
// to render them (the controllers do); Definition carries lazy path
// functions precisely so an uninstalled service is a visible state, not an
// error.
package services

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// Definition describes one supervisable service. The path-bearing fields
// are functions: installs and moves happen while the app runs, so nothing
// resolves a path before it is needed.
type Definition struct {
	// Name is the stable id ("nginx") used across config, UI and events.
	Name string
	// LabelKey points at the display name in the catalogues.
	LabelKey string
	// Exe returns the binary path, "" while not installed.
	Exe func() string
	// Args builds the command line; Replicas>1 pools ignore it in favor of
	// ReplicaArgs.
	Args func() []string
	// Dir is the working directory; "" means the binary's own folder.
	Dir func() string
	// Env adds variables to the child environment.
	Env []string
	// Group marks mutually exclusive services (all web servers bind :80):
	// starting one while another member runs is refused with the peer named.
	Group string
	// Deps are started first and stopped again with the service, as long as
	// nothing else still needs them.
	Deps []string
	// Internal marks a worker pool the manager keeps alive on its own
	// (php-fpm later); user-facing services only report their death.
	Internal bool
	// Replicas is the process pool size (default 1).
	Replicas int
	// ReplicaArgs builds the command line of pool member index.
	ReplicaArgs func(index int) []string
	// NeedsSetup reports a missing first-run state; Setup performs it once.
	NeedsSetup func() bool
	Setup      func(ctx context.Context, log func(string)) error
	// SmokeProbe opts the service into a pre-start `<exe> --version` run:
	// only binaries that answer --version instantly and die cryptically
	// without their runtime (PHP builds) benefit. Services that ignore
	// flags or keep running must leave this off.
	SmokeProbe bool
	// RedistURL overrides the VC++ download offered when the smoke run
	// cannot start the binary for runtime reasons.
	RedistURL string
	// Version reports the running binary's version, when it matters.
	Version func() string
}

// Status is one service's snapshot for the UI.
type Status struct {
	Name      string `json:"name"`
	LabelKey  string `json:"labelKey"`
	Group     string `json:"group,omitempty"`
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	PIDs      []int  `json:"pids,omitempty"`
	Version   string `json:"version,omitempty"`
}

// GroupConflictError explains that a sibling service already holds the
// group — a clear sentence beats a bind: address-in-use stack trace.
type GroupConflictError struct {
	Group    string
	Blocking string
}

func (e *GroupConflictError) Error() string {
	return fmt.Sprintf("%s is already running; stop it first", e.Blocking)
}

// NotInstalledError names a service whose binary is not on disk.
type NotInstalledError struct {
	Name string
}

func (e *NotInstalledError) Error() string {
	return fmt.Sprintf("%s is not installed", e.Name)
}

// UnknownServiceError names a service that was never registered.
type UnknownServiceError struct{ Name string }

func (e *UnknownServiceError) Error() string { return "unknown service: " + e.Name }

// Manager supervises a registered set of services. All exported methods are
// safe for concurrent use.
type Manager struct {
	mu       sync.Mutex
	defs     map[string]Definition
	order    []string
	running  map[string]*instance
	logs     map[string]*ring
	crashes  map[string]*crashClock
	stopFlag map[string]bool // intended stops suppress crash handling

	onStatus func(Status)
	onError  func(name string, err error)

	// backoffBase/backoffCap/giveUpAfter are knobs the tests shrink.
	backoffBase time.Duration
	backoffCap  time.Duration
	giveUpAfter int
}

type instance struct {
	label     string
	startedAt time.Time
	replicas  []*replica
}

type replica struct {
	label string
	cmd   *exec.Cmd
	dead  chan struct{} // closed when the process exited and was reaped
}

type crashClock struct {
	quickDeaths int
	nextWait    time.Duration
}

const (
	defaultBackoffBase = 500 * time.Millisecond
	defaultBackoffCap  = 8 * time.Second
	defaultGiveUpAfter = 8
	logRingCap         = 500
	// shortLife is how long a start must survive to count as healthy.
	shortLife = 30 * time.Second
)

// New creates an empty manager. Services register before the UI sees it.
func New() *Manager {
	return &Manager{
		defs:        map[string]Definition{},
		running:     map[string]*instance{},
		logs:        map[string]*ring{},
		crashes:     map[string]*crashClock{},
		stopFlag:    map[string]bool{},
		backoffBase: defaultBackoffBase,
		backoffCap:  defaultBackoffCap,
		giveUpAfter: defaultGiveUpAfter,
	}
}

// TuneBackoff shrinks the restart policy for tests.
func (m *Manager) TuneBackoff(base, cap time.Duration, giveUpAfter int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.backoffBase, m.backoffCap, m.giveUpAfter = base, cap, giveUpAfter
}

// Notify installs the callbacks through which the UI learns about state
// changes and failures. Call once before anything starts.
func (m *Manager) Notify(onStatus func(Status), onError func(string, error)) {
	m.mu.Lock()
	m.onStatus, m.onError = onStatus, onError
	m.mu.Unlock()
}

// Register adds a definition. Registering twice under one name replaces the
// definition (installs change paths, not identity).
func (m *Manager) Register(def Definition) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.defs[def.Name]; !exists {
		m.order = append(m.order, def.Name)
	}
	m.defs[def.Name] = def
}

// Names lists registered services in registration order.
func (m *Manager) Names() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.order...)
}

// Start brings a service up: dependencies first, first-run setup once, a
// runtime smoke check, then the hidden child processes. A failure rolls
// back everything this call started.
func (m *Manager) Start(ctx context.Context, name string) error {
	m.mu.Lock()
	started := []string{}
	err := m.startRec(ctx, name, &started)
	m.mu.Unlock()
	if err != nil {
		for i := len(started) - 1; i >= 0; i-- {
			m.stopLocked(started[i])
		}
	}
	return err
}

// startRec starts name (and, depth first, its dependencies) while the
// manager mutex is held; Start owns the lock.
func (m *Manager) startRec(ctx context.Context, name string, started *[]string) error {
	def, ok := m.defs[name]
	if !ok {
		return &UnknownServiceError{Name: name}
	}
	if _, running := m.running[name]; running {
		return nil
	}
	exe := ""
	if def.Exe != nil {
		exe = def.Exe()
	}
	if exe == "" {
		return &NotInstalledError{Name: name}
	}
	if _, err := os.Stat(exe); err != nil {
		return &NotInstalledError{Name: name}
	}
	if peer := m.groupPeerLocked(def.Group, name); peer != "" {
		return &GroupConflictError{Group: def.Group, Blocking: peer}
	}
	for _, dep := range def.Deps {
		if err := m.startRec(ctx, dep, started); err != nil {
			return fmt.Errorf("%s needs %s: %w", name, dep, err)
		}
		*started = append(*started, dep)
	}
	ring := m.ringFor(name)
	if def.NeedsSetup != nil && def.Setup != nil && def.NeedsSetup() {
		ring.append(fmt.Sprintf("first-run setup for %s…", name))
		if err := def.Setup(ctx, ring.append); err != nil {
			return fmt.Errorf("first-run setup for %s: %w", name, err)
		}
	}
	// A binary that dies before main() needs its redistributable named now,
	// not after three rounds of confusing restart failures — but only for
	// services that actually answer --version.
	if def.SmokeProbe {
		if err := smokeCheck(exe, def.RedistURL); err != nil {
			return err
		}
	}

	count := def.Replicas
	if count < 1 {
		count = 1
	}
	inst := &instance{label: name, startedAt: time.Now()}
	for i := 0; i < count; i++ {
		var args []string
		label := name
		if def.ReplicaArgs != nil {
			args = def.ReplicaArgs(i)
			label = fmt.Sprintf("%s#%d", name, i)
		} else if def.Args != nil {
			args = def.Args()
		}
		if args == nil {
			args = []string{}
		}
		cmd := exec.Command(exe, args...)
		if def.Dir != nil {
			cmd.Dir = def.Dir()
		}
		if cmd.Dir == "" {
			cmd.Dir = parentDir(exe)
		}
		cmd.Env = append(os.Environ(), def.Env...)
		applyHiddenWindow(cmd)
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		cmd.Stderr = cmd.Stdout
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start %s: %w", label, err)
		}
		rep := &replica{label: label, cmd: cmd, dead: make(chan struct{})}
		inst.replicas = append(inst.replicas, rep)
		go func(rep *replica) {
			// The pipe hits EOF when the process exits; Wait reaps it, and
			// only then does the replica count as dead.
			pump(pipe, ring)
			rep.cmd.Wait()
			close(rep.dead)
		}(rep)
		go m.watchReplica(name, def.Internal, rep)
	}
	delete(m.stopFlag, name)
	// The crash clock deliberately survives a restart: it is the restart
	// loop's own memory. A healthy long-lived run clears it in
	// watchReplica, and an explicit Stop clears it in stopLocked.
	m.running[name] = inst
	m.emitStatusLocked(name)
	ring.append(fmt.Sprintf("%s started (pids %v)", name, pidsOf(inst)))
	return nil
}

// groupPeerLocked returns the running member of group other than name.
func (m *Manager) groupPeerLocked(group, name string) string {
	if group == "" {
		return ""
	}
	for peer := range m.running {
		if peer == name {
			continue
		}
		if def, ok := m.defs[peer]; ok && def.Group == group {
			return peer
		}
	}
	return ""
}

// watchReplica reacts to a process exit: intended stops are quiet; worker
// pools restart with backoff; user-facing services report the death.
func (m *Manager) watchReplica(name string, internal bool, rep *replica) {
	<-rep.dead

	m.mu.Lock()
	stopped := m.stopFlag[name]
	inst, alive := m.running[name]
	uptime := time.Duration(0)
	if alive {
		uptime = time.Since(inst.startedAt)
	}
	onError := m.onError
	base, maxWait, giveUp := m.backoffBase, m.backoffCap, m.giveUpAfter
	restartAfter := time.Duration(0)
	shouldRestart := false
	if alive && !stopped {
		if internal {
			clock := m.crashes[name]
			if clock == nil {
				clock = &crashClock{nextWait: base}
				m.crashes[name] = clock
			}
			if uptime >= shortLife {
				// A long-lived start counts as healthy: a later crash
				// begins a fresh clock, not a continuation of the loop.
				clock.quickDeaths = 0
				clock.nextWait = base
			} else {
				clock.quickDeaths++
			}
			if clock.quickDeaths < giveUp {
				shouldRestart = true
				restartAfter = clock.nextWait
				clock.nextWait *= 2
				if clock.nextWait > maxWait {
					clock.nextWait = maxWait
				}
			}
		}
	}
	// A fully dead instance leaves the running map even when a restart is
	// planned — the restart is a fresh Start and needs the slot free.
	if alive && allReplicasDeadLocked(inst) {
		delete(m.running, name)
	}
	m.mu.Unlock()

	if stopped || !alive {
		return
	}
	if !shouldRestart {
		if onError != nil && uptime < shortLife {
			onError(name, fmt.Errorf("%s exited after %s", name, uptime.Round(time.Millisecond)))
		} else if onError != nil {
			onError(name, fmt.Errorf("%s exited", name))
		}
		m.emitStatus(name)
		return
	}
	time.Sleep(restartAfter)
	// The stop may have been requested while we slept; Start re-checks.
	_ = m.Start(context.Background(), name)
}

// Stop ends a service and, afterwards, any of its dependencies nothing else
// is using. Idempotent.
func (m *Manager) Stop(name string) error {
	m.mu.Lock()
	if _, ok := m.defs[name]; !ok {
		m.mu.Unlock()
		return &UnknownServiceError{Name: name}
	}
	m.mu.Unlock()
	m.stopLocked(name)
	return nil
}

func (m *Manager) stopLocked(name string) {
	m.mu.Lock()
	def, ok := m.defs[name]
	inst, running := m.running[name]
	if !ok {
		m.mu.Unlock()
		return
	}
	// The stop intent stays set until the next Start: between killTree and
	// the exit handlers waking up, a dying process must not be mistaken for
	// a crash.
	m.stopFlag[name] = true
	var pids []int
	if running {
		pids = pidsOf(inst)
	}
	delete(m.running, name)
	delete(m.crashes, name)
	ring := m.logs[name]
	m.mu.Unlock()

	for _, pid := range pids {
		killTree(pid)
	}
	if ring != nil {
		ring.append(name + " stopped")
	}
	m.emitStatus(name)

	// Dependencies we started for this service go down too — unless a still
	// running service also lists them.
	for _, dep := range def.Deps {
		if !m.dependentsRunning(dep, name) {
			m.stopLocked(dep)
		}
	}
}

// dependentsRunning reports whether any running service other than `except`
// lists `name` as a dependency.
func (m *Manager) dependentsRunning(name, except string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for running := range m.running {
		if running == except {
			continue
		}
		for _, dep := range m.defs[running].Deps {
			if dep == name {
				return true
			}
		}
	}
	return false
}

// Restart stops then starts, keeping the stop intent out of the crash
// bookkeeping.
func (m *Manager) Restart(ctx context.Context, name string) error {
	if err := m.Stop(name); err != nil {
		return err
	}
	return m.Start(ctx, name)
}

// StopAll ends everything this manager started, dependencies included.
func (m *Manager) StopAll() {
	m.mu.Lock()
	names := make([]string, 0, len(m.running))
	for name := range m.running {
		names = append(names, name)
	}
	m.mu.Unlock()
	sort.Strings(names)
	for _, name := range names {
		m.stopLocked(name)
	}
}

// KillStrays ends processes left behind by a previous run: every registered
// executable that this manager did not start. Called once at app start.
func (m *Manager) KillStrays() {
	m.mu.Lock()
	type stray struct{ name, exe string }
	var strays []stray
	for name, def := range m.defs {
		if _, running := m.running[name]; running || def.Exe == nil {
			continue
		}
		if exe := def.Exe(); exe != "" {
			if _, err := os.Stat(exe); err == nil {
				strays = append(strays, stray{name, exe})
			}
		}
	}
	m.mu.Unlock()
	for _, s := range strays {
		if err := killImage(baseName(s.exe)); err == nil {
			m.ringFor(s.name).append("cleaned up a leftover process from a previous run")
		}
	}
}

// Snapshot returns the current state of every registered service.
func (m *Manager) Snapshot() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(m.order))
	for _, name := range m.order {
		def := m.defs[name]
		entry := Status{Name: name, LabelKey: def.LabelKey, Group: def.Group}
		if def.Exe != nil {
			if exe := def.Exe(); exe != "" {
				if _, err := os.Stat(exe); err == nil {
					entry.Installed = true
					if def.Version != nil {
						entry.Version = def.Version()
					}
				}
			}
		}
		if inst, running := m.running[name]; running {
			entry.Running = true
			entry.PIDs = pidsOf(inst)
		}
		out = append(out, entry)
	}
	return out
}

// Logs returns the tail of a service's output ring.
func (m *Manager) Logs(name string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ring, ok := m.logs[name]; ok {
		return ring.tail()
	}
	return nil
}

// Watch polls the state and calls onChange whenever any service's Status
// changed. Blocks until ctx is done.
func (m *Manager) Watch(ctx context.Context, every time.Duration, onChange func()) {
	last := map[string]Status{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
		changed := false
		for _, st := range m.Snapshot() {
			before, seen := last[st.Name]
			if !seen || statusDiffers(before, st) {
				last[st.Name] = st
				changed = true
			}
		}
		if changed {
			onChange()
		}
	}
}

// statusDiffers compares two snapshots field by field (Status carries a
// slice, so == cannot do it).
func statusDiffers(a, b Status) bool {
	if a.Name != b.Name || a.LabelKey != b.LabelKey || a.Group != b.Group ||
		a.Installed != b.Installed || a.Running != b.Running || a.Version != b.Version {
		return true
	}
	if len(a.PIDs) != len(b.PIDs) {
		return true
	}
	for i := range a.PIDs {
		if a.PIDs[i] != b.PIDs[i] {
			return true
		}
	}
	return false
}

func (m *Manager) ringFor(name string) *ring {
	if ring, ok := m.logs[name]; ok {
		return ring
	}
	ring := &ring{}
	m.logs[name] = ring
	return ring
}

func (m *Manager) emitStatusLocked(name string) {
	// emit without re-locking (caller holds the mutex)
	status := Status{Name: name, LabelKey: m.defs[name].LabelKey, Group: m.defs[name].Group, Running: true}
	if inst, ok := m.running[name]; ok {
		status.Installed = true
		status.PIDs = pidsOf(inst)
		if m.defs[name].Version != nil {
			status.Version = m.defs[name].Version()
		}
	}
	fn := m.onStatus
	if fn != nil {
		go fn(status)
	}
}

func (m *Manager) emitStatus(name string) {
	m.mu.Lock()
	m.emitStatusLocked(name)
	m.mu.Unlock()
}

func allReplicasDeadLocked(inst *instance) bool {
	for _, rep := range inst.replicas {
		select {
		case <-rep.dead:
		default:
			return false
		}
	}
	return true
}

func pidsOf(inst *instance) []int {
	pids := make([]int, 0, len(inst.replicas))
	for _, rep := range inst.replicas {
		if rep.cmd.Process != nil {
			pids = append(pids, rep.cmd.Process.Pid)
		}
	}
	return pids
}

// pump streams a child's output into the service ring, line by line.
func pump(pipe io.Reader, ring *ring) {
	reader := bufio.NewReaderSize(pipe, 64*1024)
	for {
		line, err := reader.ReadString('\n')
		if chunk := strings.TrimRight(line, "\r\n"); chunk != "" {
			ring.append(chunk)
		}
		if err != nil {
			return
		}
	}
}
