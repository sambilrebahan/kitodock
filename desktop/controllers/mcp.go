package controllers

// The MCP controller: owns the loopback owner endpoint, registers the
// Kitodock MCP server into agent clients (Claude Code, Codex, Cursor),
// and dispatches the tool methods the bridge forwards. Destructive
// methods arrive pre-gated (the bridge enforces the confirm argument).

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"kitodock/internal/applicationcontext"
	"kitodock/internal/mcpclient"
	"kitodock/internal/ownerhttp"
	"kitodock/internal/paths"
)

// MCP owns the endpoint and registrations.
type MCP struct {
	bus   *EventBus
	owner *ownerhttp.Server
	deps  MCPDeps
	// term carries the detached-window stream methods.
	term TermDeps
	// setEnabled persists the preference so the next start resumes it.
	setEnabled func(bool) error
}

// TermDeps is the terminal stream detached windows ride on; nil when the
// composition root has no terminal controller.
type TermDeps interface {
	TermPoll(ptyID string, after uint64) (chunks []termChunk, exited bool)
	TermWrite(ptyID, data string) error
	TermResize(ptyID string, cols, rows uint16) error
}

// MCPDeps supplies the operations the tools expose; the composition root
// wires them to the real controllers.
type MCPDeps interface {
	// Projects lists managed projects as name+url pairs.
	Projects() []MCPProject
	// Services lists service names with running state.
	Services() []MCPService
	// StartService / StopService control the stack.
	StartService(name string) error
	StopService(name string) error
	// ExecProject runs a short command in a project folder and returns
	// combined output.
	ExecProject(project, command string) (string, error)
	// Databases lists database names of the local database service.
	Databases() ([]string, error)
	// CaptureContext snapshots a project's application context.
	CaptureContext(project string) (*applicationcontext.Snapshot, error)
	// Mailbox lists recent Mailpit messages.
	Mailbox(limit int) ([]applicationcontext.MailSummary, error)
	// Navigate asks the UI to focus something (page/project).
	Navigate(target string)
}

// MCPProject / MCPService are the tool-facing projections.
type MCPProject struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
	Kind string `json:"kind,omitempty"`
}

type MCPService struct {
	Name      string `json:"name"`
	Running   bool   `json:"running"`
	Installed bool   `json:"installed"`
}

// NewMCP builds the controller around a dependency bundle; setEnabled
// persists the on/off preference (nil disables persistence).
func NewMCP(bus *EventBus, deps MCPDeps, setEnabled func(bool) error) *MCP {
	return &MCP{bus: bus, deps: deps, setEnabled: setEnabled}
}

// SetTermDeps installs the terminal bridge for detached windows.
func (c *MCP) SetTermDeps(term TermDeps) { c.term = term }

// EnsureEndpoint starts the loopback endpoint if it is not already
// running and returns its base URL plus the bearer tokens, one per
// source: helpers hold the token of the personality they run as, and the
// server authorizes each token against that source's method allowlist.
func (c *MCP) EnsureEndpoint() (base, mcpToken, termToken string, err error) {
	if c.owner == nil {
		auditPath := filepath.Join(paths.AgentsDir(), "mcp-audit.jsonl")
		c.owner = ownerhttp.NewServer(c.dispatch, auditPath)
	}
	base, mcpToken, err = c.owner.Start()
	if err != nil {
		return "", "", "", err
	}
	return base, mcpToken, c.owner.Token("terminal-window"), nil
}

// MCPStatus is the settings-facing state.
type MCPStatus struct {
	Enabled bool              `json:"enabled"`
	URL     string            `json:"url,omitempty"`
	Clients []MCPClientStatus `json:"clients"`
}

// MCPClientStatus reports one client's registration.
type MCPClientStatus struct {
	Client     mcpclient.Client `json:"client"`
	Label      string           `json:"label"`
	ConfigPath string           `json:"configPath"`
	Registered bool             `json:"registered"`
}

