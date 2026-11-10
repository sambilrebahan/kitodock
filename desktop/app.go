package main

// App is the composition root: it builds the configuration store, the event
// bus and the domain controllers, and nothing else. Domain logic lives in
// internal/*; the Wails API surface lives on the controllers; this file
// exists to wire them together and hold what the window lifecycle needs.

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"kitodock/desktop/controllers"
	"kitodock/internal/config"
	"kitodock/internal/credentials"
	"kitodock/internal/cron"
	knode "kitodock/internal/nodejs"
	"kitodock/internal/paths"
	kphp "kitodock/internal/php"
	"kitodock/internal/services"
)

type App struct {
	ctx context.Context
	cfg *config.Store
	bus *controllers.EventBus

	stack *services.Manager

	System        *controllers.System
	Services      *controllers.Services
	Installer     *controllers.Installer
	Projects      *controllers.Projects
	Dev           *controllers.Dev
	Doctor        *controllers.Doctor
	Terminal      *controllers.Terminal
	CronQueue     *controllers.CronQueue
	Files         *controllers.Files
	Git           *controllers.Git
	SSH           *controllers.SSH
	Deployments   *controllers.Deployments
	Agents        *controllers.Agents
	Notifications *controllers.Notifications
	MCP           *controllers.MCP
	Updates       *controllers.Updates
	CommandCenter *controllers.CommandCenter

	phpMgr *kphp.Manager

	serving *serving

	// agentsDrain stops the agent hook spool pump at shutdown.
	agentsDrain func()

	trayIcon []byte
}

// recommendedStack is the starter set "Bootstrap the stack" offers.
var recommendedStack = []string{"nginx", "mysql", "mailpit"}

