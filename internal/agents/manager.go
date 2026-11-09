package agents

// Session manager: launches agent CLIs in project folders (through an
// injected PTY spawner), remembers launches so they can be resumed after a
// Kitodock restart, and tracks the provider-side session id once it is
// discovered from transcripts.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Session is one remembered agent launch.
type Session struct {
	ID string `json:"id"`
	// Provider is the CLI used.
	Provider ProviderKind `json:"provider"`
	// Project names the Kitodock project (may be "" for ad-hoc folders).
	Project string `json:"project,omitempty"`
	// Dir is the working directory the agent runs in.
	Dir string `json:"dir"`
	// PtyID is the live PTY handle ("" once the agent exited).
	PtyID string `json:"ptyId,omitempty"`
	// ProviderSession is the id found in the provider's transcript, used
	// for `--resume` after a restart.
	ProviderSession string    `json:"providerSession,omitempty"`
	Title           string    `json:"title,omitempty"`
	StartedAt       time.Time `json:"startedAt"`
	LastSeen        time.Time `json:"lastSeen"`
	// Archived hides the session from the default list.
	Archived bool `json:"archived,omitempty"`
}

// Spawner starts a pseudo-terminal; production uses the ConPTY registry.
type Spawner interface {
	Start(provider ProviderKind, exe string, args []string, dir string) (ptyID string, err error)
}

// Manager owns the session list and their PTYs.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	order    []string
	spawner  Spawner
	// stopper closes a PTY by id; set via SetStopper alongside the spawner.
	stopper func(ptyID string)
	path    string
	nextID  int
	// OnExit is invoked (from any goroutine) when a PTY reports exit; the
	// controller wires it to the event bus.
	OnExit func(sessionID string)
}

// NewManager loads (or creates) the session list at path.
func NewManager(spawner Spawner, path string) (*Manager, error) {
	m := &Manager{
		sessions: map[string]*Session{},
		spawner:  spawner,
		path:     path,
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read agent sessions: %w", err)
	}
	var list []Session
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parse agent sessions: %w", err)
	}
	for i := range list {
		s := list[i]
		// A remembered PTY id never survives a restart.
		s.PtyID = ""
		m.sessions[s.ID] = &s
		m.order = append(m.order, s.ID)
	}
	return m, nil
}

// Launch spawns a provider in a directory. resumeSession optionally names
// the provider-side session to continue.
func (m *Manager) Launch(provider ProviderKind, project, dir, resumeSession string) (*Session, error) {
	p, ok := ProviderByKind(provider)
	if !ok {
		return nil, fmt.Errorf("unknown agent provider %q", provider)
	}
	if dir == "" {
		return nil, fmt.Errorf("launch directory is required")
	}
	if resumeSession != "" && len(p.ResumeArgs) == 0 {
		return nil, fmt.Errorf("%s does not support session resume", p.Label)
	}

	args := []string{}
	if resumeSession != "" {
		args = append(append(args, p.ResumeArgs...), resumeSession)
	}
	ptyID, err := m.spawner.Start(provider, p.Exe, args, dir)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	id := fmt.Sprintf("ag%d-%d", time.Now().Unix(), m.nextID)
	s := &Session{
		ID: id, Provider: provider, Project: project, Dir: dir,
		PtyID: ptyID, ProviderSession: resumeSession,
		StartedAt: time.Now(), LastSeen: time.Now(),
		Title: defaultTitle(project, dir),
	}
	m.sessions[id] = s
	m.order = append(m.order, id)
	if err := m.persistLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// Attach re-associates a live PTY id with a session (used when the UI
// adopts a pane or after resume).
func (m *Manager) Attach(sessionID, ptyID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		return fmt.Errorf("agent session %s not found", sessionID)
	}
	s.PtyID = ptyID
	s.LastSeen = time.Now()
	return m.persistLocked()
}

// MarkProviderSession records the transcript-discovered session id.
func (m *Manager) MarkProviderSession(sessionID, providerSession string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		return fmt.Errorf("agent session %s not found", sessionID)
	}
	if s.ProviderSession == providerSession {
		return nil
	}
	s.ProviderSession = providerSession
	s.LastSeen = time.Now()
	return m.persistLocked()
}

// Stop ends a running agent.
func (m *Manager) Stop(sessionID string) error {
	m.mu.Lock()
	s, ok := m.sessions[sessionID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("agent session %s not found", sessionID)
	}
	ptyID := s.PtyID
	s.PtyID = ""
	m.mu.Unlock()
	if ptyID != "" && m.stopper != nil {
		m.stopper(ptyID)
	}
	return nil
}

// Rename sets a display title.
func (m *Manager) Rename(sessionID, title string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		return fmt.Errorf("agent session %s not found", sessionID)
	}
	s.Title = title
	return m.persistLocked()
}

// Archive hides (or restores) a session without deleting history.
func (m *Manager) Archive(sessionID string, archived bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		return fmt.Errorf("agent session %s not found", sessionID)
	}
	s.Archived = archived
	return m.persistLocked()
}

// Delete forgets a session entirely (a live PTY is stopped first).
func (m *Manager) Delete(sessionID string) error {
	_ = m.Stop(sessionID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[sessionID]; !ok {
		return fmt.Errorf("agent session %s not found", sessionID)
	}
	delete(m.sessions, sessionID)
	for i, id := range m.order {
		if id == sessionID {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	return m.persistLocked()
}

// List returns sessions newest-first; includeArchived controls filtering.
func (m *Manager) List(includeArchived bool) []Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Session, 0, len(m.sessions))
	for _, id := range m.order {
		s := m.sessions[id]
		if s.Archived && !includeArchived {
			continue
		}
		out = append(out, *s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// Get returns a copy of one session.
func (m *Manager) Get(sessionID string) (Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		return Session{}, false
	}
	return *s, true
}

// SetStopper installs the PTY closer; the spawner package usually pairs
// Start with a Close function the caller supplies here.
func (m *Manager) SetStopper(close func(ptyID string)) { m.stopper = close }

// SetSpawner replaces the launcher seam (the controller injects a
// project-aware PTY adapter right before each launch).
func (m *Manager) SetSpawner(s Spawner) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spawner = s
}

// persistLocked writes the session list; the caller holds the mutex, so
// the snapshot is taken directly rather than through List.
func (m *Manager) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	list := make([]Session, 0, len(m.sessions))
	for _, id := range m.order {
		if s, ok := m.sessions[id]; ok {
			list = append(list, *s)
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].StartedAt.After(list[j].StartedAt) })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

// defaultTitle builds a display title from project or folder name.
func defaultTitle(project, dir string) string {
	if project != "" {
		return project
	}
	base := filepath.Base(dir)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "ad-hoc"
	}
	return base
}
