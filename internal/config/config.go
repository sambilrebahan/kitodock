// Package config persists Kitodock's preferences to config.json under the
// environment root.
//
// Phase 0 owns the pieces the shell needs: the chosen language, the
// dashboard service visibility and the projects directory. The schema grows
// phase by phase; what is already fixed here are the rules every future
// field inherits:
//
//   - saves are atomic and keep the file's existing access rules
//     (WriteKeepingPermissions), so a rewrite can never widen who may read it;
//   - SetXxx setters revert their in-memory change when the save fails, so
//     the process state and the disk never drift apart;
//   - Get hands back a value copy, including cloned maps, because callers
//     across goroutines must not iterate something a setter can mutate;
//   - the install root is reconciled on load: an environment carried to a
//     new folder adopts the new root and rebases the paths that lived under
//     the old one.
package config

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"kitodock/internal/atomicfile"
	"kitodock/internal/paths"
)

// Stack records which services the dashboard shows. Keys are service names;
// true means visible. Roles with several candidates (nginx/Apache,
// MySQL/Postgres) coexist — visibility is the user's call.
type Stack struct {
	Enabled map[string]bool `json:"enabled"`
}

// Projects is the saved per-project and workspace preferences block.
type Projects struct {
	// TLD is the suffix project hostnames get ("shop." + TLD).
	TLD string `json:"tld"`
	// Root is the folder projects are listed from. It defaults to the
	// environment's own www/ but may point anywhere the user keeps code.
	Root string `json:"root"`
	// SSL records which projects serve HTTPS.
	SSL map[string]bool `json:"ssl,omitempty"`
	// PhpVersions records per-project PHP series overrides ("8.4").
	PhpVersions map[string]string `json:"phpVersions,omitempty"`
	// NodeVersions records per-project Node.js overrides.
	NodeVersions map[string]string `json:"nodeVersions,omitempty"`
}

// Config is the whole document. Fields with omitempty are "unset means
// default on next read", which is what lets new fields ship without
// migrations.
type Config struct {
	InstallRoot string   `json:"installRoot,omitempty"`
	Language    string   `json:"language,omitempty"`
	Stack       Stack    `json:"stack"`
	Projects    Projects `json:"projects"`
	// Crons is the persisted cron job list, stored as raw JSON to avoid
	// importing internal/cron from config (import cycle).
	Crons []map[string]any `json:"crons,omitempty"`
	// TerminalWorkspace is the persisted terminal layout (tabs, split trees,
	// spawn descriptors). Raw JSON owned by the frontend; the backend
	// round-trips it.
	TerminalWorkspace []byte `json:"terminalWorkspace,omitempty"`
	// MCPEnabled turns on the loopback MCP endpoint and the client
	// registrations. Off by default: it is an experimental surface.
	MCPEnabled bool `json:"mcpEnabled,omitempty"`
}

// defaultsFor is the value a fresh environment starts from. Returned through
// a function so the maps are cloned per store — a shared package-level map
// would let one store's setters leak into every other store's baseline.
func defaultsFor() Config {
	return Config{
		Stack: Stack{Enabled: map[string]bool{
			"nginx":   true,
			"mysql":   true,
			"mailpit": true,
		}},
		Projects: Projects{TLD: "test"},
	}
}

// Store owns config.json for this process.
type Store struct {
	mu   sync.Mutex
	path string
	data Config

	// movedFrom remembers the previous install root when a load detected the
	// environment was relocated; empty otherwise. The caller repairs the
	// state files this package does not own.
	movedFrom string
}

// Open loads (or creates) the configuration at path.
func Open(path string) (*Store, error) {
	store := &Store{path: path, data: defaultsFor()}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		var loaded Config
		if err := json.Unmarshal(raw, &loaded); err != nil {
			return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
		}
		store.backfill(&loaded)
		store.data = loaded
	case os.IsNotExist(err):
		// fresh environment; defaults already in place
	default:
		return nil, err
	}
	if store.data.Projects.Root == "" {
		store.data.Projects.Root = paths.ProjectsDir()
	}
	dirty := store.reconcileRoot()
	if dirty {
		if err := store.write(); err != nil {
			return nil, fmt.Errorf("save relocated configuration (original kept): %w", err)
		}
	}
	return store, nil
}