func clientLabels() map[mcpclient.Client]string {
	return map[mcpclient.Client]string{
		mcpclient.ClaudeCode: "Claude Code",
		mcpclient.CodexCLI:   "Codex CLI",
		mcpclient.Cursor:     "Cursor",
	}
}

// MCPStatus reports the endpoint + registrations.
func (c *MCP) MCPStatus() MCPStatus {
	home := userHome()
	labels := clientLabels()
	status := MCPStatus{Enabled: c.owner != nil && c.owner.URL() != ""}
	if status.Enabled {
		status.URL = c.owner.URL()
	}
	for _, client := range []mcpclient.Client{mcpclient.ClaudeCode, mcpclient.CodexCLI, mcpclient.Cursor} {
		status.Clients = append(status.Clients, MCPClientStatus{
			Client: client, Label: labels[client],
			Registered: mcpclient.Registered(client, home),
		})
	}
	return status
}

// EnableMCP starts the loopback endpoint and registers the server into
// every supported client, keeping backups.
func (c *MCP) EnableMCP() error {
	if c.owner == nil {
		auditPath := filepath.Join(paths.AgentsDir(), "mcp-audit.jsonl")
		c.owner = ownerhttp.NewServer(c.dispatch, auditPath)
	}
	base, token, err := c.owner.Start()
	if err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	spec := mcpclient.ServerSpec{
		Command: exe,
		Args:    []string{"--mcp-serve"},
		Env: map[string]string{
			"KITODOCK_OWNER_URL":   base,
			"KITODOCK_OWNER_TOKEN": token,
		},
	}
	home := userHome()
	var problems []string
	for _, client := range []mcpclient.Client{mcpclient.ClaudeCode, mcpclient.CodexCLI, mcpclient.Cursor} {
		if err := mcpclient.Register(client, home, spec); err != nil {
			problems = append(problems, string(client)+": "+err.Error())
		}
	}
	if len(problems) > 0 {
		// The endpoint must not outlive a half-done enable: stop it so the
		// status the UI shows ("off") matches reality.
		c.owner.Stop()
		return fmt.Errorf("registration incomplete (endpoint stopped): %s", strings.Join(problems, "; "))
	}
	if c.setEnabled != nil {
		_ = c.setEnabled(true)
	}
	return nil
}

// DisableMCP unregisters everywhere and stops the endpoint.
func (c *MCP) DisableMCP() error {
	home := userHome()
	var problems []string
	for _, client := range []mcpclient.Client{mcpclient.ClaudeCode, mcpclient.CodexCLI, mcpclient.Cursor} {
		if err := mcpclient.Unregister(client, home); err != nil {
			problems = append(problems, string(client)+": "+err.Error())
		}
	}
	if c.owner != nil {
		c.owner.Stop()
	}
	if c.setEnabled != nil {
		_ = c.setEnabled(false)
	}
	if len(problems) > 0 {
		return fmt.Errorf("unregistration incomplete: %s", strings.Join(problems, "; "))
	}
	return nil
}

// MCPAudit returns the argument-free audit trail, newest first.
func (c *MCP) MCPAudit(limit int) []ownerhttp.AuditEntry {
	entries, err := ownerhttp.ReadAudit(filepath.Join(paths.AgentsDir(), "mcp-audit.jsonl"), limit)
	if err != nil {
		return nil
	}
	return entries
}

