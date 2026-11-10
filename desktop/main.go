package main

// Kitodock desktop entry point. One binary, several process modes (GUI
// today; mcp, agent hooks, --cron-run, --terminal-window and --command-bar
// join in later phases — see docs/02-arsitektur-kitodock.md §8).

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"kitodock/desktop/controllers"
	"kitodock/internal/agents"
	"kitodock/internal/cron"
	"kitodock/internal/mcpbridge"
	"kitodock/internal/paths"

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
	// Helper personalities run before anything GUI-shaped and never
	// elevate: the MCP bridge serves stdio for an agent client, and the
	// agent-event hook appends one spool line and exits.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--mcp-serve":
			runMCPServe()
			return
		case "--agent-event":
			runAgentEvent(os.Args[2:])
			return
		case "--cron-run":
			runCronOnce(os.Args[2:])
			return
		case "--terminal-window":
			runTerminalWindow(os.Args[2:])
			return
		}
	}

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
			app.Agents,
			app.Notifications,
			app.MCP,
			app.Updates,
			app.CommandCenter,
			app.WindowInfo,
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

// runMCPServe is the stdio bridge personality: an agent client spawned us
// to speak MCP; every tool call goes to the GUI's loopback endpoint.
func runMCPServe() {
	base := os.Getenv("KITODOCK_OWNER_URL")
	token := os.Getenv("KITODOCK_OWNER_TOKEN")
	if base == "" || token == "" {
		fmt.Fprintln(os.Stderr, "kitodock: KITODOCK_OWNER_URL/TOKEN not set")
		os.Exit(1)
	}
	owner := mcpbridge.NewOwnerClient(base, token)
	if err := mcpbridge.Serve(os.Stdin, os.Stdout, owner); err != nil {
		fmt.Fprintln(os.Stderr, "kitodock:", err)
		os.Exit(1)
	}
}

// runAgentEvent is the hook personality: providers invoke it with their
// event payload; we append one line to the spool and leave.
func runAgentEvent(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: kitodock --agent-event <provider> [payload]")
		os.Exit(1)
	}
	event := agents.SpoolEvent{
		Provider: agents.ProviderKind(args[0]),
	}
	if len(args) > 1 {
		event.Payload = args[1]
	} else if stat, err := os.Stdin.Stat(); err == nil && stat.Size() > 0 {
		data, _ := io.ReadAll(os.Stdin)
		event.Payload = string(data)
		if len(event.Payload) > 16*1024 {
			event.Payload = event.Payload[:16*1024]
		}
	}
	// Best-effort extraction of session id and a human hint.
	var probe map[string]any
	if json.Unmarshal([]byte(event.Payload), &probe) == nil {
		for _, key := range []string{"session_id", "sessionId", "thread_id"} {
			if s, ok := probe[key].(string); ok {
				event.SessionID = s
				break
			}
		}
		for _, key := range []string{"prompt", "message", "summary", "last_message"} {
			if s, ok := probe[key].(string); ok && s != "" {
				event.Summary = clipHookSummary(s)
				break
			}
		}
	}
	if err := agents.Spool(filepath.Join(paths.AgentsDir(), "spool"), event); err != nil {
		fmt.Fprintln(os.Stderr, "kitodock:", err)
		os.Exit(1)
	}
}

// clipHookSummary trims a hook hint to one line.
func clipHookSummary(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 140 {
		s = s[:140] + "…"
	}
	return s
}

// runCronOnce is the Task Scheduler personality: background-mode cron
// entries re-invoke this binary every minute; the job's schedule decides
// whether this minute actually fires. Never touches the GUI.
func runCronOnce(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: kitodock --cron-run <job-id>")
		os.Exit(1)
	}
	if err := cron.RunOnceFromConfig(paths.ConfigFile(), args[0]); err != nil {
		// Task Scheduler has no console; failures land in the app log dir.
		fmt.Fprintln(os.Stderr, "kitodock:", err)
		os.Exit(1)
	}
}

// runTerminalWindow is the satellite personality: a borderless window
// hosting exactly one terminal pane whose PTY lives in the main process.
// Credentials arrive through the environment set by the spawner.
func runTerminalWindow(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: kitodock --terminal-window <pty-id>")
		os.Exit(1)
	}
	ptyID := args[0]
	base := os.Getenv("KITODOCK_OWNER_URL")
	token := os.Getenv("KITODOCK_OWNER_TOKEN")
	if base == "" || token == "" {
		fmt.Fprintln(os.Stderr, "kitodock: KITODOCK_OWNER_URL/TOKEN not set")
		os.Exit(1)
	}

	err := wails.Run(&options.App{
		Title:       "Kitodock Terminal",
		Width:       760,
		Height:      480,
		MinWidth:    380,
		MinHeight:   220,
		AssetServer: &assetserver.Options{Assets: assets},
		OnStartup:   func(ctx context.Context) { _ = ctx },
		Bind: []interface{}{
			controllers.NewWindowInfoDetached(ptyID, base, token),
		},
		Windows: &windows.Options{
			WebviewIsTransparent:              false,
			WindowIsTranslucent:               false,
			DisableWindowIcon:                 false,
			DisableFramelessWindowDecorations: false,
		},
		Frameless: true,
	})
	if err != nil {
		panic(err)
	}
}