// NewApp wires the Phase 1 graph: configuration → event bus → controllers,
// with catalogues installed before anything else can render a message and
// the service stack registered before the window exists.
func NewApp(trayIcon []byte) *App {
	cfg, err := config.Open(paths.ConfigFile())
	if err != nil {
		panic(fmt.Sprintf("load configuration: %v", err))
	}
	bus := controllers.NewEventBus()
	stack := buildStack()
	installer := controllers.NewInstaller(bus)
	system := controllers.NewSystem(cfg, bus)
	controllers.InstallCatalogues(langs, system.GetLanguage)
	servicesCtrl := controllers.NewServices(bus, cfg, stack, installer, recommendedStack)

	phpMgr := kphp.New(paths.PHPDir())
	nodeMgr := knode.New(paths.NodeDir())
	WireLocalCA()
	serving := newServing(cfg, stack, phpMgr, realHostsFile())
	projectsCtrl := controllers.NewProjects(bus, cfg, serving, phpMgr, nodeMgr)
	devCtrl := controllers.NewDev(bus, serving)
	doctorCtrl := controllers.NewDoctor(bus, serving.DoctorTarget)
	filesCtrl := controllers.NewFiles(serving)
	gitCtrl := controllers.NewGit(bus, serving)
	termCtrl := controllers.NewTerminal(bus, serving, func() string { return paths.CmderRoot() }, cfg)
	cronQueueCtrl := controllers.NewCronQueue(bus, serving, func(jobs []cron.Job) error {
		return cfg.SetCrons(jobsToRaw(jobs))
	})
	sshCtrl := controllers.NewSSH(bus, credentials.System{}, paths.SSHDir)
	deployCtrl, err := controllers.NewDeployments(
		bus, filepath.Join(paths.DeploymentsDir(), "index.json"), credentials.System{}, sshCtrl)
	if err != nil {
		panic(fmt.Sprintf("open deployment store: %v", err))
	}

	// Phase 6: agents, MCP endpoint, updater and the command center.
	agentsCtrl, err := controllers.NewAgents(bus, termCtrl.Registry(), serving)
	if err != nil {
		panic(fmt.Sprintf("agent manager: %v", err))
	}

	// The notification inbox behind the sidebar bell; service failures
	// land in it through the Services controller hook.
	notificationsCtrl, err := controllers.NewNotifications(bus)
	if err != nil {
		panic(fmt.Sprintf("notification store: %v", err))
	}
	servicesCtrl.SetNotifier(func(name string, err error) {
		notificationsCtrl.Push("failed", name, err.Error())
	})
	projectsCtrl.SetCreateServing(serving)
	// Agent sessions ride the same PTY registry as the terminal
	// workspace; exits feed both the pane-close event and the agent bookkeeping.
	termCtrl.Registry().NotifyExit(func(ptyID string) {
		bus.Emit("term:exit", map[string]any{"id": ptyID})
		agentsCtrl.HandlePtyExit(ptyID)
	})

	deps := &mcpDeps{serving: serving, stack: stack,
		emit: func(event string, data map[string]any) { bus.Emit(event, data) }}
	mcpCtrl := controllers.NewMCP(bus, deps, cfg.SetMCPEnabled)
	updatesCtrl := controllers.NewUpdates(bus, "sambilrebahan/kitodock")
	commandCenter := controllers.NewCommandCenter(bus,
		func() controllers.CommandContext {
			ctx := controllers.CommandContext{
				Pages:    []string{"dashboard", "projects", "terminal", "ssh", "deployments", "agents", "cron", "downloads", "settings"},
				Services: stack.Names(),
			}
			for _, p := range serving.ListProjects() {
				ctx.Projects = append(ctx.Projects, p.Name)
			}
			if hosts, err := sshCtrl.ListHosts(); err == nil {
				for _, h := range hosts {
					ctx.Hosts = append(ctx.Hosts, h.Name)
				}
			}
			for _, p := range agentsCtrl.AgentProviders() {
				if p.Path != "" {
					ctx.Agents = append(ctx.Agents, string(p.Provider.Kind))
				}
			}
			return ctx
		},
		func(target string) { bus.Emit("commandcenter:navigate", map[string]any{"target": target}) },
		map[string]func(string) error{
			"service:start": func(name string) error { return stack.Start(context.Background(), name) },
			"service:stop":  func(name string) error { return stack.Stop(name) },
			// Pane-creating intents execute in the frontend, which owns
			// the workspace state; the backend just announces them.
			"terminal": func(project string) error {
				bus.Emit("commandcenter:run", map[string]any{"kind": "terminal", "project": project})
				return nil
			},
			"ssh": func(host string) error {
				bus.Emit("commandcenter:run", map[string]any{"kind": "ssh", "host": host})
				return nil
			},
			"agent": func(provider string) error {
				bus.Emit("commandcenter:run", map[string]any{"kind": "agent", "provider": provider})
				return nil
			},
		},
	)

	app := &App{
		cfg:           cfg,
		bus:           bus,
		stack:         stack,
		System:        system,
		Services:      servicesCtrl,
		Installer:     installer,
		Projects:      projectsCtrl,
		Dev:           devCtrl,
		Doctor:        doctorCtrl,
		Terminal:      termCtrl,
		CronQueue:     cronQueueCtrl,
		Files:         filesCtrl,
		Git:           gitCtrl,
		SSH:           sshCtrl,
		Deployments:   deployCtrl,
		Agents:        agentsCtrl,
		Notifications: notificationsCtrl,
		MCP:           mcpCtrl,
		Updates:       updatesCtrl,
		CommandCenter: commandCenter,
		serving:       serving,
		phpMgr:        phpMgr,
		trayIcon:      trayIcon,
	}
	return app
}

// jobsToRaw converts typed cron jobs to the raw config representation
// (config stores []map[string]any to avoid importing cron from config).
func jobsToRaw(jobs []cron.Job) []map[string]any {
	out := make([]map[string]any, len(jobs))
	for i, job := range jobs {
		data, err := json.Marshal(job)
		if err != nil {
			continue
		}
		var m map[string]any
		if json.Unmarshal(data, &m) == nil {
			out[i] = m
		}
	}
	return out
}

// rawToJobs converts the raw config representation back to typed jobs.
func rawToJobs(raw []map[string]any) []cron.Job {
	out := make([]cron.Job, 0, len(raw))
	for _, m := range raw {
		data, err := json.Marshal(m)
		if err != nil {
			continue
		}
		var job cron.Job
		if json.Unmarshal(data, &job) == nil {
			out = append(out, job)
		}
	}
	return out
}

// Config exposes the store to the lifecycle (relocation repair lands in a
// later phase). Controllers received theirs at construction.
func (a *App) Config() *config.Store { return a.cfg }
