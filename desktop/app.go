package main

// App is the composition root: it builds the configuration store, the event
// bus and the domain controllers, and nothing else. Domain logic lives in
// internal/*; the Wails API surface lives on the controllers; this file
// exists to wire them together and hold what the window lifecycle needs.

import (
	"context"
	"fmt"

	"kitodock/desktop/controllers"
	"kitodock/internal/config"
	"kitodock/internal/paths"
)

type App struct {
	ctx    context.Context
	cfg    *config.Store
	bus    *controllers.EventBus
	System *controllers.System
}

// NewApp wires the Phase 0 graph: configuration → event bus → system
// controller, with catalogues installed before anything else can render a
// message.
func NewApp() *App {
	cfg, err := config.Open(paths.ConfigFile())
	if err != nil {
		panic(fmt.Sprintf("load configuration: %v", err))
	}
	bus := controllers.NewEventBus()
	system := controllers.NewSystem(cfg, bus)
	system.InstallCatalogues(langs)
	return &App{cfg: cfg, bus: bus, System: system}
}

// Config exposes the store to the lifecycle (relocation repair lands in a
// later phase). Controllers received theirs at construction.
func (a *App) Config() *config.Store { return a.cfg }
