// Package queueworker runs `php artisan queue:work` per project as a
// managed background process with a log ring, kill-tree stop, and status
// events. One worker per project at a time.
package queueworker

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Status is one project's worker state.
type Status struct {
	Project string `json:"project"`
	Running bool   `json:"running"`
	PID     int    `json:"pid,omitempty"`
	Args    string `json:"args,omitempty"`
}

// Config resolves what the worker needs.
type Config struct {
	// PhpExe resolves the project's PHP binary path.
	PhpExe func(project string) string
	// WorkDir resolves the project path.
	WorkDir func(project string) string
	// OnEvent is called on every state change.
	OnEvent func(project string, status Status)
	// OnLog streams output lines.
	OnLog func(project, line string)
}

type worker struct {
	cmd       *exec.Cmd
	cancel    func()
	logs      *ring
	startedAt time.Time
	args      string
}

// Manager owns one worker per project.
type Manager struct {
	mu      sync.Mutex
	workers map[string]*worker
	cfg     Config
}

func NewManager(cfg Config) *Manager {
	return &Manager{workers: map[string]*worker{}, cfg: cfg}
}

// Start launches (or replaces) a project's queue worker.
func (m *Manager) Start(project, extraArgs string) error {
	phpExe := m.cfg.PhpExe(project)
	if phpExe == "" {
		return fmt.Errorf("no PHP runtime resolved for %s", project)
	}
	workDir := m.cfg.WorkDir(project)
	if workDir == "" {
		return fmt.Errorf("project %s not found", project)
	}
	if _, err := os.Stat(workDir + string(os.PathSeparator) + "artisan"); err != nil {
		return fmt.Errorf("%s is not a Laravel project (no artisan)", project)
	}

	m.Stop(project)

	args := []string{phpExe, "artisan", "queue:work"}
	if strings.TrimSpace(extraArgs) != "" {
		args = append(args, strings.Fields(extraArgs)...)
	}

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start queue worker: %w", err)
	}

	w := &worker{cmd: cmd, logs: &ring{}, startedAt: time.Now(), args: extraArgs}
	m.mu.Lock()
	m.workers[project] = w
	m.mu.Unlock()

	go func() {
		reader := bufio.NewReaderSize(stdout, 64*1024)
		for {
			line, err := reader.ReadString('\n')
			if chunk := strings.TrimRight(line, "\r\n"); chunk != "" {
				w.logs.append(chunk)
				if m.cfg.OnLog != nil {
					m.cfg.OnLog(project, chunk)
				}
			}
			if err != nil {
				break
			}
		}
		cmd.Wait()
		m.mu.Lock()
		if current, ok := m.workers[project]; ok && current == w {
			delete(m.workers, project)
		}
		m.mu.Unlock()
		m.emit(project)
	}()
	m.emit(project)
	return nil
}

// Stop ends a project's worker (kill-tree).
func (m *Manager) Stop(project string) {
	m.mu.Lock()
	w, ok := m.workers[project]
	if ok {
		delete(m.workers, project)
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	if w.cmd.Process != nil {
		_ = exec.Command("taskkill", "/F", "/T", "/PID", fmt.Sprint(w.cmd.Process.Pid)).Run()
	}
	m.emit(project)
}

// Restart = Stop + Start.
func (m *Manager) Restart(project, args string) error {
	m.Stop(project)
	return m.Start(project, args)
}

// Status reports one project's worker.
func (m *Manager) Status(project string) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.workers[project]
	if !ok {
		return Status{Project: project}
	}
	return Status{
		Project: project, Running: true,
		PID: w.cmd.Process.Pid, Args: w.args,
	}
}

// Logs returns a project's worker output tail.
func (m *Manager) Logs(project string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.workers[project]
	if !ok {
		return nil
	}
	return w.logs.tail()
}

// StopAll ends everything (shutdown).
func (m *Manager) StopAll() {
	m.mu.Lock()
	projects := make([]string, 0, len(m.workers))
	for p := range m.workers {
		projects = append(projects, p)
	}
	m.mu.Unlock()
	for _, p := range projects {
		m.Stop(p)
	}
}

func (m *Manager) emit(project string) {
	if m.cfg.OnEvent != nil {
		go m.cfg.OnEvent(project, m.Status(project))
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
