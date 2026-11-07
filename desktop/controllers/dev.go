package controllers

// The dev server controller: start/stop a project's dev command, stream
// logs, and read the detected port for the proxy.

import (
	"fmt"
	"path/filepath"

	"kitodock/internal/devserver"
	"kitodock/internal/localtls"
)

// Dev is the dev-server controller.
type Dev struct {
	bus *EventBus
	mgr *devserver.Manager
	// serving integrates with project serving (hostname, SSL, paths).
	serving DevServing
}

// DevServing is what the dev controller needs from the app's wiring.
type DevServing interface {
	// ProjectPath resolves a project name to its on-disk path.
	ProjectPath(name string) string
	// ProjectSSL reports whether a project serves HTTPS.
	ProjectSSL(name string) bool
	// HostnameOf builds a project's full hostname.
	HostnameOf(name string) string
	// PathDirsFor returns the runtime PATH dirs for a project.
	PathDirsFor(name string) []string
	// CaCertPath returns the local CA bundle path ("" = none).
	CaCertPath() string
	// PackageManagerFor resolves a project's pm binary.
	PackageManagerFor(name string) string
}

func NewDev(bus *EventBus, serving DevServing) *Dev {
	d := &Dev{bus: bus, mgr: devserver.NewManager(), serving: serving}
	d.mgr.Notify(func(project string) {
		bus.Emit(EvDevStatus, d.mgr.Status(project))
	})
	return d
}

// StartDev launches a project's dev server (async; events follow).
func (d *Dev) StartDev(project string) error {
	path := d.serving.ProjectPath(project)
	if path == "" {
		return fmt.Errorf("project %s not found", project)
	}
	hostname := d.serving.HostnameOf(project)

	// Local TLS env for the dev server subprocess.
	var tlsEnv []string
	if caPath := d.serving.CaCertPath(); caPath != "" {
		certsDir := filepath.Dir(caPath)
		leafCrt := filepath.Join(certsDir, hostname+".crt")
		leafKey := filepath.Join(certsDir, hostname+".key")
		tlsEnv = localtls.Environment(nil, caPath, localtls.Project{
			Path: path, Hostname: hostname, CertPath: leafCrt, KeyPath: leafKey,
		})
	}

	_, err := d.mgr.Start(devserver.Config{
		ProjectName:    project,
		Path:           path,
		PathDirs:       func() []string { return d.serving.PathDirsFor(project) },
		Env:            func() []string { return tlsEnv },
		PackageManager: func() string { return d.serving.PackageManagerFor(project) },
		AllowedHosts:   func() []string { return []string{hostname, "localhost"} },
	})
	return err
}

// StopDev ends a project's dev server.
func (d *Dev) StopDev(project string) { d.mgr.Stop(project) }

// DevStatus reports one project's dev server.
func (d *Dev) DevStatus(project string) devserver.Status { return d.mgr.Status(project) }

// DevLogs streams a project's dev server tail.
func (d *Dev) DevLogs(project string) []string { return d.mgr.Logs(project) }

// DevRunning lists all live dev servers.
func (d *Dev) DevRunning() []devserver.Status { return d.mgr.Running() }

// StopAllDev ends everything (shutdown).
func (d *Dev) StopAllDev() { d.mgr.StopAll() }