// backfill fills fields older documents predate. An explicitly empty
// Stack.Enabled map is respected as "user hid everything"; only a missing
// map (nil) is upgraded with the defaults.
func (s *Store) backfill(c *Config) {
	if c.Projects.TLD == "" {
		c.Projects.TLD = defaultsFor().Projects.TLD
	}
	if c.Stack.Enabled == nil {
		c.Stack.Enabled = map[string]bool{}
		maps.Copy(c.Stack.Enabled, defaultsFor().Stack.Enabled)
	}
}

// reconcileRoot notices that the environment folder moved and follows it:
// recorded paths under the old root are rebased onto the new one, and the
// move is remembered for the caller. Paths kept outside the old root are the
// user's own and stay untouched. Writing the new root back is not optional —
// without it every start would rediscover the same move forever.
func (s *Store) reconcileRoot() bool {
	current := paths.Root()
	previous := s.data.InstallRoot
	if paths.IsSameRoot(previous, current) {
		return false
	}
	if previous == "" {
		s.data.InstallRoot = current
		return true
	}
	if moved, ok := paths.MovePath(previous, current, s.data.Projects.Root); ok {
		s.data.Projects.Root = moved
	}
	s.data.InstallRoot = current
	s.movedFrom = previous
	return true
}

// MovedFrom names the environment's previous root when a move was detected
// at load, else "".
func (s *Store) MovedFrom() string { return s.movedFrom }

// Get returns a snapshot. Everything a setter can mutate is cloned.
func (s *Store) Get() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.data
	out.Stack.Enabled = maps.Clone(out.Stack.Enabled)
	return out
}

// write persists the current data. Callers hold the mutex.
func (s *Store) write() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteKeepingPermissions(s.path, append(raw, '\n'), 0o600)
}

// commit applies change under the mutex and reverts it if the save fails.
// The "before" snapshot clones the maps too: changes may edit them in place,
// and a shallow struct copy would hand the revert the very map it must
// restore.
func (s *Store) commit(change func(*Config)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.data
	before.Stack.Enabled = maps.Clone(before.Stack.Enabled)
	change(&s.data)
	if err := s.write(); err != nil {
		s.data = before
		return err
	}
	return nil
}

// SetStack replaces the whole dashboard-visibility map.
func (s *Store) SetStack(stack Stack) error {
	return s.commit(func(c *Config) { c.Stack = stack })
}

// SetServiceEnabled toggles one service's dashboard visibility.
func (s *Store) SetServiceEnabled(name string, visible bool) error {
	return s.commit(func(c *Config) {
		if c.Stack.Enabled == nil {
			c.Stack.Enabled = map[string]bool{}
		}
		if visible {
			c.Stack.Enabled[name] = true
		} else {
			delete(c.Stack.Enabled, name)
		}
	})
}

// SetProjects replaces the projects preferences.
func (s *Store) SetProjects(projects Projects) error {
	return s.commit(func(c *Config) { c.Projects = projects })
}

// Language is the chosen UI language code, "" while never chosen.
func (s *Store) Language() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Language
}

// SetLanguage records the chosen UI language. Other Kitodock processes learn
// about it through Watch — see there for why the file, not an event, is the
// agreement channel.
func (s *Store) SetLanguage(code string) error {
	return s.commit(func(c *Config) { c.Language = code })
}

// SetMCPEnabled toggles the experimental MCP endpoint preference.
func (s *Store) SetMCPEnabled(enabled bool) error {
	return s.commit(func(c *Config) { c.MCPEnabled = enabled })
}

// Watch reports the parsed configuration whenever the file changes, until
// ctx is done. It watches the containing directory on purpose: saves replace
// the file atomically, and a watch pinned to the old file's handle would go
// quiet after the first one. Events are debounced so a save storm collapses
// into one callback.
func (s *Store) Watch(ctx context.Context, onChange func(Config)) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return
	}
	defer watcher.Close()
	if err := watcher.Add(filepath.Dir(s.path)); err != nil {
		return
	}
	var settle <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-settle:
			settle = nil
			raw, err := os.ReadFile(s.path)
			if err != nil {
				continue // mid-rename gap; the next event re-reads
			}
			var fresh Config
			if err := json.Unmarshal(raw, &fresh); err != nil {
				continue // torn external write; keep the last good state
			}
			s.mu.Lock()
			s.backfill(&fresh)
			s.data = fresh
			s.mu.Unlock()
			onChange(fresh)
		case event := <-watcher.Events:
			if event.Name != s.path {
				continue
			}
			settle = time.After(150 * time.Millisecond)
		case <-watcher.Errors:
			// transient; the next event resumes
		}
	}
}
