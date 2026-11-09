package controllers

// The terminal controller: spawn/write/resize/stop PTY sessions, list
// external terminals, and open them at project folders.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"kitodock/internal/pty"
	"kitodock/internal/terminal"
)

// Terminal manages embedded PTY sessions and external terminal launches.
type Terminal struct {
	bus *EventBus
	pty *pty.Registry
	ctx context.Context
	// serving resolves project paths and runtime dirs.
	serving TerminalServing
	// cmderRoot is the Cmder install root ("" = not installed).
	cmderRoot func() string
	// workspaceStore persists the terminal layout (save/restore).
	workspaceStore WorkspaceStore
}

// WorkspaceStore persists the terminal layout.
type WorkspaceStore interface {
	GetTerminalWorkspace() []byte
	SetTerminalWorkspace(raw []byte) error
}

// TerminalServing is what the terminal controller needs from the app.
type TerminalServing interface {
	ProjectPath(name string) string
	PathDirsFor(name string) []string
}

func NewTerminal(bus *EventBus, serving TerminalServing, cmderRoot func() string, ws WorkspaceStore) *Terminal {
	t := &Terminal{bus: bus, pty: pty.NewRegistry(), serving: serving, cmderRoot: cmderRoot, workspaceStore: ws}
	t.pty.NotifyExit(func(id string) {
		bus.Emit(EvTermExit, map[string]any{"id": id})
	})
	return t
}

// SetContext installs the Wails context.
func (t *Terminal) SetContext(ctx context.Context) { t.ctx = ctx }

