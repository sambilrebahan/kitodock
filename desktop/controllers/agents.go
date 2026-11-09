package controllers

// The agents controller: provider probing, launching agent CLIs in
// project folders through the shared PTY registry, resume after restart,
// lifecycle hooks, and the activity feed drained from the CLI spool.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"kitodock/internal/agents"
	"kitodock/internal/paths"
	"kitodock/internal/pty"
)

// Agents owns the agent manager and activity feed.
type Agents struct {
	bus  *EventBus
	mgr  *agents.Manager
	feed *agents.ActivityLog

	pty *pty.Registry
	// serving resolves project paths and runtime PATH entries.
	serving AgentServing

	mu         sync.Mutex
	ptyToAgent map[string]string // live PTY id → agent session id
}

// AgentServing is what the agents controller needs from the app.
type AgentServing interface {
	ProjectPath(name string) string
	PathDirsFor(name string) []string
}

// ptyAgentSpawner adapts the PTY registry to the agents.Spawner seam.
type ptyAgentSpawner struct {
	registry   *pty.Registry
	controller *Agents
	provider   agents.ProviderKind
	project    string
}

// Start launches one agent CLI; the adapter remembers which agent session
// the PTY belongs to so exits can be mapped back.
func (s *ptyAgentSpawner) Start(provider agents.ProviderKind, exe string, args []string, dir string) (string, error) {
	pathDirs := []string{}
	if s.project != "" && s.controller.serving != nil {
		pathDirs = s.controller.serving.PathDirsFor(s.project)
	}
	ptyID, err := s.registry.StartCommand(pty.Spawn{
		Exe: exe, Args: args, Dir: dir, PathDirs: pathDirs,
		Cols: 110, Rows: 32,
	})
	if err != nil {
		return "", err
	}
	return ptyID, nil
}

// NewAgents builds the controller; the PTY registry is shared with the
// terminal workspace so agent panes stream through the same term:data
// events.
func NewAgents(bus *EventBus, registry *pty.Registry, serving AgentServing) (*Agents, error) {
	dir := paths.AgentsDir()
	mgr, err := agents.NewManager(nil, filepath.Join(dir, "sessions.json"))
	if err != nil {
		return nil, err
	}
	feed := agents.NewActivityLog(filepath.Join(dir, "activity.jsonl"))
	a := &Agents{
		bus: bus, mgr: mgr, feed: feed,
		pty: registry, serving: serving,
		ptyToAgent: map[string]string{},
	}
	mgr.SetStopper(func(ptyID string) {
		registry.Close(ptyID)
		a.mu.Lock()
		delete(a.ptyToAgent, ptyID)
		a.mu.Unlock()
	})
	// Provider sessions link into the activity feed.
	feed.SetLink(func(providerSession string) string {
		for _, s := range mgr.List(true) {
			if s.ProviderSession == providerSession {
				return s.ID
			}
		}
		return ""
	})
	return a, nil
}

// StartDrain pumps the hook spool into the feed until stop; new entries
// are announced as agents:activity events.
func (c *Agents) StartDrain() (stop func()) {
	return c.feed.StartDrainLoop(
		filepath.Join(paths.AgentsDir(), "spool"), drainInterval,
		func(events []agents.Activity) {
			c.bus.Emit(EvAgentsActivity, map[string]any{"events": events})
		})
}

// drainInterval is the spool pump cadence; short enough that hook events
// feel live, long enough to batch bursts.
const drainInterval = 250 * time.Millisecond

// --- Providers & hooks -------------------------------------------------------

// AgentProviders returns the catalog with live install probes.
func (c *Agents) AgentProviders() []agents.Installed {
	return agents.ProbeAll(context.Background())
}

// HookStatus reports, per provider, whether Kitodock hooks are installed.
func (c *Agents) HookStatus() []AgentHookStatus {
	out := []AgentHookStatus{}
	for _, p := range agents.Catalog() {
		status := AgentHookStatus{Provider: string(p.Kind), Label: p.Label, Supported: p.HookStyle != agents.HookStyleNone}
		if status.Supported {
			status.Installed = agents.HooksInstalled(p.Kind, agentHome())
		}
		out = append(out, status)
	}
	return out
}

// AgentHookStatus is one provider's hook state for the UI.
type AgentHookStatus struct {
	Provider  string `json:"provider"`
	Label     string `json:"label"`
	Supported bool   `json:"supported"`
	Installed bool   `json:"installed"`
}

// InstallAgentHooks writes lifecycle hooks for one provider.
func (c *Agents) InstallAgentHooks(provider string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	kind := agents.ProviderKind(provider)
	if _, ok := agents.ProviderByKind(kind); !ok {
		return fmt.Errorf("unknown provider %q", provider)
	}
	spec := agents.HookSpec{
		Command: quoteCommand(exe, "--agent-event", string(kind)),
		Events:  []agents.HookEvent{agents.EventSessionStart, agents.EventTurnStop},
	}
	return agents.InstallHooks(kind, agentHome(), spec)
}

