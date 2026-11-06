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
)

// EventBus wraps Wails event emission so controllers never touch the runtime
// package directly and events keep working (as no-ops) in tests, where no
// Wails context exists yet.
type EventBus struct {
	mu  sync.RWMutex
	ctx context.Context
}

// NewEventBus returns a bus with no context; the app lifecycle installs the
// real one at startup, before any controller can emit.
func NewEventBus() *EventBus { return &EventBus{} }

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
