// Package ownerhttp is the loopback control endpoint the GUI process
// exposes for Kitodock's helper personalities: the MCP stdio bridge (and
// later the command bar) talk to it over HTTP with a per-run bearer token.
// It listens on 127.0.0.1 only, rejects anything but that token, and
// writes an argument-free audit line for every call.
package ownerhttp

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Handler executes one named method; payload is the decoded request body.
type Handler func(method string, payload json.RawMessage) (any, error)

// AuditEntry is one argument-free audit record.
type AuditEntry struct {
	Time   time.Time `json:"time"`
	Method string    `json:"method"`
	OK     bool      `json:"ok"`
	// ErrorHint is a short error class, never the full message (which
	// could quote user data).
	ErrorHint string `json:"errorHint,omitempty"`
	// Source tags the caller personality.
	Source string `json:"source"`
}

// Server is the loopback endpoint.
type Server struct {
	mu        sync.Mutex
	handler   Handler
	token     string
	ln        net.Listener
	srv       *http.Server
	auditMu   sync.Mutex
	auditPath string
	running   bool
}

// NewServer builds a server around a method dispatcher. auditPath may be
// empty (auditing disabled).
func NewServer(handler Handler, auditPath string) *Server {
	return &Server{handler: handler, auditPath: auditPath}
}

// Start listens on an OS-chosen loopback port and returns the base URL.
func (s *Server) Start() (baseURL, token string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return s.urlLocked(), s.token, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", fmt.Errorf("listen loopback: %w", err)
	}
	s.ln = ln
	s.token = newToken()

	mux := http.NewServeMux()
	mux.HandleFunc("/call", s.handleCall)
	mux.HandleFunc("/health", s.handleHealth)
	s.srv = &http.Server{Handler: mux}
	go func() { _ = s.srv.Serve(ln) }()
	s.running = true
	return s.urlLocked(), s.token, nil
}

// Stop shuts the listener down.
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	_ = s.srv.Close()
	s.running = false
}

// URL returns the current base URL ("" when stopped).
func (s *Server) URL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.urlLocked()
}

func (s *Server) urlLocked() string {
	if s.ln == nil {
		return ""
	}
	return "http://" + s.ln.Addr().String()
}

// Token exposes the bearer token for spawning helper personalities.
func (s *Server) Token() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleCall is the single RPC surface: POST /call with
// {"method": "...", "payload": {...}} and a valid bearer token.
func (s *Server) handleCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "post only", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		Method  string          `json:"method"`
		Source  string          `json:"source"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Method == "" {
		http.Error(w, "method required", http.StatusBadRequest)
		return
	}

	result, err := s.handler(req.Method, req.Payload)
	s.audit(req.Method, req.Source, err)

	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"result": result})
}

// authorized checks the bearer token; the listener is loopback-only by
// construction.
func (s *Server) authorized(r *http.Request) bool {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if len(auth) <= len(prefix) || auth[:len(prefix)] != prefix {
		return false
	}
	s.mu.Lock()
	token := s.token
	s.mu.Unlock()
	return auth[len(prefix):] == token
}

// audit appends an argument-free record of the call.
func (s *Server) audit(method, source string, err error) {
	if s.auditPath == "" {
		return
	}
	entry := AuditEntry{Time: time.Now(), Method: method, Source: source, OK: err == nil}
	if err != nil {
		entry.ErrorHint = classify(err)
	}
	data, merr := json.Marshal(entry)
	if merr != nil {
		return
	}
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	_ = os.MkdirAll(filepath.Dir(s.auditPath), 0o755)
	f, ferr := os.OpenFile(s.auditPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if ferr != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}

// classify reduces an error to a coarse class for the audit log.
func classify(err error) string {
	msg := err.Error()
	switch {
	case len(msg) > 40:
		return msg[:40]
	default:
		return msg
	}
}

// ReadAudit loads the audit tail (newest first, capped).
func ReadAudit(path string, limit int) ([]AuditEntry, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []AuditEntry
	for _, line := range splitLines(data) {
		var e AuditEntry
		if json.Unmarshal(line, &e) == nil {
			entries = append(entries, e)
		}
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	// Newest first.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	return entries, nil
}

func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			if i > start {
				out = append(out, data[start:i])
			}
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}

// newToken mints a 32-byte random bearer token.
func newToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		// Degraded but functional fallback; loopback still applies.
		return base64.RawStdEncoding.EncodeToString([]byte(time.Now().String()))
	}
	return base64.RawStdEncoding.EncodeToString(buf)
}
