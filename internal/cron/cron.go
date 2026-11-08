// Package cron runs user-defined scheduled commands inside the app and,
// for jobs that must fire when the app is closed, registers Windows Task
// Scheduler entries that re-invoke the binary with --cron-run.
package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/robfig/cron/v3"
)

// Job is one scheduled command.
type Job struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Project  string `json:"project,omitempty"` // project name for PATH resolution
	Command  string `json:"command"`           // full command line (cmd /c)
	Schedule string `json:"schedule"`          // cron expression
	Enabled  bool   `json:"enabled"`
	// BackgroundMode registers a Task Scheduler entry so the job fires even
	// when the app is closed; the entry re-runs the binary with --cron-run.
	BackgroundMode bool      `json:"backgroundMode,omitempty"`
	LastRun        time.Time `json:"lastRun,omitempty"`
	LastOutput     string    `json:"lastOutput,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}

// Status is a job's current state for the UI.
type Status struct {
	Job     Job    `json:"job"`
	Running bool   `json:"running"`
	NextRun string `json:"nextRun,omitempty"`
}

// Progress reports a running or finished job.
type Progress struct {
	JobID  string    `json:"jobId"`
	Phase  string    `json:"phase"` // "start" | "output" | "done" | "error"
	Output string    `json:"output,omitempty"`
	At     time.Time `json:"at"`
}

// Config is what the manager needs from the app.
type Config struct {
	// Persist stores the job list (called after every mutation).
	Persist func([]Job) error
	// PathDirs resolves runtime PATH dirs for a job's project.
	PathDirs func(project string) []string
	// WorkDir resolves the working directory for a project.
	WorkDir func(project string) string
	// BinaryPath is this executable's path (Task Scheduler entries use it).
	BinaryPath string
	// OnProgress emits job lifecycle events.
	OnProgress func(Progress)
}

// Manager owns the cron engine and the job list.
type Manager struct {
	mu      sync.Mutex
	jobs    map[string]*Job
	engine  *cron.Cron
	entries map[string]cron.EntryID // job ID → engine entry
	running map[string]bool         // job ID → currently executing
	cfg     Config
}

func NewManager(cfg Config) *Manager {
	parser := cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	return &Manager{
		jobs:    map[string]*Job{},
		engine:  cron.New(cron.WithParser(parser), cron.WithChain()),
		entries: map[string]cron.EntryID{},
		running: map[string]bool{},
		cfg:     cfg,
	}
}

// Start begins the engine and loads persisted jobs.
func (m *Manager) Start(jobs []Job) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.engine.Start()
	for i := range jobs {
		job := jobs[i]
		m.jobs[job.ID] = &job
		if job.Enabled && !job.BackgroundMode {
			m.scheduleLocked(job.ID)
		}
		if job.Enabled && job.BackgroundMode {
			m.ensureTaskLocked(&job)
		}
	}
}

// Stop halts the engine and all running jobs.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx := m.engine.Stop()
	<-ctx.Done()
	for id := range m.running {
		// The executor's context handles the actual kill.
		delete(m.running, id)
	}
}

// List returns all jobs, newest first.
func (m *Manager) List() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		out = append(out, *job)
	}
	// Sort by CreatedAt descending.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].CreatedAt.After(out[i].CreatedAt) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// Save adds or updates a job.
func (m *Manager) Save(job Job) error {
	if job.ID == "" {
		job.ID = fmt.Sprintf("cron-%d", time.Now().UnixNano())
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now()
	}
	// Validate the schedule.
	parser := cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	if _, err := parser.Parse(job.Schedule); err != nil {
		return fmt.Errorf("invalid schedule %q: %w", job.Schedule, err)
	}

	m.mu.Lock()
	m.unscheduleLocked(job.ID)
	m.jobs[job.ID] = &job
	if job.Enabled {
		if job.BackgroundMode {
			m.ensureTaskLocked(&job)
		} else {
			m.scheduleLocked(job.ID)
		}
	}
	m.mu.Unlock()
	return m.persist()
}

// Delete removes a job.
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	m.unscheduleLocked(id)
	delete(m.jobs, id)
	delete(m.running, id)
	m.removeTask(id)
	m.mu.Unlock()
	return m.persist()
}

// Toggle enables or disables a job.
func (m *Manager) Toggle(id string, enabled bool) error {
	m.mu.Lock()
	job, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("job %s not found", id)
	}
	job.Enabled = enabled
	m.unscheduleLocked(id)
	if enabled {
		if job.BackgroundMode {
			m.ensureTaskLocked(job)
		} else {
			m.scheduleLocked(id)
		}
	} else {
		m.removeTask(id)
	}
	m.mu.Unlock()
	return m.persist()
}

// RunNow executes a job immediately, regardless of schedule.
func (m *Manager) RunNow(id string) error {
	m.mu.Lock()
	job, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("job %s not found", id)
	}
	go m.execute(*job)
	return nil
}

// RunOnceFromConfig is the --cron-run entry point: reads config, checks if
// the current minute is a firing minute, executes if so.
func RunOnceFromConfig(configPath, jobID string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var doc struct {
		Crons []Job `json:"crons"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return err
	}
	for _, job := range doc.Crons {
		if job.ID != jobID || !job.Enabled {
			continue
		}
		if !isFiringMinute(job.Schedule, time.Now()) {
			return nil
		}
		m := NewManager(Config{})
		m.execute(job)
		return nil
	}
	return fmt.Errorf("job %s not found or disabled", jobID)
}

