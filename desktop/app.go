package main

// App is the composition root: it builds the configuration store, the event
// bus and the domain controllers, and nothing else. Domain logic lives in
// internal/*; the Wails API surface lives on the controllers; this file
// exists to wire them together and hold what the window lifecycle needs.

import (
	"context"
	"encoding/json"
	"fmt"

	"kitodock/desktop/controllers"
	"kitodock/internal/config"
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

	System    *controllers.System
	Services  *controllers.Services
	Installer *controllers.Installer
	Projects  *controllers.Projects
	Dev       *controllers.Dev
	Doctor    *controllers.Doctor
	Terminal  *controllers.Terminal
	CronQueue *controllers.CronQueue

	phpMgr *kphp.Manager

	serving *serving

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
	termCtrl := controllers.NewTerminal(bus, serving, func() string { return paths.CmderRoot() })
	cronQueueCtrl := controllers.NewCronQueue(bus, serving, func(jobs []cron.Job) error {
		return cfg.SetCrons(jobsToRaw(jobs))
	})

	app := &App{
		cfg:       cfg,
		bus:       bus,
		stack:     stack,
		System:    system,
		Services:  servicesCtrl,
		Installer: installer,
		Projects:  projectsCtrl,
		Dev:       devCtrl,
		Doctor:    doctorCtrl,
		Terminal:  termCtrl,
		CronQueue: cronQueueCtrl,
		serving:   serving,
		phpMgr:    phpMgr,
		trayIcon:  trayIcon,
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
