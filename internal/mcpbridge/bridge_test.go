package mcpbridge

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"kitodock/internal/ownerhttp"
)

// TestServeToolsListAndCall drives the stdio bridge against a real
// loopback owner server: initialize → tools/list → confirm gate → call.
func TestServeToolsListAndCall(t *testing.T) {
	srv := ownerhttp.NewServer(func(method string, payload json.RawMessage) (any, error) {
		switch method {
		case "project_list":
			return []map[string]any{{"name": "shop", "url": "https://shop.test"}}, nil
		case "service_start":
			return map[string]any{"started": true}, nil
		}
		return nil, errUnknownMethod
	}, filepath.Join(t.TempDir(), "audit.jsonl"))
	base, token, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	var in bytes.Buffer
	var out bytes.Buffer
	write := func(v any) {
		data, _ := json.Marshal(v)
		in.Write(data)
		in.WriteByte('\n')
	}
	write(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	write(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	// Confirm-gated tool without confirm: dry reply, no owner call.
	write(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": "kitodock_exec", "arguments": map[string]any{"project": "shop", "command": "php artisan migrate"}}})
	write(map[string]any{"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": map[string]any{"name": "kitodock_service_start", "arguments": map[string]any{"service": "nginx"}}})
	// Unknown method → JSON-RPC error.
	write(map[string]any{"jsonrpc": "2.0", "id": 5, "method": "no/such"})

	if err := Serve(&in, &out, NewOwnerClient(base, token)); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("replies = %d:\n%s", len(lines), out.String())
	}

	var init struct {
		Result struct {
			ProtocolVersion string                `json:"protocolVersion"`
			ServerInfo      struct{ Name string } `json:"serverInfo"`
		} `json:"result"`
	}
	decode(t, lines[0], &init)
	if init.Result.ProtocolVersion != protocolVersion || init.Result.ServerInfo.Name != "kitodock" {
		t.Fatalf("initialize = %+v", init.Result)
	}

	var list struct {
		Result struct {
			Tools []Tool `json:"tools"`
		} `json:"result"`
	}
	decode(t, lines[1], &list)
	if len(list.Result.Tools) != 9 {
		t.Fatalf("tools = %d", len(list.Result.Tools))
	}
	names := map[string]bool{}
	for _, tool := range list.Result.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{
		"kitodock_project_list", "kitodock_project_open",
		"kitodock_service_list", "kitodock_service_start", "kitodock_service_stop",
		"kitodock_exec", "kitodock_db_list", "kitodock_context", "kitodock_mail_list",
	} {
		if !names[want] {
			t.Errorf("missing tool %s", want)
		}
	}

	var gated struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
		} `json:"result"`
	}
	decode(t, lines[2], &gated)
	if !strings.Contains(gated.Result.Content[0].Text, "confirm") {
		t.Fatalf("gated reply = %+v", gated.Result)
	}

	var started struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
		} `json:"result"`
	}
	decode(t, lines[3], &started)
	if !strings.Contains(started.Result.Content[0].Text, `"started":true`) {
		t.Fatalf("service_start reply = %+v", started.Result)
	}

	var errReply struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	decode(t, lines[4], &errReply)
	if errReply.Error.Code != codeMethod {
		t.Fatalf("unknown method reply = %+v", errReply.Error)
	}
}

func TestConfirmGatedToolsCarrySchema(t *testing.T) {
	for _, name := range []string{"kitodock_exec", "kitodock_context"} {
		var found *Tool
		for i, tool := range Tools() {
			if tool.Name == name {
				found = &Tools()[i]
			}
		}
		if found == nil {
			t.Fatalf("%s missing", name)
		}
		props := found.InputSchema["properties"].(map[string]any)
		if _, ok := props["confirm"]; !ok {
			t.Errorf("%s schema lacks confirm", name)
		}
	}
}

func TestIsNotActuallyUsedButKeepsImports(t *testing.T) {
	// OwnerClient error path against a dead port.
	client := NewOwnerClient("http://127.0.0.1:1", "tok")
	if _, err := client.Call("x", nil); err == nil {
		t.Fatal("dead owner accepted a call")
	}
}

var errUnknownMethod = &methodError{"unknown method"}

type methodError struct{ msg string }

func (e *methodError) Error() string { return e.msg }

func decode(t *testing.T, line string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(line), v); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
}