// SpawnRequest describes one new terminal pane.
type SpawnRequest struct {
	// Kind: "powershell" | "cmd" | "gitbash" | "cmder" ("" = powershell).
	Kind string `json:"kind"`
	// Project scopes the terminal to a project folder + runtime PATH.
	Project string `json:"project,omitempty"`
	// Cols/Rows are the initial pane size.
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// StartPty spawns a new PTY session and returns its ID.
func (t *Terminal) StartPty(req SpawnRequest) (string, error) {
	exe, args := t.resolveShell(req.Kind)
	if exe == "" {
		return "", fmt.Errorf("terminal %s not found", req.Kind)
	}

	var dir string
	var pathDirs []string
	if req.Project != "" {
		dir = t.serving.ProjectPath(req.Project)
		pathDirs = t.serving.PathDirsFor(req.Project)
	}

	cols, rows := req.Cols, req.Rows
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}

	id, err := t.pty.StartCommand(pty.Spawn{
		Exe: exe, Args: args, Dir: dir,
		PathDirs: pathDirs, Cols: cols, Rows: rows,
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// WritePty sends data to a session.
func (t *Terminal) WritePty(id string, data string) error {
	session, ok := t.pty.Get(id)
	if !ok {
		return fmt.Errorf("terminal %s not found", id)
	}
	_, err := session.Write([]byte(data))
	return err
}

// ResizePty changes a session's pseudoconsole size.
func (t *Terminal) ResizePty(id string, cols, rows uint16) error {
	session, ok := t.pty.Get(id)
	if !ok {
		return fmt.Errorf("terminal %s not found", id)
	}
	return session.Resize(cols, rows)
}

// StopPty ends a session.
func (t *Terminal) StopPty(id string) {
	t.pty.Close(id)
}

// ReadPtyData returns buffered output for a session (polling mode; the
// event-based stream uses term:data:<id>).
func (t *Terminal) ReadPtyData(id string) (string, error) {
	session, ok := t.pty.Get(id)
	if !ok {
		return "", fmt.Errorf("terminal %s not found", id)
	}
	buf := make([]byte, 32*1024)
	n, err := session.Read(buf)
	if n > 0 {
		return string(buf[:n]), nil
	}
	if err != nil {
		return "", err
	}
	return "", nil
}

// ListTerminals returns every external terminal available.
func (t *Terminal) ListTerminals() []terminal.Profile {
	root := ""
	if t.cmderRoot != nil {
		root = t.cmderRoot()
	}
	return terminal.Detect(root)
}

// OpenProjectTerminal launches an external terminal at a project folder.
func (t *Terminal) OpenProjectTerminal(kind, project string) error {
	profiles := t.ListTerminals()
	for _, p := range profiles {
		if string(p.Kind) == kind {
			dir := t.serving.ProjectPath(project)
			if dir == "" {
				dir = "."
			}
			pathDirs := t.serving.PathDirsFor(project)
			return terminal.Open(p.Kind, p.Path, dir, pathDirs)
		}
	}
	return fmt.Errorf("terminal %s not found", kind)
}

// OpenExternalSSH opens the system's default SSH client (for the SSH page).
func (t *Terminal) OpenExternalSSH(command string) error {
	if t.ctx == nil {
		return fmt.Errorf("not ready")
	}
	// Build the terminal command via cmd start.
	exe, args := t.resolveShell("powershell")
	if exe == "" {
		return fmt.Errorf("no terminal available")
	}
	full := append([]string{exe, "-NoExit", "-Command"}, strings.Fields(command)...)
	_ = args
	return terminal.OpenWithCommand("powershell", exe, ".", strings.Join(full[3:], " "), nil)
}

// StreamPtyData begins streaming a session's output as term:data events.
// The frontend calls this once per pane; output flows through the bus.
func (t *Terminal) StreamPtyData(id string) {
	session, ok := t.pty.Get(id)
	if !ok {
		return
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := session.Read(buf)
			if n > 0 {
				t.bus.Emit(EvTermData, map[string]any{
					"id":   id,
					"data": string(buf[:n]),
				})
			}
			if err != nil {
				t.bus.Emit(EvTermExit, map[string]any{"id": id})
				return
			}
		}
	}()
}

// CloseAll ends every PTY (shutdown).
func (t *Terminal) CloseAll() { t.pty.CloseAll() }

// resolveShell maps a kind to an executable and default arguments.
func (t *Terminal) resolveShell(kind string) (string, []string) {
	profiles := t.ListTerminals()
	for _, p := range profiles {
		if string(p.Kind) == kind {
			switch p.Kind {
			case terminal.PowerShell:
				return p.Path, []string{"-NoLogo"}
			case terminal.GitBash:
				return p.Path, []string{"--login", "-i"}
			default:
				return p.Path, nil
			}
		}
	}
	// Default: PowerShell.
	if path := findOnPath("powershell.exe"); path != "" {
		return path, []string{"-NoLogo"}
	}
	if path := findOnPath("cmd.exe"); path != "" {
		return path, nil
	}
	return "", nil
}

func findOnPath(exe string) string {
	profiles := terminal.Detect("")
	for _, p := range profiles {
		if strings.HasSuffix(strings.ToLower(p.Path), strings.ToLower(exe)) {
			return p.Path
		}
	}
	return ""
}

var _ = filepath.Join

// GetTerminalWorkspace returns the persisted layout ("" when unset).
func (t *Terminal) GetTerminalWorkspace() string {
	if t.workspaceStore == nil {
		return ""
	}
	return string(t.workspaceStore.GetTerminalWorkspace())
}

// SaveTerminalWorkspace persists the layout (raw JSON from the frontend).
func (t *Terminal) SaveTerminalWorkspace(raw string) error {
	if t.workspaceStore == nil {
		return nil
	}
	return t.workspaceStore.SetTerminalWorkspace([]byte(raw))
}

// SetDocumentDirtyCount records how many editor buffers are unsaved.
// The lifecycle's beforeClose checks this before allowing the app to quit.
func (t *Terminal) SetDocumentDirtyCount(count int) {
	t.bus.documentDirty.Store(int32(count))
}

// ResolveDocumentShutdown answers the shutdown gate: the frontend calls
// this after the user decides what to do with dirty documents.
func (t *Terminal) ResolveDocumentShutdown(allow bool) {
	t.bus.shutdownResolve <- allow
}

// DocumentDirtyCount reports the current unsaved-buffer count.
func (t *Terminal) DocumentDirtyCount() int {
	return int(t.bus.documentDirty.Load())
}
