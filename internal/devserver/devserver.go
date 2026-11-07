// Package devserver runs a project's dev command (`npm run dev` by default)
// as a managed background process and discovers the real port from its log
// output, so the reverse proxy always targets the app server and not the
// asset server.
//
// Port detection is priority-ordered — the first "localhost:NNNN" in a dev
// log is often Vite's asset server, not the app the proxy should forward
// to:
//
//	priority 2 — an explicit app-server line ("Server running on :8000",
//	              e.g. `php artisan serve`)
//	priority 1 — a generic binding line ("Local:", "ready", "listening")
//	priority 0 — a bare localhost:<port>
//
// A higher-priority hit replaces a lower one; a lower one never overrides a
// higher. Port-conflict lines ("in use", EADDRINUSE) are ignored entirely.
package devserver

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Status is one dev server's state for the UI.
type Status struct {
	Running     bool   `json:"running"`
	Port        int    `json:"port,omitempty"`
	Script      string `json:"script,omitempty"`
	PID         int    `json:"pid,omitempty"`
	ProjectName string `json:"projectName"`
}

// StackExes are the only binaries a port cleanup may target.
var StackExes = []string{"php.exe", "php-cgi.exe", "node.exe", "bun.exe"}

// Config carries everything Start needs; the resolvers are functions so
// the app wires its own runtime logic without this package knowing it.
type Config struct {
	ProjectName string
	Path        string
	// Command overrides the default `<pm> run dev` (with {port} token).
	Command string
	// PackageManager resolves the pm binary (npm/pnpm/yarn/bun).
	PackageManager func() string
	// PathDirs prepends runtime dirs to PATH (php, node, composer).
	PathDirs func() []string
	// Env adds extra variables (local TLS, per-project runtime).
	Env func() []string
	// AllowedHosts feeds the Vite 6+ host whitelist.
	AllowedHosts func() []string
}

// process is one running dev server.
type process struct {
	cancel context.CancelFunc
	cmd    *exec.Cmd
	logs   *ring

	portMu       sync.Mutex
	port         int
	bestPriority int

	startedAt time.Time
}

func (p *process) currentPort() int {
	p.portMu.Lock()
	defer p.portMu.Unlock()
	return p.port
}

// observe is the port detector: every output line passes through it.
func (p *process) observe(line string) {
	port, priority, ok := detectPort(line)
	if !ok {
		return
	}
	p.portMu.Lock()
	defer p.portMu.Unlock()
	if priority > p.bestPriority || p.port == 0 {
		p.port = port
		p.bestPriority = priority
	}
}

// Manager owns one dev process per project name.
type Manager struct {
	mu      sync.Mutex
	procs   map[string]*process
	startMu sync.Mutex // serializes launch/stop against port reservation
	onEvent func(project string)
}

func NewManager() *Manager {
	return &Manager{procs: map[string]*process{}}
}

// Notify installs the status-change callback.
func (m *Manager) Notify(onEvent func(project string)) { m.onEvent = onEvent }

func (m *Manager) emit(project string) {
	if m.onEvent != nil {
		go m.onEvent(project)
	}
}

// Start launches (or replaces) a project's dev server and returns the port
// the command was told to use.
func (m *Manager) Start(cfg Config) (int, error) {
	m.startMu.Lock()
	defer m.startMu.Unlock()

	m.mu.Lock()
	if _, running := m.procs[cfg.ProjectName]; running {
		m.mu.Unlock()
		m.stopLocked(cfg.ProjectName)
		m.mu.Lock()
	}
	m.mu.Unlock()

	port := pickFreePort(3000)

	var args []string
	if strings.TrimSpace(cfg.Command) != "" {
		args = []string{"/c"}
		args = append(args, replacePortToken(strings.Fields(cfg.Command), port)...)
	} else {
		script, pm, ok := devCommandFor(cfg)
		if !ok {
			return 0, fmt.Errorf("%s has no dev or start script in package.json", cfg.ProjectName)
		}
		args = []string{"/c", pm, "run", script}
		args = append(args, portFlagsFor(script, port)...)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "cmd", args...)
	cmd.Dir = cfg.Path
	cmd.Env = devEnvironment(cfg, port)
	hide(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return 0, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		cancel()
		return 0, fmt.Errorf("start dev server: %w", err)
	}

	proc := &process{cancel: cancel, cmd: cmd, logs: &ring{}, startedAt: time.Now()}
	m.mu.Lock()
	m.procs[cfg.ProjectName] = proc
	m.mu.Unlock()

	go pump(stdout, proc.logs, proc.observe)
	go func() {
		cmd.Wait()
		m.mu.Lock()
		if current, ok := m.procs[cfg.ProjectName]; ok && current == proc {
			delete(m.procs, cfg.ProjectName)
		}
		m.mu.Unlock()
		m.emit(cfg.ProjectName)
	}()
	m.emit(cfg.ProjectName)
	return port, nil
}

