//go:build windows

// Package pty manages pseudo-terminal sessions on Windows through ConPTY
// (the Windows pseudoconsole API). Each session wraps one hidden conpty +
// one child process; the registry owns IDs (term-1, term-2, …) that the
// frontend uses to address panes.
//
// The environment every child sees is carefully constructed: the live PATH
// is re-read from the Windows registry (machine + user) so terminals see
// tools installed after the app launched, and Kitodock's own runtime
// directories are prepended for the session's project.
package pty

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/UserExistsError/conpty"
	"golang.org/x/sys/windows/registry"
)

// Session is the interface every PTY backend satisfies (SSH sessions will
// use the same shape in Phase 5).
type Session interface {
	io.ReadWriteCloser
	Resize(cols, rows uint16) error
}

// Waiter reports when the child has fully exited (on Windows, conhost
// keeps the output pipe open after the shell exits, so Read alone is not
// enough to detect exit).
type Waiter interface {
	Wait() error
}

// Env vars every Kitodock terminal carries — hooks and the agents
// subsystem read these to recognize our terminals.
const (
	EnvTerminalID = "KITODOCK_TERMINAL_ID"
	EnvAgentOwner = "KITODOCK_AGENT_OWNER"
)

// conptySession couples the conpty handle with session metadata.
type conptySession struct {
	id     string
	handle *conpty.ConPty
	// closeOnce prevents double-Close from racing the exit handler.
	closeOnce sync.Once
}

func (s *conptySession) Read(p []byte) (int, error)  { return s.handle.Read(p) }
func (s *conptySession) Write(p []byte) (int, error) { return s.handle.Write(p) }
func (s *conptySession) Close() error {
	s.closeOnce.Do(func() { s.handle.Close() })
	return nil
}
func (s *conptySession) Resize(cols, rows uint16) error {
	// conpty.Resize takes (cols, rows).
	return s.handle.Resize(int(cols), int(rows))
}

// Registry owns all live PTY sessions.
type Registry struct {
	mu       sync.Mutex
	sessions map[string]*conptySession
	next     atomic.Int64
	// onExit notifies when a terminal dies (agents subsystem listens).
	onExit func(id string)
}

func NewRegistry() *Registry {
	return &Registry{sessions: map[string]*conptySession{}}
}

// NotifyExit installs the callback fired when a session is closed.
func (r *Registry) NotifyExit(onExit func(id string)) { r.onExit = onExit }

// Spawn describes one new terminal session.
type Spawn struct {
	// Exe is the binary to run ("powershell", "cmd", "bash").
	Exe string
	// Args are extra arguments.
	Args []string
	// Dir is the working directory ("" = inherit).
	Dir string
	// PathDirs are prepended to PATH (php, node, composer dirs).
	PathDirs []string
	// Env adds extra variables after the base set.
	Env []string
	// Cols/Rows are the initial pseudoconsole size.
	Cols, Rows uint16
}

// StartCommand spawns a new PTY session and returns its ID.
func (r *Registry) StartCommand(spawn Spawn) (string, error) {
	id := fmt.Sprintf("term-%d", r.next.Add(1))

	// Build the command line (conpty.Start takes a single string).
	parts := []string{spawn.Exe}
	parts = append(parts, spawn.Args...)
	commandLine := strings.Join(parts, " ")

	// Build the child environment.
	env := buildEnv(id, spawn.PathDirs, spawn.Env)

	opts := []conpty.ConPtyOption{
		conpty.ConPtyEnv(env),
		conpty.ConPtyDimensions(int(spawn.Cols), int(spawn.Rows)),
	}
	if spawn.Dir != "" {
		opts = append(opts, conpty.ConPtyWorkDir(spawn.Dir))
	}

	handle, err := conpty.Start(commandLine, opts...)
	if err != nil {
		return "", fmt.Errorf("spawn %s: %w", spawn.Exe, err)
	}

	session := &conptySession{id: id, handle: handle}
	r.mu.Lock()
	r.sessions[id] = session
	r.mu.Unlock()
	return id, nil
}

// Get returns a session's ReadWriteCloser for streaming.
func (r *Registry) Get(id string) (Session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok {
		return nil, false
	}
	return s, true
}

// Close ends one session and fires the exit callback.
func (r *Registry) Close(id string) {
	r.mu.Lock()
	s, ok := r.sessions[id]
	if ok {
		delete(r.sessions, id)
	}
	r.mu.Unlock()
	if !ok {
		return
	}
	s.Close()
	if r.onExit != nil {
		go r.onExit(id)
	}
}

// CloseAll ends every session (app shutdown).
func (r *Registry) CloseAll() {
	r.mu.Lock()
	ids := make([]string, 0, len(r.sessions))
	for id := range r.sessions {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for _, id := range ids {
		r.Close(id)
	}
}

// buildEnv constructs the child process environment: the live Windows PATH
// (re-read from the registry so tools installed after app launch are
// visible), with extraDirs prepended, plus Kitodock's identification vars
// and any spawn-specific extras.
func buildEnv(id string, extraPathDirs, extraEnv []string) []string {
	env := os.Environ()

	// Replace PATH with the live version.
	path := LivePath(extraPathDirs)
	env = replaceOrAppend(env, "PATH", path)

	// Kitodock identification.
	env = replaceOrAppend(env, EnvTerminalID, id)

	// Extra vars from the spawn descriptor (project runtime, TLS env, …).
	for _, pair := range extraEnv {
		if key, value, ok := strings.Cut(pair, "="); ok {
			env = replaceOrAppend(env, key, value)
		}
	}
	return env
}

// replaceOrAppend sets key=value in env, replacing an existing entry.
func replaceOrAppend(env []string, key, value string) []string {
	pair := key + "=" + value
	for i, existing := range env {
		if strings.HasPrefix(strings.ToLower(existing), strings.ToLower(key)+"=") {
			env[i] = pair
			return env
		}
	}
	return append(env, pair)
}

// LivePath returns the current PATH as the Windows registry sees it —
// machine + user — merged with the process's own PATH, deduplicated,
// with extraDirs prepended.
func LivePath(extraDirs []string) string {
	var parts []string
	parts = append(parts, extraDirs...)
	parts = append(parts, registryPath(true)...)  // HKLM
	parts = append(parts, registryPath(false)...) // HKCU
	parts = append(parts, filepath.SplitList(os.Getenv("PATH"))...)
	return strings.Join(dedup(parts), string(os.PathListSeparator))
}

// registryPath reads PATH from one registry hive.
func registryPath(machine bool) []string {
	root := registry.CURRENT_USER
	keyPath := `Environment`
	if machine {
		root = registry.LOCAL_MACHINE
		keyPath = `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`
	}
	key, err := registry.OpenKey(root, keyPath, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer key.Close()
	value, _, err := key.GetStringValue("PATH")
	if err != nil {
		return nil
	}
	return filepath.SplitList(value)
}

// dedup keeps the first occurrence of each path (case-insensitive).
func dedup(parts []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, part := range parts {
		if part == "" {
			continue
		}
		key := strings.ToLower(part)
		if !seen[key] {
			seen[key] = true
			out = append(out, part)
		}
	}
	return out
}
