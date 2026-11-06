package main

// Kitodock desktop entry point. One binary, several process modes (GUI today;
// mcp, agent hooks, --cron-run, --terminal-window and --command-bar join in
// later phases — see docs/02-arsitektur-kitodock.md §8).

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed all:langs
var langs embed.FS

func main() {
	// Phase 0 runs the GUI only. Non-GUI modes arrive with their phases and
	// must stay cheap: no manager initialization, no window.
	app := NewApp()

	err := wails.Run(&options.App{
		Title:     "Kitodock",
		Width:     1280,
		Height:    760,
		MinWidth:  960,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 11, G: 16, B: 38, A: 1},
		OnStartup:        app.startup,
		OnBeforeClose:    app.beforeClose,
		OnShutdown:       app.shutdown,
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               "kitodock-main-single-instance",
			OnSecondInstanceLaunch: app.onSecondInstance,
		},
		Bind: []interface{}{
			app.System,
		},
		Windows: &windows.Options{
			WebviewIsTransparent:              false,
			WindowIsTranslucent:               true,
			DisableWindowIcon:                 false,
			DisableFramelessWindowDecorations: false,
		},
		Frameless: true,
	})
	if err != nil {
		panic(err)
	}
}
