// Package ownerhttp is the loopback control endpoint the GUI process
// exposes for Kitodock's helper personalities: the MCP stdio bridge (and
// later the command bar) talk to it over HTTP with a per-run bearer token.
// It listens on 127.0.0.1 only, rejects anything but that token, and
// writes an argument-free audit line for every call.
package ownerhttp

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	ErrorClass string `json:"errorClass,omitempty"`
	// Source tags the caller personality.
	Source string `json:"source"`
}

// Server is the loopback endpoint.
// Source allowlists: which methods each personality may call. The token
// a helper holds determines its source; the source determines the method
// set, so an MCP client can never reach terminal methods and a detached
// window can never reach exec.
var sourceAllowlist = map[string][]string{
	"mcp": {
		"project_list", "project_open", "service_list", "service_start",
		"service_stop", "exec", "db_list", "context", "mail_list",
	},
	"terminal-window": {"term_poll", "term_write", "term_resize"},
}

func allowed(source, method string) bool {
	for _, m := range sourceAllowlist[source] {
		if m == method {
			return true
		}
	}
	return false
}

type Server struct {
	mu        sync.Mutex
	handler   Handler
	tokens    map[string]string
	ln        net.Listener
	srv       *http.Server
	auditMu   sync.Mutex
	auditPath string
	running   bool
}

// NewServer builds a server around a method dispatcher. auditPath may be
// empty (auditing disabled).
func NewServer(handler Handler, auditPath string) *Server {
	return &Server{handler: handler, auditPath: auditPath, tokens: map[string]string{}}
}

// Start listens on an OS-chosen loopback port and returns the base URL.
func (s *Server) Start() (baseURL, token string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return s.urlLocked(), s.tokens["mcp"], nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", fmt.Errorf("listen loopback: %w", err)
	}
	s.ln = ln
	s.tokens["mcp"] = newToken()
	s.tokens["terminal-window"] = newToken()

	mux := http.NewServeMux()
	mux.HandleFunc("/call", s.handleCall)
	mux.HandleFunc("/health", s.handleHealth)
	// Bounded timeouts so a stuck helper cannot pin sockets open.
	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() { _ = s.srv.Serve(ln) }()
	s.running = true
	return s.urlLocked(), s.tokens["mcp"], nil
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

// Token returns the bearer token for one source ("" when stopped).
func (s *Server) Token(source string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens[source]
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
	source, ok := s.authorize(r)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		Method  string          `json:"method"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Method == "" || !allowed(source, req.Method) {
		s.audit(req.Method, source, errDenied)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	result, err := s.handler(req.Method, req.Payload)
	s.audit(req.Method, source, err)

	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"result": result})
}

// errDenied marks allowlist rejections in the audit trail.
var errDenied = fmt.Errorf("method not allowed for this source")

// authorize maps the bearer token back to its source; constant-time per
// candidate so tokens cannot be timing-probed.
func (s *Server) authorize(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if len(auth) <= len(prefix) || auth[:len(prefix)] != prefix {
		return "", false
	}
	presented := auth[len(prefix):]
	s.mu.Lock()
	defer s.mu.Unlock()
	for source, token := range s.tokens {
		if subtle.ConstantTimeCompare([]byte(presented), []byte(token)) == 1 {
			return source, true
		}
	}
	return "", false
}

// audit appends an argument-free record of the call.
func (s *Server) audit(method, source string, err error) {
	if s.auditPath == "" {
		return
	}
	entry := AuditEntry{Time: time.Now(), Method: method, Source: source, OK: err == nil}
	if err != nil {
		entry.ErrorClass = classify(err)
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

// classify reduces an error to a coarse class; raw messages can quote
// paths and secrets and never enter the audit trail.
func classify(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not allowed"):
		return "denied"
	case strings.Contains(msg, "not found"), strings.Contains(msg, "no host"), strings.Contains(msg, "no project"):
		return "not_found"
	case strings.Contains(msg, "unreachable"), strings.Contains(msg, "not running"), strings.Contains(msg, "unavailable"):
		return "unreachable"
	case strings.Contains(msg, "busy"), strings.Contains(msg, "already in progress"):
		return "busy"
	default:
		return "internal"
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
