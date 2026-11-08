//go:build !windows

// Package pty: non-Windows stub. Kitodock's terminal workspace is
// Windows-first; other platforms get honest errors.
package pty

import (
	"errors"
	"io"
	"sync"
)

type Session interface {
	io.ReadWriteCloser
	Resize(cols, rows uint16) error
}

type Waiter interface {
	Wait() error
}

const (
	EnvTerminalID = "KITODOCK_TERMINAL_ID"
	EnvAgentOwner = "KITODOCK_AGENT_OWNER"
)

var ErrWindowsOnly = errors.New("PTY sessions are Windows-only in this phase")

type Spawn struct {
	Exe      string
	Args     []string
	Dir      string
	PathDirs []string
	Env      []string
	Cols     uint16
	Rows     uint16
}

type Registry struct {
	mu       sync.Mutex
	sessions map[string]stubSession
	nextID   int
}

type stubSession struct{ id string }

func (s stubSession) Read([]byte) (int, error)    { return 0, io.EOF }
func (s stubSession) Write([]byte) (int, error)   { return 0, ErrWindowsOnly }
func (s stubSession) Close() error                { return nil }
func (s stubSession) Resize(uint16, uint16) error { return ErrWindowsOnly }

func NewRegistry() *Registry {
	return &Registry{sessions: map[string]stubSession{}}
}

func (r *Registry) NotifyExit(func(string)) {}

func (r *Registry) StartCommand(Spawn) (string, error) {
	return "", ErrWindowsOnly
}

func (r *Registry) Get(string) (Session, bool) { return nil, false }

func (r *Registry) Close(string) {}

func (r *Registry) CloseAll() {}

func LivePath(extraDirs []string) string { return "" }
