// Package controllers holds the Wails-bound API surface, one struct per
// domain (docs/02-arsitektur-kitodock.md §3). The composition root in
// desktop/app.go stays thin: it only wires dependencies into these structs.
//
// events.go is the single source of truth for event names the backend emits
// (doc 02 §3.2): every emitter imports its constant instead of scattering
// string literals across call sites.
package controllers

import (
	"context"
	"sync"
	"sync/atomic"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Event names emitted by the backend. The set grows one phase at a time,
// following the "domain:signal" convention planned in docs/02 §3.2.
const (
	// EvAppNotice is a transient user-facing notice (store corruption,
	// relocation, …) shown by the shell.
	EvAppNotice = "app:notice"
	// EvLanguageChanged announces the active UI language changed — emitted by
	// SetLanguage locally and by the config watcher when another Kitodock
	// process changed it.
	EvLanguageChanged = "language:changed"
	// EvServiceStatus carries the full services snapshot.
	EvServiceStatus = "service:status"
	// EvServiceError reports a service that died or refused to start;
	// payload may carry a "redist" download URL.
	EvServiceError = "service:error"
	// EvInstallProgress streams download/extract heartbeats.
	EvInstallProgress = "install:progress"
	// EvInstallDone reports an install finishing (ok or error).
	EvInstallDone = "install:done"
	// EvStackChanged carries the dashboard-visibility map.
	EvStackChanged = "stack:changed"
	// EvSysMetrics carries a periodic machine sample.
	EvSysMetrics = "sys:metrics"
	// EvProjectChanged announces a project preference change (kind:
	// ssl/php/tld) and asks the UI to refresh.
	EvProjectChanged = "project:changed"
	// EvProjectCreate streams scaffolding progress for a new project.
	EvProjectCreate = "project:create"
	// EvProjectError reports a serving failure for a project.
	EvProjectError = "project:error"
	// EvDevStatus carries one dev server's state.
	EvDevStatus = "devserver:status"
	// EvDoctorOutput streams fix progress lines.
	EvDoctorOutput = "doctor:output"
	// EvTermData carries PTY output chunks (term:data:<id>).
	EvTermData = "term:data"
	// EvTermExit announces a PTY session ended.
	EvTermExit = "term:exit"
	// EvCronStatus carries a cron job's lifecycle.
	EvCronStatus = "cron:status"
	// EvQueueStatus carries a queue worker's state.
	EvQueueStatus = "queueworker:status"
	// EvQueueLog streams queue worker output.
	EvQueueLog = "queueworker:log"
	// EvSourceControl announces a source control operation state change.
	EvSourceControl = "sourcecontrol:operation"
	// EvSSHData carries remote shell output chunks (payload includes id).
	EvSSHData = "ssh:data"
	// EvSSHExit announces a remote shell session ended.
	EvSSHExit = "ssh:exit"
	// EvSSHConnectStage reports one dial stage (payload includes host).
	EvSSHConnectStage = "ssh:connect-stage"
	// EvSSHExternalEdit reports an external-edit watcher action.
	EvSSHExternalEdit = "ssh:external-edit"
	// EvDocumentsShutdownRequest asks the frontend to confirm quitting with
	// unsaved editor buffers.
	EvDocumentsShutdownRequest = "documents:shutdown-request"
	// EvAgentsActivity carries freshly drained agent hook events.
	EvAgentsActivity = "agents:activity"
	// EvAgentsSession announces agent session lifecycle changes.
	EvAgentsSession = "agents:session"
	// EvNotificationsNew announces one inbox item arriving.
	EvNotificationsNew = "notifications:new"
)

// Deployment run events follow deployment:<kind>:<event>:<id> where kind
// is apply | detect | destroy, event is step | line | done, and id is the
// deployment ID — the UI subscribes once per deployment it shows.
const (
	deployEventPrefix = "deployment:"
	// KindApply / KindDetect / KindDestroy name the run kinds in event
	// names; they mirror deployments.Kind values.
	KindApply   = "apply"
	KindDetect  = "detect"
	KindDestroy = "destroy"
)

// DeployEvent builds one deployment event name.
func DeployEvent(kind, event, deploymentID string) string {
	return deployEventPrefix + kind + ":" + event + ":" + deploymentID
}

// SFTP transfer events: sftp:progress:<transferID> and sftp:done:<transferID>.
const (
	sftpProgressPrefix = "sftp:progress:"
	sftpDonePrefix     = "sftp:done:"
)

// EventBus wraps Wails event emission so controllers never touch the runtime
// package directly and events keep working (as no-ops) in tests, where no
// Wails context exists yet.
type EventBus struct {
	mu  sync.RWMutex
	ctx context.Context
	// documentDirty is the unsaved-editor-buffer count for the shutdown gate.
	documentDirty atomic.Int32
	// shutdownResolve receives the user's decision (allow=true) from the
	// frontend after the documents:shutdown-request dialog.
	shutdownResolve chan bool
}

// NewEventBus returns a bus with no context; the app lifecycle installs the
// real one at startup, before any controller can emit.
func NewEventBus() *EventBus { return &EventBus{shutdownResolve: make(chan bool, 1)} }

// SetContext installs (or replaces, on shutdown) the Wails context every
// emission goes through.
func (b *EventBus) SetContext(ctx context.Context) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ctx = ctx
}

// Emit sends name with data to the frontend. It is a no-op when no context is
// installed, which keeps controller unit tests trivial.
func (b *EventBus) Emit(name string, data ...any) {
	b.mu.RLock()
	ctx := b.ctx
	b.mu.RUnlock()
	if ctx == nil {
		return
	}
	wruntime.EventsEmit(ctx, name, data...)
}

// ShutdownResolveCh exposes the resolve channel for the lifecycle's
// beforeClose document gate.
func (b *EventBus) ShutdownResolveCh() <-chan bool { return b.shutdownResolve }
