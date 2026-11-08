package controllers

// The files + git controllers: hardened file browsing/editing and the git
// source control panel, backed by internal/workspacefiles and
// internal/sourcecontrol.

import (
	"context"
	"path/filepath"
	"sync"

	"kitodock/internal/sourcecontrol"
	"kitodock/internal/workspacefiles"
)

// FilesServing resolves workspace contexts to paths.
type FilesServing interface {
	ProjectPath(name string) string
}

// Files is the file browser/editor controller.
type Files struct {
	mu       sync.Mutex
	services map[string]*workspacefiles.Service // keyed by context name
	serving  FilesServing
}

func NewFiles(serving FilesServing) *Files {
	return &Files{services: map[string]*workspacefiles.Service{}, serving: serving}
}

// serviceFor returns (or lazily creates) the workspacefiles service for a
// context (project name).
func (f *Files) serviceFor(kind, name string) (*workspacefiles.Service, bool) {
	if kind != "project" {
		return nil, false
	}
	root := f.serving.ProjectPath(name)
	if root == "" {
		return nil, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := kind + ":" + name
	svc, ok := f.services[key]
	if !ok {
		svc = workspacefiles.New(root)
		f.services[key] = svc
	}
	return svc, true
}

// GetContext returns the workspace context info for the frontend.
func (f *Files) GetContext(kind, name string) (workspacefiles.RootInfo, string) {
	svc, ok := f.serviceFor(kind, name)
	if !ok {
		return workspacefiles.RootInfo{}, workspacefiles.ResultNotFound
	}
	return workspacefiles.RootInfo{
		Context: workspacefiles.ContextRef{Kind: kind, Name: name},
		Root:    svc.Root(),
		RootKey: kind + ":" + name,
	}, workspacefiles.ResultOK
}

// ListDirectory returns a paginated listing.
func (f *Files) ListDirectory(kind, name, path, cursor string) (workspacefiles.ListResult, string) {
	svc, ok := f.serviceFor(kind, name)
	if !ok {
		return workspacefiles.ListResult{}, workspacefiles.ResultNotFound
	}
	result := svc.List(workspacefiles.ContextRef{Kind: kind, Name: name}, path, cursor)
	return result, workspacefiles.ResultOK
}

// ReadDocument loads a text file with BOM/EOL/revision info.
func (f *Files) ReadDocument(kind, name, path string) (workspacefiles.Document, string) {
	svc, ok := f.serviceFor(kind, name)
	if !ok {
		return workspacefiles.Document{}, workspacefiles.ResultNotFound
	}
	return svc.Read(workspacefiles.ContextRef{Kind: kind, Name: name}, path)
}

// CheckDocument polls for external changes.
func (f *Files) CheckDocument(kind, name, path, revision string) (string, string) {
	svc, ok := f.serviceFor(kind, name)
	if !ok {
		return workspacefiles.ResultNotFound, ""
	}
	return svc.Check(workspacefiles.ContextRef{Kind: kind, Name: name}, path, revision)
}

// SaveDocument writes with optimistic revision checking.
func (f *Files) SaveDocument(kind, name, path, content, revision string) (string, string) {
	svc, ok := f.serviceFor(kind, name)
	if !ok {
		return workspacefiles.ResultNotFound, ""
	}
	return svc.Save(workspacefiles.ContextRef{Kind: kind, Name: name}, path, content, revision)
}

// ReadImage loads an image as base64.
func (f *Files) ReadImage(kind, name, path string) (workspacefiles.ImageResult, string) {
	svc, ok := f.serviceFor(kind, name)
	if !ok {
		return workspacefiles.ImageResult{}, workspacefiles.ResultNotFound
	}
	return svc.ReadImage(workspacefiles.ContextRef{Kind: kind, Name: name}, path)
}

// --- Git controller ------------------------------------------------------------

// Git is the source control panel controller.
type Git struct {
	mu       sync.Mutex
	services map[string]*sourcecontrol.Service
	serving  FilesServing
	bus      *EventBus
}

func NewGit(bus *EventBus, serving FilesServing) *Git {
	g := &Git{services: map[string]*sourcecontrol.Service{}, serving: serving, bus: bus}
	return g
}

func (g *Git) serviceFor(project string) (*sourcecontrol.Service, bool) {
	root := g.serving.ProjectPath(project)
	if root == "" {
		return nil, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	svc, ok := g.services[project]
	if !ok {
		svc = sourcecontrol.New(root)
		svc.Notify(func() {
			g.bus.Emit(EvSourceControl, map[string]any{"project": project})
		})
		g.services[project] = svc
	}
	return svc, true
}

func (g *Git) InspectSourceControl(project string) (map[string]any, string) {
	svc, ok := g.serviceFor(project)
	if !ok {
		return nil, "not_found"
	}
	status, err := svc.Status(context.Background())
	if err != nil {
		return map[string]any{"error": err.Error()}, "error"
	}
	return map[string]any{
		"branch":   status.Branch,
		"upstream": status.Upstream,
		"ahead":    status.Ahead,
		"behind":   status.Behind,
		"files":    status.Files,
	}, "ok"
}

func (g *Git) GetSourceControlStatus(project string) (sourcecontrol.Status, error) {
	svc, ok := g.serviceFor(project)
	if !ok {
		return sourcecontrol.Status{}, nil
	}
	return svc.Status(context.Background())
}

func (g *Git) StartSourceControlStage(project string, paths []string) error {
	svc, ok := g.serviceFor(project)
	if !ok {
		return nil
	}
	return svc.Stage(context.Background(), paths)
}

func (g *Git) StartSourceControlUnstage(project string, paths []string) error {
	svc, ok := g.serviceFor(project)
	if !ok {
		return nil
	}
	return svc.Unstage(context.Background(), paths)
}

func (g *Git) StartSourceControlCommit(project, message string) error {
	svc, ok := g.serviceFor(project)
	if !ok {
		return nil
	}
	return svc.Commit(context.Background(), message)
}

func (g *Git) StartSourceControlPush(project string) error {
	svc, ok := g.serviceFor(project)
	if !ok {
		return nil
	}
	return svc.Push(context.Background())
}

func (g *Git) GetSourceControlOperation(project, opID string) (sourcecontrol.Operation, bool) {
	svc, ok := g.serviceFor(project)
	if !ok {
		return sourcecontrol.Operation{}, false
	}
	return svc.GetOperation(opID)
}

func (g *Git) GetSourceControlBusy(project string) bool {
	svc, ok := g.serviceFor(project)
	if !ok {
		return false
	}
	return svc.Busy()
}

func (g *Git) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, svc := range g.services {
		svc.Close()
	}
}

var _ = filepath.Join
