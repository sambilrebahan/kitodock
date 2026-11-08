package controllers

// The cron + queue worker controller: CRUD scheduled jobs, toggle them,
// run them now, and manage Laravel queue workers per project.

import (
	"fmt"
	"os"

	"kitodock/internal/cron"
	"kitodock/internal/queueworker"
)

// CronQueue owns both the cron engine and the queue workers.
type CronQueue struct {
	bus   *EventBus
	cron  *cron.Manager
	queue *queueworker.Manager
}

// CronServing resolves what cron jobs and queue workers need.
type CronServing interface {
	ProjectPath(name string) string
	PathDirsFor(name string) []string
	PhpExeFor(name string) string
}

func NewCronQueue(bus *EventBus, serving CronServing, persist func([]cron.Job) error) *CronQueue {
	cq := &CronQueue{}

	cq.cron = cron.NewManager(cron.Config{
		Persist:    persist,
		PathDirs:   serving.PathDirsFor,
		WorkDir:    serving.ProjectPath,
		BinaryPath: binaryPath(),
		OnProgress: func(p cron.Progress) {
			bus.Emit(EvCronStatus, p)
		},
	})

	cq.queue = queueworker.NewManager(queueworker.Config{
		PhpExe:  serving.PhpExeFor,
		WorkDir: serving.ProjectPath,
		OnEvent: func(project string, status queueworker.Status) {
			bus.Emit(EvQueueStatus, status)
		},
		OnLog: func(project, line string) {
			bus.Emit(EvQueueLog, map[string]any{"project": project, "line": line})
		},
	})

	cq.bus = bus
	return cq
}

// StartCron begins the engine with persisted jobs.
func (cq *CronQueue) StartCron(jobs []cron.Job) { cq.cron.Start(jobs) }

// StopCron halts the engine.
func (cq *CronQueue) StopCron() { cq.cron.Stop() }

// --- Cron API ---

func (cq *CronQueue) ListCrons() []cron.Job { return cq.cron.List() }

func (cq *CronQueue) SaveCron(job cron.Job) error { return cq.cron.Save(job) }

func (cq *CronQueue) DeleteCron(id string) error { return cq.cron.Delete(id) }

func (cq *CronQueue) ToggleCron(id string, enabled bool) error { return cq.cron.Toggle(id, enabled) }

func (cq *CronQueue) RunCronNow(id string) error { return cq.cron.RunNow(id) }

// --- Queue worker API ---

func (cq *CronQueue) StartQueueWorker(project, args string) error {
	return cq.queue.Start(project, args)
}

func (cq *CronQueue) StopQueueWorker(project string) { cq.queue.Stop(project) }

func (cq *CronQueue) RestartQueueWorker(project, args string) error {
	return cq.queue.Restart(project, args)
}

func (cq *CronQueue) GetQueueWorkerStatus(project string) queueworker.Status {
	return cq.queue.Status(project)
}

func (cq *CronQueue) GetQueueWorkerLogs(project string) []string {
	return cq.queue.Logs(project)
}

func (cq *CronQueue) StopAllQueueWorkers() { cq.queue.StopAll() }

// binaryPath returns this executable's path for Task Scheduler entries.
func binaryPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return exe
}

var _ = fmt.Sprintf
