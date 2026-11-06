package main

// The tray is how a background service app stays reachable after the window
// closes: one icon, per-service start/stop, start/stop everything, quit.
// Menu labels re-render from the catalogues whenever the language changes.

import (
	"sync"

	"github.com/energye/systray"
)

// TrayCallbacks is everything the tray needs from the app; injected so the
// tray never reaches into the controllers directly.
type TrayCallbacks struct {
	// ServiceIDs lists the services the tray offers, in order.
	ServiceIDs []string
	// Label resolves a service id to its display name right now.
	Label func(id string) string
	// Titles are the fixed entries.
	Titles struct {
		Services string
		StartAll string
		StopAll  string
		Show     string
		Quit     string
	}
	Running    func(id string) bool
	OnStart    func(id string)
	OnStop     func(id string)
	OnStartAll func()
	OnStopAll  func()
	OnShow     func()
	OnQuit     func()
}

// trayState owns the mutable menu pieces so relabeling can swap text
// without rebuilding identity.
type trayState struct {
	mu           sync.Mutex
	callbacks    TrayCallbacks
	icon         []byte
	itemServices *systray.MenuItem
	itemStartAll *systray.MenuItem
	itemStopAll  *systray.MenuItem
	itemShow     *systray.MenuItem
	itemQuit     *systray.MenuItem
	serviceItems map[string]*systray.MenuItem
}

var theTray = &trayState{}

// RunTray starts the tray loop; it blocks, so callers wrap it in a
// goroutine. Call StopTray on shutdown.
func RunTray(icon []byte, callbacks TrayCallbacks) {
	theTray.mu.Lock()
	theTray.callbacks = callbacks
	theTray.icon = icon
	theTray.mu.Unlock()
	systray.Run(theTray.onReady, nil)
}

// StopTray tears the icon down (safe if never started).
func StopTray() { systray.Quit() }

// RelabelTray re-renders every menu entry from the current callbacks —
// call it after a language change.
func RelabelTray() { theTray.relabel() }

func (t *trayState) onReady() {
	systray.SetIcon(t.icon)
	systray.SetTitle("Kitodock")
	systray.SetTooltip("Kitodock")

	t.mu.Lock()
	cb := t.callbacks
	t.mu.Unlock()

	t.itemServices = systray.AddMenuItem(cb.Titles.Services, cb.Titles.Services)
	t.itemServices.Disable()

	t.serviceItems = map[string]*systray.MenuItem{}
	for _, id := range cb.ServiceIDs {
		item := systray.AddMenuItem(cb.Label(id), cb.Label(id))
		t.serviceItems[id] = item
		id := id
		item.Click(func() { t.toggleService(id) })
	}

	systray.AddSeparator()
	t.itemStartAll = systray.AddMenuItem(cb.Titles.StartAll, cb.Titles.StartAll)
	t.itemStartAll.Click(func() { t.withCallbacks(func(cb TrayCallbacks) { cb.OnStartAll() }) })
	t.itemStopAll = systray.AddMenuItem(cb.Titles.StopAll, cb.Titles.StopAll)
	t.itemStopAll.Click(func() { t.withCallbacks(func(cb TrayCallbacks) { cb.OnStopAll() }) })
	t.itemShow = systray.AddMenuItem(cb.Titles.Show, cb.Titles.Show)
	t.itemShow.Click(func() { t.withCallbacks(func(cb TrayCallbacks) { cb.OnShow() }) })
	systray.AddSeparator()
	t.itemQuit = systray.AddMenuItem(cb.Titles.Quit, cb.Titles.Quit)
	t.itemQuit.Click(func() { t.withCallbacks(func(cb TrayCallbacks) { cb.OnQuit() }) })
}

func (t *trayState) withCallbacks(do func(TrayCallbacks)) {
	t.mu.Lock()
	cb := t.callbacks
	t.mu.Unlock()
	do(cb)
}

// toggleService routes a service item's click: stop a running service,
// start a stopped one. The label swaps to say which.
func (t *trayState) toggleService(id string) {
	t.withCallbacks(func(cb TrayCallbacks) {
		if cb.Running(id) {
			cb.OnStop(id)
		} else {
			cb.OnStart(id)
		}
	})
}

// relabel rewrites titles and refreshes each service item's text.
func (t *trayState) relabel() {
	t.withCallbacks(func(cb TrayCallbacks) {
		if t.itemQuit == nil {
			return
		}
		t.itemServices.SetTitle(cb.Titles.Services)
		t.itemStartAll.SetTitle(cb.Titles.StartAll)
		t.itemStopAll.SetTitle(cb.Titles.StopAll)
		t.itemShow.SetTitle(cb.Titles.Show)
		t.itemQuit.SetTitle(cb.Titles.Quit)
		for id, item := range t.serviceItems {
			item.SetTitle(cb.Label(id))
		}
	})
}
