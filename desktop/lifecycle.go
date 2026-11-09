package main

// Window lifecycle for Phase 1: install the context, sweep leftover
// processes, stream service status and machine metrics, run the tray, and
// turn window-close into hide-to-tray unless the user quits for real.

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/options"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"kitodock/desktop/controllers"
	"kitodock/internal/i18n"
)

var quitting atomic.Bool

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.bus.SetContext(ctx)
	a.Services.SetContext(ctx)
	a.Installer.SetContext(ctx)
	a.Projects.SetContext(ctx)

	// Language changes made by another Kitodock process arrive through the
	// configuration file; the watcher blocks, so it runs on its own goroutine.
	go controllers.WatchLanguageFor(ctx, a.cfg, a.bus)

	// Processes a crashed run left behind must not hold ports when this run
	// starts; sweep them before anything binds.
	go a.stack.KillStrays()

	// Self-heal every project's serving configuration (vhosts, hosts
	// entries, certificates, php pools) after a move or partial failure.
	go a.serving.Sweep(ctx)

	// Start the cron engine with persisted jobs.
	a.Terminal.SetContext(ctx)
	a.CronQueue.StartCron(rawToJobs(a.cfg.Get().Crons))

	// Stream what changed, both to the window and into the tray's labels.
	go a.stack.Watch(ctx, 1500*time.Millisecond, func() {
		a.bus.Emit(controllers.EvServiceStatus, a.stack.Snapshot())
		RelabelTray()
	})
	go func() {
		ticker := time.NewTicker(1500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.bus.Emit(controllers.EvSysMetrics, a.System.GetSystemMetrics())
			}
		}
	}()

	go RunTray(a.trayIcon, a.trayCallbacks())
}

// trayCallbacks builds the tray's behavior in terms of what exists now.
func (a *App) trayCallbacks() TrayCallbacks {
	cb := TrayCallbacks{
		ServiceIDs: a.stack.Names(),
		Running: func(id string) bool {
			for _, st := range a.stack.Snapshot() {
				if st.Name == id {
					return st.Running
				}
			}
			return false
		},
	}
	cb.Label = func(id string) string {
		for _, st := range a.stack.Snapshot() {
			if st.Name == id {
				label := i18n.Text(st.LabelKey, nil)
				verb := i18n.Text("tray.start", nil)
				if st.Running {
					verb = i18n.Text("tray.stop", nil)
				}
				return verb + " " + label
			}
		}
		return id
	}
	cb.Titles.Services = i18n.Text("tray.services", nil)
	cb.Titles.StartAll = i18n.Text("tray.startAll", nil)
	cb.Titles.StopAll = i18n.Text("tray.stopAll", nil)
	cb.Titles.Show = i18n.Text("tray.show", nil)
	cb.Titles.Quit = i18n.Text("tray.quit", nil)
	cb.OnStart = func(id string) { _ = a.Services.StartService(id) }
	cb.OnStop = func(id string) { _ = a.Services.StopService(id) }
	cb.OnStartAll = func() { _ = a.Services.StartVisible() }
	cb.OnStopAll = func() { _ = a.Services.StopVisible() }
	cb.OnShow = func() { bringMainWindowUp(a.ctx) }
	cb.OnQuit = a.quitFromTray
	return cb
}

func (a *App) shutdown(ctx context.Context) {
	_ = ctx
	quitting.Store(true)
	a.Dev.StopAllDev()
	a.CronQueue.StopCron()
	a.CronQueue.StopAllQueueWorkers()
	a.Terminal.CloseAll()
	a.SSH.CloseAllSSH()
	a.stack.StopAll()
	StopTray()
	a.ctx = nil
	a.bus.SetContext(nil)
}

// beforeClose hides the window instead of closing it: a services app lives
// in the tray. Closing for real goes through QuitApp or the tray's quit,
// both of which set the quitting flag first. When quitting with unsaved
// editor buffers, the document gate asks the user before proceeding.
func (a *App) beforeClose(ctx context.Context) bool {
	if quitting.Load() {
		return false
	}
	// Document shutdown gate: if unsaved buffers exist, ask the frontend
	// to confirm; the user's answer arrives through the resolve channel.
	if a.Terminal.DocumentDirtyCount() > 0 {
		a.bus.Emit(controllers.EvDocumentsShutdownRequest, a.Terminal.DocumentDirtyCount())
		select {
		case allow := <-a.bus.ShutdownResolveCh():
			if !allow {
				return true // prevent close
			}
			// User chose to discard: proceed to quit path below.
		case <-time.After(30 * time.Second):
			// Timeout: prevent close (safe default).
			return true
		}
		quitting.Store(true)
		a.Dev.StopAllDev()
		a.CronQueue.StopCron()
		a.CronQueue.StopAllQueueWorkers()
		a.Terminal.CloseAll()
		a.stack.StopAll()
		StopTray()
		return false
	}
	if a.ctx != nil {
		wailsruntime.WindowHide(a.ctx)
	}
	return true
}

// quitFromTray exits: show nothing, stop everything, close the window.
func (a *App) quitFromTray() {
	quitting.Store(true)
	a.stack.StopAll()
	StopTray()
	if a.ctx != nil {
		wailsruntime.Quit(a.ctx)
	}
}

// onSecondInstance surfaces the running main window when the exe is
// launched again (single-instance lock, see main.go).
func (a *App) onSecondInstance(_ options.SecondInstanceData) {
	bringMainWindowUp(a.ctx)
}
