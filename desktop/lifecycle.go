package main

// Window lifecycle for Phase 0: install the context, start the cross-process
// language sync, allow closing. Tray, hide-to-tray and the services cascade
// arrive in Phase 1.

import (
	"context"

	"github.com/wailsapp/wails/v2/pkg/options"

	"kitodock/desktop/controllers"
)

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.bus.SetContext(ctx)
	// Language changes made by another Kitodock process arrive through the
	// configuration file; the watcher blocks, so it runs on its own goroutine.
	go controllers.WatchLanguageFor(ctx, a.cfg, a.bus)
}

func (a *App) shutdown(ctx context.Context) {
	_ = ctx
	a.ctx = nil
	a.bus.SetContext(nil)
}

// beforeClose decides whether the window may close. No tray exists yet, so
// closing quits; Phase 1 turns this into hide-to-tray unless quitting.
func (a *App) beforeClose(ctx context.Context) bool {
	_ = ctx
	return false
}

// onSecondInstance surfaces the running main window when the exe is launched
// again (single-instance lock, see main.go).
func (a *App) onSecondInstance(_ options.SecondInstanceData) {
	bringMainWindowUp(a.ctx)
}
