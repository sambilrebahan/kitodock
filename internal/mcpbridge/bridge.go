// Package mcpbridge speaks the Model Context Protocol on stdio so agent
// CLIs (Claude Code, Codex, Cursor) can drive Kitodock: tool calls arrive
// as JSON-RPC 2.0 lines, are forwarded over the loopback owner endpoint,
// and results return as MCP tool content. The bridge is intentionally
// thin — every capability check and audit trail lives behind the owner
// endpoint, and destructive tools require an explicit confirm argument.
package mcpbridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Protocol version we advertise (MCP 2024-11-05 shape).
const protocolVersion = "2024-11-05"

// Tool describes one exposed tool.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// confirmRequired is appended to confirm-gated tool schemas.
func confirmSchema(extra map[string]any) map[string]any {
	schema := map[string]any{"type": "object"}
	props := map[string]any{
		"confirm": map[string]any{
			"type":        "boolean",
			"description": "Set true to actually run; omitting it returns a dry explanation.",
		},
	}
	for k, v := range extra {
		props[k] = v
	}
	schema["properties"] = props
	schema["required"] = []string{}
	return schema
}

func plainSchema(extra map[string]any) map[string]any {
	schema := map[string]any{"type": "object", "properties": extra}
	return schema
}

// Tools is the catalog the bridge advertises. Names are stable API.
func Tools() []Tool {
	return []Tool{
		{
			Name:        "kitodock_project_list",
			Description: "List the projects Kitodock manages, with their local URLs.",
			InputSchema: plainSchema(nil),
		},
		{
			Name:        "kitodock_project_open",
			Description: "Bring one project to the foreground in the Kitodock window.",
			InputSchema: plainSchema(map[string]any{
				"project": map[string]any{"type": "string", "description": "Project name"},
			}),
		},
		{
			Name:        "kitodock_service_list",
			Description: "List local services (nginx, mysql, …) with running/installed state.",
			InputSchema: plainSchema(nil),
		},
		{
			Name:        "kitodock_service_start",
			Description: "Start a local service.",
			InputSchema: plainSchema(map[string]any{
				"service": map[string]any{"type": "string", "description": "Service name"},
			}),
		},
		{
			Name:        "kitodock_service_stop",
			Description: "Stop a local service.",
			InputSchema: plainSchema(map[string]any{
				"service": map[string]any{"type": "string", "description": "Service name"},
			}),
		},
		{
			Name:        "kitodock_exec",
			Description: "Run a project-scoped command (php artisan …, npm …) and return its output. Destructive: requires confirm=true.",
			InputSchema: confirmSchema(map[string]any{
				"project": map[string]any{"type": "string", "description": "Project name"},
				"command": map[string]any{"type": "string", "description": "Command line to run"},
			}),
		},
		{
			Name:        "kitodock_db_list",
			Description: "List the databases of the local MySQL/PostgreSQL service.",
			InputSchema: plainSchema(nil),
		},
		{
			Name:        "kitodock_context",
			Description: "Capture the application context of a Laravel project (routes, environment summary) as JSONL.",
			InputSchema: confirmSchema(map[string]any{
				"project": map[string]any{"type": "string", "description": "Project name"},
			}),
		},
		{
			Name:        "kitodock_mail_list",
			Description: "List recent mail caught by the local Mailpit inbox.",
			InputSchema: plainSchema(nil),
		},
	}
}

// OwnerClient calls the GUI's loopback endpoint.
type OwnerClient struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewOwnerClient builds a client with a short timeout.
func NewOwnerClient(baseURL, token string) *OwnerClient {
	return &OwnerClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

// Call invokes one owner method. payload may be nil.
func (c *OwnerClient) Call(method string, payload any) (json.RawMessage, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	body, err := json.Marshal(map[string]any{
		"method": method, "source": "mcp", "payload": payload,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/call", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("owner endpoint unreachable: %w", err)
	}
	defer resp.Body.Close()
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("owner response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("owner error: %s", envelope.Error)
	}
	return envelope.Result, nil
}

// --- JSON-RPC plumbing -------------------------------------------------------

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSON-RPC error codes used by MCP.
const (
	codeParse     = -32700
	codeMethod    = -32601
	codeInvalidPr = -32602
	codeInternal  = -32603
)

// Serve runs the stdio bridge until stdin closes. owner must be non-nil.
func Serve(stdin io.Reader, stdout io.Writer, owner *OwnerClient) error {
	writer := bufio.NewWriter(stdout)
	defer writer.Flush()

	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			writeResult(writer, nil, rpcError{codeParse, "unparseable request"})
			continue
		}
		result, rpcErr := dispatch(&req, owner)
		if len(req.ID) == 0 {
			// Notifications get no reply.
			continue
		}
		if rpcErr != nil {
			writeResult(writer, nil, *rpcErr)
		} else {
			writeResult(writer, result, rpcError{})
		}
	}
	return scanner.Err()
}

// dispatch answers one request.
func dispatch(req *rpcRequest, owner *OwnerClient) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "kitodock", "version": "0.6.0"},
		}, nil
	case "notifications/initialized", "initialized":
		return nil, nil
	case "tools/list":
		return map[string]any{"tools": Tools()}, nil
	case "tools/call":
		return callTool(req.Params, owner)
	case "ping":
		return map[string]any{}, nil
	}
	return nil, &rpcError{codeMethod, "method not found: " + req.Method}
}

// callTool executes one tool invocation through the owner endpoint. The
// confirm gate is enforced here, before anything reaches the GUI.
func callTool(params json.RawMessage, owner *OwnerClient) (any, *rpcError) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return nil, &rpcError{codeInvalidPr, "invalid tools/call params"}
	}

	var args map[string]any
	if len(call.Arguments) > 0 {
		_ = json.Unmarshal(call.Arguments, &args)
	}
	// Strict allowlist: only advertised tool names pass. Names are matched
	// whole — "exec" or "kitodock_exec-x" never reach the owner endpoint.
	known := false
	for _, tool := range Tools() {
		if tool.Name == call.Name {
			known = true
			break
		}
	}
	if !known {
		return nil, &rpcError{codeInvalidPr, fmt.Sprintf("unknown tool %q", call.Name)}
	}

	confirm, _ := args["confirm"].(bool)
	switch call.Name {
	case "kitodock_exec", "kitodock_context":
		if !confirm {
			return toolText(fmt.Sprintf(
				"%s is confirm-gated: repeat the call with \"confirm\": true to run it.",
				call.Name)), nil
		}
		// Server-side gate marker: the endpoint re-checks it, so skipping
		// this bridge cannot skip the confirmation.
		args["_confirmed"] = true
	}
	// The confirm flag is transport detail, not payload.
	delete(args, "confirm")

	raw, err := owner.Call(strings.TrimPrefix(call.Name, "kitodock_"), args)
	if err != nil {
		return nil, &rpcError{codeInternal, err.Error()}
	}
	return toolText(string(raw)), nil
}

// toolText wraps a string as MCP tool content.
func toolText(text string) any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	}
}

// writeResult emits one JSON-RPC reply line. A zero rpcError with a nil
// result means "empty success".
func writeResult(w *bufio.Writer, result any, rpcErr rpcError) {
	reply := map[string]any{"jsonrpc": "2.0", "id": nil}
	if rpcErr.Code != 0 || rpcErr.Message != "" {
		reply["error"] = rpcErr
	} else {
		reply["result"] = result
	}
	data, err := json.Marshal(reply)
	if err != nil {
		return
	}
	w.Write(data)
	w.WriteByte('\n')
	_ = w.Flush()
}