// RemoveAgentHooks strips one provider's Kitodock hooks.
func (c *Agents) RemoveAgentHooks(provider string) error {
	return agents.RemoveHooks(agents.ProviderKind(provider), agentHome())
}

// --- Sessions ------------------------------------------------------------------

// AgentSessions lists remembered sessions.
func (c *Agents) AgentSessions(includeArchived bool) []agents.Session {
	return c.mgr.List(includeArchived)
}

// LaunchAgent starts a provider inside a project.
func (c *Agents) LaunchAgent(provider, project, resumeSession string) (agents.Session, error) {
	dir := ""
	if c.serving != nil {
		dir = c.serving.ProjectPath(project)
	}
	if dir == "" {
		return agents.Session{}, fmt.Errorf("project %q has no folder", project)
	}
	spawner := &ptyAgentSpawner{registry: c.pty, controller: c, project: project}
	c.mgr.SetSpawner(spawner)
	s, err := c.mgr.Launch(agents.ProviderKind(provider), project, dir, resumeSession)
	if err != nil {
		return agents.Session{}, err
	}
	c.mu.Lock()
	c.ptyToAgent[s.PtyID] = s.ID
	c.mu.Unlock()
	c.bus.Emit(EvAgentsSession, map[string]any{"event": "launched", "session": s})
	return *s, nil
}

// ResumeAgent relaunch a remembered session after a restart.
func (c *Agents) ResumeAgent(sessionID string) (agents.Session, error) {
	s, ok := c.mgr.Get(sessionID)
	if !ok {
		return agents.Session{}, fmt.Errorf("agent session %s not found", sessionID)
	}
	if s.ProviderSession == "" {
		return agents.Session{}, fmt.Errorf("session %s has no provider session id to resume yet", sessionID)
	}
	return c.LaunchAgent(string(s.Provider), s.Project, s.ProviderSession)
}

// StopAgent ends a running agent.
func (c *Agents) StopAgent(sessionID string) error { return c.mgr.Stop(sessionID) }

// RenameAgent sets a title.
func (c *Agents) RenameAgent(sessionID, title string) error { return c.mgr.Rename(sessionID, title) }

// ArchiveAgent hides or restores a session.
func (c *Agents) ArchiveAgent(sessionID string, archived bool) error {
	return c.mgr.Archive(sessionID, archived)
}

// DeleteAgent forgets a session.
func (c *Agents) DeleteAgent(sessionID string) error { return c.mgr.Delete(sessionID) }

// DiscoverAgentSessions lists a provider's resumable past sessions.
func (c *Agents) DiscoverAgentSessions(provider string) ([]agents.DiscoveredSession, error) {
	return agents.DiscoverSessions(agents.ProviderKind(provider), agentHome(), 50)
}

// AdoptProviderSession links a discovered provider session into a
// remembered Kitodock session (used when the user adopts a running agent).
func (c *Agents) AdoptProviderSession(sessionID, providerSession string) error {
	return c.mgr.MarkProviderSession(sessionID, providerSession)
}

// AgentActivity returns the recent activity feed.
func (c *Agents) AgentActivity(limit int) []agents.Activity {
	return c.feed.Feed(limit)
}

// HandlePtyExit maps a PTY exit back to its agent session (wired into the
// shared registry's exit notification by the composition root).
func (c *Agents) HandlePtyExit(ptyID string) {
	c.mu.Lock()
	agentID := c.ptyToAgent[ptyID]
	delete(c.ptyToAgent, ptyID)
	c.mu.Unlock()
	if agentID == "" {
		return
	}
	if s, ok := c.mgr.Get(agentID); ok && s.PtyID == ptyID {
		_ = c.mgr.Stop(agentID)
	}
	c.bus.Emit(EvAgentsSession, map[string]any{"event": "exit", "sessionId": agentID})
}

// WritePtyAgent forwards keystrokes to a running agent's PTY.
func (c *Agents) WriteAgent(sessionID, data string) error {
	s, ok := c.mgr.Get(sessionID)
	if !ok || s.PtyID == "" {
		return fmt.Errorf("agent session %s is not running", sessionID)
	}
	session, live := c.pty.Get(s.PtyID)
	if !live {
		return fmt.Errorf("agent session %s is not running", sessionID)
	}
	_, err := session.Write([]byte(data))
	return err
}

// agentHome resolves the home directory hooks and discovery read from.
func agentHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}

// quoteCommand renders a hook command line, quoting the executable path
// when it contains spaces.
func quoteCommand(exe string, args ...string) string {
	if strings.ContainsAny(exe, " \t") && !strings.HasPrefix(exe, `"`) {
		exe = `"` + exe + `"`
	}
	return strings.Join(append([]string{exe}, args...), " ")
}