// isFiringMinute checks whether the current minute matches the schedule.
func isFiringMinute(schedule string, now time.Time) bool {
	parser := cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	spec, err := parser.Parse(schedule)
	if err != nil {
		return false
	}
	// Check if now is a firing time by seeing if the next fire time after
	// (now - 1 minute) is <= now.
	prev := now.Add(-time.Minute)
	next := spec.Next(prev)
	return !next.After(now)
}

// scheduleLocked adds a job to the engine (mutex held).
func (m *Manager) scheduleLocked(id string) {
	job, ok := m.jobs[id]
	if !ok {
		return
	}
	entryID, err := m.engine.AddFunc(job.Schedule, func() { m.execute(*job) })
	if err != nil {
		return
	}
	m.entries[id] = entryID
}

// unscheduleLocked removes a job from the engine (mutex held).
func (m *Manager) unscheduleLocked(id string) {
	if entryID, ok := m.entries[id]; ok {
		m.engine.Remove(entryID)
		delete(m.entries, id)
	}
}

// execute runs one job with a timeout and kill-tree.
func (m *Manager) execute(job Job) {
	m.mu.Lock()
	if m.running[job.ID] {
		m.mu.Unlock()
		return
	}
	m.running[job.ID] = true
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.running, job.ID)
		if j, ok := m.jobs[job.ID]; ok {
			j.LastRun = time.Now()
		}
		m.mu.Unlock()
		m.persist()
	}()

	m.emit(job.ID, "start", "")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var env []string
	if m.cfg.PathDirs != nil {
		dirs := m.cfg.PathDirs(job.Project)
		if len(dirs) > 0 {
			env = append(os.Environ(), "PATH="+strings.Join(dirs, string(os.PathListSeparator))+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
	}

	cmd := exec.CommandContext(ctx, "cmd", "/c", job.Command)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if m.cfg.WorkDir != nil {
		if dir := m.cfg.WorkDir(job.Project); dir != "" {
			cmd.Dir = dir
		}
	}
	if len(env) > 0 {
		cmd.Env = env
	} else {
		cmd.Env = os.Environ()
	}

	output, err := cmd.CombinedOutput()
	// Cap output at 8 KiB for persistence.
	text := string(output)
	if len(text) > 8192 {
		text = text[:8192]
	}

	m.mu.Lock()
	if j, ok := m.jobs[job.ID]; ok {
		j.LastOutput = text
	}
	m.mu.Unlock()

	if err != nil {
		m.emit(job.ID, "error", text)
	} else {
		m.emit(job.ID, "done", text)
	}
}

// persist saves the job list through the config callback.
func (m *Manager) persist() error {
	if m.cfg.Persist == nil {
		return nil
	}
	return m.cfg.Persist(m.List())
}

// emit fires a progress event.
func (m *Manager) emit(jobID, phase, output string) {
	if m.cfg.OnProgress != nil {
		m.cfg.OnProgress(Progress{JobID: jobID, Phase: phase, Output: output, At: time.Now()})
	}
}

// --- Task Scheduler integration ---------------------------------------------

// ensureTaskLocked registers a per-minute Task Scheduler entry for
// background-mode jobs (mutex held).
func (m *Manager) ensureTaskLocked(job *Job) {
	if m.cfg.BinaryPath == "" {
		return
	}
	taskName := `Kitodock\` + job.ID
	args := []string{
		"/Create", "/F",
		"/TN", taskName,
		"/TR", fmt.Sprintf(`"%s" --cron-run %s`, m.cfg.BinaryPath, job.ID),
		"/SC", "MINUTE", "/MO", "1",
		"/RL", "LIMITED",
	}
	_ = exec.Command("schtasks", args...).Run()
}

// removeTask deletes a Task Scheduler entry.
func (m *Manager) removeTask(id string) {
	if m.cfg.BinaryPath == "" {
		return
	}
	_ = exec.Command("schtasks", "/Delete", "/F", "/TN", `Kitodock\`+id).Run()
}