// Status reports one project's dev server.
func (m *Manager) Status(project string) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	proc, ok := m.procs[project]
	if !ok {
		return Status{ProjectName: project}
	}
	return Status{
		Running: true, Port: proc.currentPort(), Script: "dev",
		PID: proc.cmd.Process.Pid, ProjectName: project,
	}
}

// Stop ends a project's dev server and frees its detected port.
func (m *Manager) Stop(project string) {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	m.stopLocked(project)
}

func (m *Manager) stopLocked(project string) {
	m.mu.Lock()
	proc, ok := m.procs[project]
	if !ok {
		m.mu.Unlock()
		return
	}
	delete(m.procs, project)
	m.mu.Unlock()

	pid := proc.cmd.Process.Pid
	proc.cancel()
	killTree(pid)
	if port := proc.currentPort(); port > 0 {
		killPort(port, StackExes...)
	}
	m.emit(project)
}

// StopAll ends everything (app shutdown).
func (m *Manager) StopAll() {
	m.mu.Lock()
	names := make([]string, 0, len(m.procs))
	for name := range m.procs {
		names = append(names, name)
	}
	m.mu.Unlock()
	for _, name := range names {
		m.Stop(name)
	}
}

// Logs returns a project's dev server tail.
func (m *Manager) Logs(project string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	proc, ok := m.procs[project]
	if !ok {
		return nil
	}
	return proc.logs.tail()
}

// Running lists every live dev server.
func (m *Manager) Running() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(m.procs))
	for name, proc := range m.procs {
		out = append(out, Status{
			Running: true, Port: proc.currentPort(),
			PID: proc.cmd.Process.Pid, ProjectName: name,
		})
	}
	return out
}

// --- port detection ---------------------------------------------------------

var (
	appServerKeyword = regexp.MustCompile(`(?i)server[^\n]{0,30}(?:running|started)`)
	bindingKeyword   = regexp.MustCompile(`(?i)(?:local:|ready|listening)`)
	portInText       = regexp.MustCompile(`(?:localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\])?[:\s](\d{2,5})`)
	conflictPattern  = regexp.MustCompile(`(?i)(in use|eaddrinuse|already in use|trying \d+ instead)`)
)

// detectPort returns (port, priority, true) when a line names one.
func detectPort(line string) (int, int, bool) {
	if conflictPattern.MatchString(line) {
		return 0, 0, false
	}
	// The keywords anchor a search window: everything after them is fair
	// game for the port, including IPv4 addresses that carry their own
	// digits ("Server running on http://127.0.0.1:8000").
	if loc := appServerKeyword.FindStringIndex(line); loc != nil {
		if p, ok := portAfter(line[loc[1]:]); ok {
			return p, 2, true
		}
	}
	if loc := bindingKeyword.FindStringIndex(line); loc != nil {
		if p, ok := portAfter(line[loc[1]:]); ok {
			return p, 1, true
		}
	}
	// Bare form: a localhost-ish address with a port.
	if m := regexp.MustCompile(`(?:localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\]):(\d{2,5})`).FindStringSubmatch(line); m != nil {
		if p, err := strconv.Atoi(m[1]); err == nil && validPort(p) {
			return p, 0, true
		}
	}
	return 0, 0, false
}

