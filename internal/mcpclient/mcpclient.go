// Package mcpclient registers Kitodock's MCP server into agent clients
// (Claude Code, Codex CLI, Cursor). Every edit keeps a one-time backup and
// is idempotent; removal restores the pre-registration state.
package mcpclient

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ServerSpec describes how to launch the Kitodock MCP server.
type ServerSpec struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// Client identifies one agent client config format.
type Client string

const (
	ClaudeCode Client = "claude-code" // ~/.claude.json
	CodexCLI   Client = "codex"       // ~/.codex/config.toml
	Cursor     Client = "cursor"      // ~/.cursor/mcp.json
)

// ServerName is the key the server registers under.
const ServerName = "kitodock"

// configPath resolves a client's config file under home.
func configPath(c Client, home string) string {
	switch c {
	case ClaudeCode:
		return filepath.Join(home, ".claude.json")
	case CodexCLI:
		return filepath.Join(home, ".codex", "config.toml")
	case Cursor:
		return filepath.Join(home, ".cursor", "mcp.json")
	}
	return ""
}

// Registered reports whether the Kitodock server is present in a config.
func Registered(c Client, home string) bool {
	path := configPath(c, home)
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	switch c {
	case CodexCLI:
		return strings.Contains(string(data), "[mcp_servers."+ServerName+"]")
	default:
		var doc map[string]any
		if json.Unmarshal(data, &doc) != nil {
			return false
		}
		servers, _ := doc["mcpServers"].(map[string]any)
		_, ok := servers[ServerName]
		return ok
	}
}

// Register adds the server to one client's config, creating the file when
// absent and keeping a backup of the first pre-existing version.
func Register(c Client, home string, spec ServerSpec) error {
	path := configPath(c, home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	backupOnce(path)

	switch c {
	case ClaudeCode, Cursor:
		doc, err := readJSONDoc(path)
		if err != nil {
			return fmt.Errorf("refusing to touch %s: %w", filepath.Base(path), err)
		}
		servers, _ := doc["mcpServers"].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
			doc["mcpServers"] = servers
		}
		servers[ServerName] = map[string]any{
			"command": spec.Command, "args": spec.Args, "env": spec.Env,
		}
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(path, append(data, '\n'), 0o644)

	case CodexCLI:
		return appendTOMLSection(path, spec)
	}
	return fmt.Errorf("unknown client %q", c)
}

// Unregister removes the server, restoring the backup when one exists.
func Unregister(c Client, home string) error {
	path := configPath(c, home)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	if backup, err := os.ReadFile(path + ".kitodock-bak"); err == nil {
		if len(strings.TrimSpace(string(backup))) == 0 {
			return os.Remove(path)
		}
		return os.WriteFile(path, backup, 0o644)
	}
	switch c {
	case ClaudeCode, Cursor:
		doc, err := readJSONDoc(path)
		if err != nil {
			return fmt.Errorf("refusing to touch %s: %w", filepath.Base(path), err)
		}
		if servers, ok := doc["mcpServers"].(map[string]any); ok {
			delete(servers, ServerName)
			if len(servers) == 0 {
				delete(doc, "mcpServers")
			}
		}
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(path, append(data, '\n'), 0o644)
	case CodexCLI:
		return removeTOMLSection(path)
	}
	return nil
}

// appendTOMLSection writes [mcp_servers.kitodock] with command/args/env.
func appendTOMLSection(path string, spec ServerSpec) error {
	header := "[mcp_servers." + ServerName + "]"
	section := header + "\ncommand = \"" + spec.Command + "\"\n"
	if len(spec.Args) > 0 {
		quoted := make([]string, len(spec.Args))
		for i, a := range spec.Args {
			quoted[i] = "\"" + a + "\""
		}
		section += "args = [" + strings.Join(quoted, ", ") + "]\n"
	}
	// Env is flattened as env.KEY = "value" entries.
	for k, v := range spec.Env {
		section += "env." + k + " = \"" + v + "\"\n"
	}

	content := ""
	if data, err := os.ReadFile(path); err == nil {
		content = string(data)
	}
	lines := strings.Split(content, "\n")
	kept := make([]string, 0, len(lines)+8)
	skipping := false
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "[mcp_servers."+ServerName+"]") {
			skipping = true
			continue
		}
		if skipping && strings.HasPrefix(trimmed, "[") {
			skipping = false // next section begins
		} else if skipping {
			continue
		}
		kept = append(kept, l)
	}
	if len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) != "" {
		kept = append(kept, "")
	}
	kept = append(kept, strings.Split(strings.TrimRight(section, "\n"), "\n")...)
	return os.WriteFile(path, []byte(strings.Join(kept, "\n")+"\n"), 0o644)
}

// removeTOMLSection deletes our section block.
func removeTOMLSection(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	kept := make([]string, 0, len(lines))
	skipping := false
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "[mcp_servers."+ServerName+"]") {
			skipping = true
			continue
		}
		if skipping && strings.HasPrefix(trimmed, "[") {
			skipping = false
		} else if skipping {
			continue
		}
		kept = append(kept, l)
	}
	return os.WriteFile(path, []byte(strings.TrimRight(strings.Join(kept, "\n"), "\n")+"\n"), 0o644)
}

// backupOnce preserves the pre-registration config the first time we
// touch it. A file that already carries our registration is never backed
// up: restoring it would re-register instead of cleaning up.
func backupOnce(path string) {
	bak := path + ".kitodock-bak"
	if _, err := os.Stat(bak); err == nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if strings.Contains(string(data), ServerName) {
		return
	}
	_ = os.WriteFile(bak, data, 0o600)
}

// tomlString renders a TOML basic string with real escaping — Windows
// paths otherwise produce invalid TOML.
func tomlString(v string) string {
	return "\"" + strings.NewReplacer(
		"\\", "\\\\",
		"\"", "\\\"",
		"\n", "\\n",
	).Replace(v) + "\""
}

// writeFileAtomic lands content via temp+rename so a crash mid-write
// cannot leave a truncated config behind.
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".kitodock-tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readJSONDoc loads a JSON config or errors — an unparseable file is
// never silently replaced.
func readJSONDoc(path string) (map[string]any, error) {
	doc := map[string]any{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return doc, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return doc, nil
}
