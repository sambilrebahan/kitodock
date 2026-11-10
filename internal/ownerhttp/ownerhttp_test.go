package ownerhttp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// TestServerAuthAllowlistAndAudit covers the hardened surface: per-source
// tokens, method allowlists, cross-source denial, class-only audit.
func TestServerAuthAllowlistAndAudit(t *testing.T) {
	audit := filepath.Join(t.TempDir(), "audit.jsonl")
	srv := NewServer(func(method string, payload json.RawMessage) (any, error) {
		switch method {
		case "project_list":
			return []map[string]any{{"name": "shop"}}, nil
		case "term_poll":
			return map[string]any{"chunks": nil, "exited": false}, nil
		}
		return nil, fmt.Errorf("unknown method")
	}, audit)

	base, mcpToken, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	termToken := srv.Token("terminal-window")
	if mcpToken == "" || termToken == "" || mcpToken == termToken {
		t.Fatalf("tokens malformed: %q vs %q", mcpToken, termToken)
	}

	call := func(token, method string) (*http.Response, map[string]any) {
		body, _ := json.Marshal(map[string]any{"method": method, "payload": map[string]any{}})
		req, _ := http.NewRequest(http.MethodPost, base+"/call", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var envelope map[string]any
		json.NewDecoder(resp.Body).Decode(&envelope)
		return resp, envelope
	}

	if resp, err := http.Get(base + "/health"); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("health = %v err=%v", resp, err)
	}
	if resp, err := http.Post(base+"/call", "application/json", bytes.NewReader([]byte(`{"method":"project_list"}`))); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unauthenticated = %v err=%v", resp, err)
	}

	resp, envelope := call(mcpToken, "project_list")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mcp project_list = %d %v", resp.StatusCode, envelope)
	}
	if resp, _ := call(mcpToken, "term_write"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("mcp term_write = %d, want 403", resp.StatusCode)
	}
	if resp, envelope := call(termToken, "term_poll"); resp.StatusCode != http.StatusOK || envelope["result"] == nil {
		t.Fatalf("term poll = %d %v", resp.StatusCode, envelope)
	}
	if resp, _ := call(termToken, "exec"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("term exec = %d, want 403", resp.StatusCode)
	}

	raw, err := os.ReadFile(audit)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := ReadAudit(audit, 0)
	if err != nil {
		t.Fatal(err)
	}
	sawDenied := false
	for _, e := range entries {
		if e.Source == "mcp" && e.Method == "term_write" && !e.OK && e.ErrorClass == "denied" {
			sawDenied = true
		}
	}
	if !sawDenied {
		t.Fatalf("denied call not audited: %s / %+v", raw, entries)
	}
}

// TestClassOnlyAuditOnHandlerError: allowed-but-failing methods log a
// class, never the raw message (which can quote paths and tokens).
func TestClassOnlyAuditOnHandlerError(t *testing.T) {
	audit := filepath.Join(t.TempDir(), "audit.jsonl")
	srv := NewServer(func(method string, _ json.RawMessage) (any, error) {
		if method == "db_list" {
			return nil, fmt.Errorf("could not reach mysql at C:/secret path ghp_xyz")
		}
		return nil, nil
	}, audit)
	base, token, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	body, _ := json.Marshal(map[string]any{"method": "db_list", "payload": map[string]any{}})
	req, _ := http.NewRequest(http.MethodPost, base+"/call", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("db_list = %v err=%v", resp, err)
	}

	raw, err := os.ReadFile(audit)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("ghp_xyz")) || bytes.Contains(raw, []byte("secret path")) {
		t.Fatalf("audit leaked message: %s", raw)
	}
	entries, _ := ReadAudit(audit, 0)
	if len(entries) != 1 || entries[0].ErrorClass == "" || entries[0].OK {
		t.Fatalf("audit entry = %+v", entries)
	}
}