// portAfter finds the first plausible port number in text.
func portAfter(text string) (int, bool) {
	for _, m := range portInText.FindAllStringSubmatch(text, -1) {
		if p, err := strconv.Atoi(m[1]); err == nil && validPort(p) {
			return p, true
		}
	}
	return 0, false
}

func validPort(p int) bool { return p >= 1024 && p <= 65535 }

// pickFreePort scans upward from preferred for a bindable port.
func pickFreePort(preferred int) int {
	for port := preferred; port < preferred+200; port++ {
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			listener.Close()
			return port
		}
	}
	return preferred
}

// --- command building -------------------------------------------------------

// devCommandFor picks the script (dev, then start) and the pm binary.
func devCommandFor(cfg Config) (script, pm string, ok bool) {
	scripts := readScripts(cfg.Path)
	for _, candidate := range []string{"dev", "start"} {
		if scripts[candidate] {
			pm = "npm"
			if cfg.PackageManager != nil && cfg.PackageManager() != "" {
				pm = cfg.PackageManager()
			}
			return candidate, pm, true
		}
	}
	return "", "", false
}

// readScripts lists which scripts package.json declares.
func readScripts(projectPath string) map[string]bool {
	out := map[string]bool{}
	data, err := os.ReadFile(projectPath + string(os.PathSeparator) + "package.json")
	if err != nil {
		return out
	}
	var doc struct {
		Scripts map[string]any `json:"scripts"`
	}
	if jsonUnmarshal(data, &doc) != nil {
		return out
	}
	for name := range doc.Scripts {
		out[name] = true
	}
	return out
}

// portFlagsFor appends --port only to servers that accept it directly.
func portFlagsFor(script string, port int) []string {
	switch script {
	case "vite", "next", "nuxt", "astro":
		return []string{"--port", strconv.Itoa(port)}
	}
	return nil
}

func replacePortToken(args []string, port int) []string {
	out := make([]string, len(args))
	for i, arg := range args {
		out[i] = strings.ReplaceAll(arg, "{port}", strconv.Itoa(port))
	}
	return out
}

// devEnvironment builds the child env: PATH prefix, PORT, BROWSER=none,
// the Vite 6+ host whitelist, and any injected extras.
func devEnvironment(cfg Config, port int) []string {
	var path string
	if cfg.PathDirs != nil {
		if dirs := cfg.PathDirs(); len(dirs) > 0 {
			path = strings.Join(dirs, string(os.PathListSeparator)) + string(os.PathListSeparator) + os.Getenv("PATH")
		}
	}
	if path == "" {
		path = os.Getenv("PATH")
	}
	env := []string{"PATH=" + path, fmt.Sprintf("PORT=%d", port), "BROWSER=none"}
	if cfg.AllowedHosts != nil {
		if hosts := cfg.AllowedHosts(); len(hosts) > 0 {
			env = append(env, "__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS="+strings.Join(hosts, ","))
		}
	}
	for _, pair := range os.Environ() {
		if strings.HasPrefix(pair, "PATH=") {
			continue
		}
		env = append(env, pair)
	}
	if cfg.Env != nil {
		env = append(env, cfg.Env()...)
	}
	return env
}

// --- plumbing ----------------------------------------------------------------

func pump(stdout interface{ Read([]byte) (int, error) }, logs *ring, observe func(string)) {
	reader := bufio.NewReaderSize(stdout, 64*1024)
	for {
		line, err := reader.ReadString('\n')
		if chunk := strings.TrimRight(line, "\r\n"); chunk != "" {
			logs.append(chunk)
			observe(chunk)
		}
		if err != nil {
			return
		}
	}
}

type ring struct {
	mu    sync.Mutex
	lines []string
}

func (r *ring) append(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.lines) >= 500 {
		copy(r.lines, r.lines[1:])
		r.lines[len(r.lines)-1] = line
		return
	}
	r.lines = append(r.lines, line)
}

func (r *ring) tail() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}
