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

// TestServerAuthAndRoundTrip covers the whole surface: bearer enforcement,
// method dispatch, audit without arguments.
func TestServerAuthAndRoundTrip(t *testing.T) {
	audit := filepath.Join(t.TempDir(), "audit.jsonl")
	calls := map[string]int{}
	srv := NewServer(func(method string, payload json.RawMessage) (any, error) {
		calls[method]++
		switch method {
		case "project_list":
			return []map[string]any{{"name": "shop"}}, nil
		case "boom":
			return nil, fmt.Errorf("sensitive detail")
		}
		return nil, fmt.Errorf("unknown method")
	}, audit)

	base, token, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	// Health is open; calls need the token.
	if resp, err := http.Get(base + "/health"); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("health = %v err=%v", resp, err)
	}
	if resp, err := http.Post(base+"/call", "application/json", bytes.NewReader([]byte(`{"method":"project_list"}`))); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unauthenticated call = %v err=%v", resp, err)
	}

	// Authenticated round-trip decodes the result envelope.
	body, _ := json.Marshal(map[string]any{"method": "project_list", "source": "mcp", "payload": map[string]any{}})
	req, _ := http.NewRequest(http.MethodPost, base+"/call", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Result []map[string]any `json:"result"`
		Error  string           `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || len(envelope.Result) != 1 || envelope.Result[0]["name"] != "shop" {
		t.Fatalf("call = %d %+v err=%q", resp.StatusCode, envelope.Result, envelope.Error)
	}

	// Errors map to 500 with a message, and the audit stays argument-free.
	body, _ = json.Marshal(map[string]any{"method": "boom", "payload": map[string]any{"secret": "x"}})
	req, _ = http.NewRequest(http.MethodPost, base+"/call", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("boom = %v err=%v", resp, err)
	}

	entries, err := ReadAudit(audit, 0)
	if err != nil || len(entries) != 2 {
		t.Fatalf("audit = %d err=%v", len(entries), err)
	}
	if entries[0].Method != "boom" || entries[0].OK || entries[0].ErrorHint != "sensitive detail" {
		t.Fatalf("audit[0] = %+v", entries[0])
	}
	if entries[1].Method != "project_list" || !entries[1].OK {
		t.Fatalf("audit[1] = %+v", entries[1])
	}
	raw := readFile(t, audit)
	if bytes.Contains(raw, []byte("secret")) || bytes.Contains(raw, []byte("shop")) {
		t.Fatalf("audit leaked payload data: %s", raw)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