// dispatch routes one owner method; names arrive kitodock_-stripped.
func (c *MCP) dispatch(method string, payload json.RawMessage) (any, error) {
	var args map[string]any
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &args)
	}
	switch method {
	case "project_list":
		return c.deps.Projects(), nil
	case "project_open":
		project, _ := args["project"].(string)
		if project == "" {
			return nil, fmt.Errorf("project is required")
		}
		c.deps.Navigate("project:" + project)
		return map[string]any{"opened": project}, nil
	case "service_list":
		return c.deps.Services(), nil
	case "service_start":
		name, _ := args["service"].(string)
		if name == "" {
			return nil, fmt.Errorf("service is required")
		}
		if err := c.deps.StartService(name); err != nil {
			return nil, err
		}
		return map[string]any{"started": name}, nil
	case "service_stop":
		name, _ := args["service"].(string)
		if name == "" {
			return nil, fmt.Errorf("service is required")
		}
		if err := c.deps.StopService(name); err != nil {
			return nil, err
		}
		return map[string]any{"stopped": name}, nil
	case "exec":
		project, _ := args["project"].(string)
		command, _ := args["command"].(string)
		if project == "" || command == "" {
			return nil, fmt.Errorf("project and command are required")
		}
		// The bridge enforces the confirm gate; the server refuses to run
		// without the marker so a hand-crafted request cannot skip it.
		if args["_confirmed"] != true {
			return nil, fmt.Errorf("exec is confirm-gated")
		}
		delete(args, "_confirmed")
		return c.deps.ExecProject(project, command)
	case "db_list":
		return c.deps.Databases()
	case "context":
		project, _ := args["project"].(string)
		if project == "" {
			return nil, fmt.Errorf("project is required")
		}
		if args["_confirmed"] != true {
			return nil, fmt.Errorf("context capture is confirm-gated")
		}
		delete(args, "_confirmed")
		return c.deps.CaptureContext(project)
	case "mail_list":
		return c.deps.Mailbox(25)
	case "term_poll":
		if c.term == nil {
			return nil, fmt.Errorf("terminal bridge unavailable")
		}
		ptyID, _ := args["ptyId"].(string)
		after, _ := args["since"].(float64)
		if ptyID == "" {
			return nil, fmt.Errorf("ptyId is required")
		}
		chunks, exited := c.term.TermPoll(ptyID, uint64(after))
		return map[string]any{"chunks": chunks, "exited": exited}, nil
	case "term_write":
		if c.term == nil {
			return nil, fmt.Errorf("terminal bridge unavailable")
		}
		ptyID, _ := args["ptyId"].(string)
		data, _ := args["data"].(string)
		if ptyID == "" {
			return nil, fmt.Errorf("ptyId is required")
		}
		return map[string]any{"ok": true}, c.term.TermWrite(ptyID, data)
	case "term_resize":
		if c.term == nil {
			return nil, fmt.Errorf("terminal bridge unavailable")
		}
		ptyID, _ := args["ptyId"].(string)
		cols, _ := args["cols"].(float64)
		rows, _ := args["rows"].(float64)
		if ptyID == "" {
			return nil, fmt.Errorf("ptyId is required")
		}
		return map[string]any{"ok": true}, c.term.TermResize(ptyID, uint16(cols), uint16(rows))
	}
	return nil, fmt.Errorf("unknown method %q", method)
}

// userHome resolves the home dir for client configs.
func userHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}

// execAllowlist guards the exec tool: project-scoped package managers and
// artisan only, first token exact.
var execAllowlist = map[string]bool{
	"php": true, "artisan": true, "composer": true,
	"npm": true, "npx": true, "pnpm": true, "yarn": true,
	"node": true, "git": true,
}

// RunProjectExec implements MCPDeps.ExecProject for the composition root:
// allowlisted binaries only, executed in the project folder with a
// timeout inherited from the caller's context.
func RunProjectExec(dir string, command string) (string, error) {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "", fmt.Errorf("empty command")
	}
	if !execAllowlist[strings.ToLower(fields[0])] {
		return "", fmt.Errorf("%q is not allowed through kitodock_exec (allowed: php, composer, npm, npx, pnpm, yarn, node, git)", fields[0])
	}
	cmd := exec.Command(fields[0], fields[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if len(out) > 64*1024 {
		out = out[:64*1024]
	}
	return string(out), err
}
