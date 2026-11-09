package main

// Kitodock desktop entry point. One binary, several process modes (GUI
// today; mcp, agent hooks, --cron-run, --terminal-window and --command-bar
// join in later phases — see docs/02-arsitektur-kitodock.md §8).

import (
	"embed"
	"fmt"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed all:langs
var langs embed.FS

//go:embed build/windows/icon.ico
var trayIcon []byte

// skipElevationEnv lets CI and dev shells run the shell without a UAC
// round-trip; services that need the hosts file or low ports will simply
// say so when they get there.
const skipElevationEnv = "KITODOCK_SKIP_ELEVATE"

func main() {
	// Phase 1 runs the GUI only. Non-GUI modes arrive with their phases and
	// must stay cheap: no manager initialization, no window.
	if os.Getenv(skipElevationEnv) == "" {
		if relaunched, err := relaunchElevated(os.Args); err != nil {
			fmt.Fprintln(os.Stderr, "kitodock:", err)
			os.Exit(1)
		} else if relaunched {
			return
		}
	}

	app := NewApp(trayIcon)

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
			app.Services,
			app.Installer,
			app.Projects,
			app.Dev,
			app.Doctor,
			app.Terminal,
			app.CronQueue,
			app.Files,
			app.Git,
			app.SSH,
			app.Deployments,
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
