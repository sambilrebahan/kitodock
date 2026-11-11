package main

// mcpdeps adapts the app's domain objects to the MCP controller's
// dependency bundle: the tools see projects, services, the database,
// application-context captures and Mailpit, without importing the
// controllers package themselves.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"kitodock/desktop/controllers"
	"kitodock/internal/applicationcontext"
	"kitodock/internal/paths"
	"kitodock/internal/services"
)

// mcpDeps implements controllers.MCPDeps over the live stack and serving.
type mcpDeps struct {
	serving *serving
	stack   *services.Manager
	emit    func(event string, data map[string]any)
}

func (d *mcpDeps) Projects() []controllers.MCPProject {
	out := []controllers.MCPProject{}
	for _, p := range d.serving.ListProjects() {
		scheme := "http"
		if d.serving.ProjectSSL(p.Name) {
			scheme = "https"
		}
		out = append(out, controllers.MCPProject{
			Name: p.Name,
			URL:  scheme + "://" + d.serving.HostnameOf(p.Name),
			Kind: string(p.Kind),
		})
	}
	return out
}

func (d *mcpDeps) Services() []controllers.MCPService {
	out := []controllers.MCPService{}
	for _, s := range d.stack.Snapshot() {
		out = append(out, controllers.MCPService{
			Name: s.Name, Running: s.Running, Installed: s.Installed,
		})
	}
	return out
}

func (d *mcpDeps) StartService(name string) error {
	return d.stack.Start(context.Background(), name)
}

func (d *mcpDeps) StopService(name string) error {
	return d.stack.Stop(name)
}

func (d *mcpDeps) ExecProject(project, command string) (string, error) {
	dir := d.serving.ProjectPath(project)
	if dir == "" {
		return "", fmt.Errorf("project %q not found", project)
	}
	return controllers.RunProjectExec(dir, command)
}

// Databases lists schemas via the bundled mysql client; the server must
// be running.
func (d *mcpDeps) Databases() ([]string, error) {
	for _, s := range d.stack.Snapshot() {
		if s.Name == "mysql" && !s.Running {
			return nil, fmt.Errorf("mysql is not running; start it first")
		}
	}
	mysqlBin := filepath.Join(paths.MySQLDir(), "bin", "mysql.exe")
	if _, err := os.Stat(mysqlBin); err != nil {
		return nil, fmt.Errorf("mysql client not installed")
	}
	out, err := exec.Command(mysqlBin, "-u", "root", "-N", "-e", "SHOW DATABASES").Output()
	if err != nil {
		return nil, fmt.Errorf("mysql query: %w", err)
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

func (d *mcpDeps) CaptureContext(project string) (*applicationcontext.Snapshot, error) {
	dir := d.serving.ProjectPath(project)
	if dir == "" {
		return nil, fmt.Errorf("project %q not found", project)
	}
	phpBin := "php"
	if dirs := d.serving.PathDirsFor(project); len(dirs) > 0 {
		if cand := filepath.Join(dirs[0], "php.exe"); fileExists(cand) {
			phpBin = cand
		}
	}
	snap, err := applicationcontext.Capture(project, dir, phpBin, applicationcontext.ExecRunner)
	if err != nil {
		return nil, err
	}
	if _, err := applicationcontext.AppendLog(
		filepath.Join(paths.AgentsDir(), "context"), project, snap); err != nil {
		return nil, err
	}
	return snap, nil
}

// Mailbox reads Mailpit's default UI port.
func (d *mcpDeps) Mailbox(limit int) ([]applicationcontext.MailSummary, error) {
	fetch := &applicationcontext.FetchMailpit{BaseURL: "http://127.0.0.1:8025"}
	return fetch.List(limit)
}

func (d *mcpDeps) Navigate(target string) {
	d.emit("commandcenter:navigate", map[string]any{"target": target})
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
