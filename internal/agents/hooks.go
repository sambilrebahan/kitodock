package agents

// Hook installation: write (and cleanly remove) lifecycle hooks into each
// provider's config so agent activity flows back to Kitodock as
// `kitodock --agent-event` invocations. Every edit is idempotent and
// preceded by a one-time backup; removal restores the pre-hook state.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// hookMarker tags config objects Kitodock owns.
const hookMarker = "kitodock-managed"

// HookEvent is one lifecycle event a hook reports.
type HookEvent string

const (
	EventSessionStart HookEvent = "session-start"
	EventTurnStop     HookEvent = "turn-stop"
)

// HookSpec describes the command hooks run.
type HookSpec struct {
	// Command is the full command line the provider executes. It receives
	// the provider's event payload on stdin or as $1 depending on style.
	Command string
	Events  []HookEvent
}

// InstallHooks writes Kitodock hooks into the provider's config under
// home, keeping a backup of the untouched original. Providers without a
// hookable config return a clear error.
func InstallHooks(p ProviderKind, home string, spec HookSpec) error {
	provider, ok := ProviderByKind(p)
	if !ok {
		return fmt.Errorf("unknown provider %q", p)
	}
	path := configPath(p, home)
	if path == "" || provider.HookStyle == HookStyleNone {
		return fmt.Errorf("%s does not support lifecycle hooks", provider.Label)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	switch provider.HookStyle {
	case HookStyleSettingsJSON:
		return installJSONHooks(path, spec)
	case HookStyleConfigTOML:
		return installTOMLNotify(path, spec)
	}
	return fmt.Errorf("unsupported hook style %q", provider.HookStyle)
}

// HooksInstalled reports whether Kitodock-managed hooks are present in a
// provider's config. Providers without hook support report false.
func HooksInstalled(p ProviderKind, home string) bool {
	provider, ok := ProviderByKind(p)
	if !ok || provider.HookStyle == HookStyleNone {
		return false
	}
	path := configPath(p, home)
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	switch provider.HookStyle {
	case HookStyleSettingsJSON:
		return strings.Contains(string(data), hookMarker)
	case HookStyleConfigTOML:
		return strings.Contains(string(data), tomlMarker)
	}
	return false
}

// RemoveHooks strips Kitodock-managed entries, restoring the backup when
// one exists.
func RemoveHooks(p ProviderKind, home string) error {
	provider, ok := ProviderByKind(p)
	if !ok {
		return fmt.Errorf("unknown provider %q", p)
	}
	path := configPath(p, home)
	if path == "" || provider.HookStyle == HookStyleNone {
		return fmt.Errorf("%s does not support lifecycle hooks", provider.Label)
	}
	if backup, err := os.ReadFile(path + ".kitodock-bak"); err == nil {
		if len(strings.TrimSpace(string(backup))) == 0 {
			return os.Remove(path)
		}
		return os.WriteFile(path, backup, 0o644)
	}
	switch provider.HookStyle {
	case HookStyleSettingsJSON:
		return removeJSONHooks(path)
	case HookStyleConfigTOML:
		return removeTOMLNotify(path)
	}
	return nil
}

// backupOnce preserves the original config the first time we touch it.
func backupOnce(path string) {
	bak := path + ".kitodock-bak"
	if _, err := os.Stat(bak); err == nil {
		return
	}
	if data, err := os.ReadFile(path); err == nil {
		_ = os.WriteFile(bak, data, 0o600)
	}
}

// --- settings.json style (Claude Code, Gemini CLI) -------------------------
//
// The managed block lives under hooks.<event>, matching each CLI's own
// hook schema:
//
//	"hooks": {
//	  "Stop": [ { "kitodock-managed": true,
//	              "hooks": [ { "type": "command", "command": "…" } ] } ]
//	}

func installJSONHooks(path string, spec HookSpec) error {
	backupOnce(path)
	doc := readJSONMap(path)

	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		doc["hooks"] = hooks
	}
	for _, event := range spec.Events {
		key := jsonEventKey(event)
		entries, _ := hooks[key].([]any)
		managed := map[string]any{
			hookMarker: true,
			"hooks": []any{map[string]any{
				"type": "command", "command": spec.Command,
			}},
		}
		kept := make([]any, 0, len(entries)+1)
		for _, e := range entries {
			if m, ok := e.(map[string]any); ok && m[hookMarker] == true {
				continue // replace any previous managed entry
			}
			kept = append(kept, e)
		}
		hooks[key] = append(kept, managed)
	}
	return writeJSONMap(path, doc)
}

func removeJSONHooks(path string) error {
	doc := readJSONMap(path)
	hooks, _ := doc["hooks"].(map[string]any)
	changed := false
	for key, entries := range hooks {
		list, ok := entries.([]any)
		if !ok {
			continue
		}
		kept := make([]any, 0, len(list))
		for _, e := range list {
			if m, ok := e.(map[string]any); ok && m[hookMarker] == true {
				changed = true
				continue
			}
			kept = append(kept, e)
		}
		if len(kept) == 0 {
			delete(hooks, key)
		} else {
			hooks[key] = kept
		}
	}
	if changed {
		return writeJSONMap(path, doc)
	}
	return nil
}

// jsonEventKey maps our event names to each CLI's hook key.
func jsonEventKey(e HookEvent) string {
	switch e {
	case EventSessionStart:
		return "SessionStart"
	case EventTurnStop:
		return "Stop"
	}
	return string(e)
}

// --- config.toml style (Codex CLI notify) -----------------------------------
//
// Codex reports lifecycle through one notify command:
//
//	notify = ["kitodock.exe", "--agent-event", "codex"]
//
// The line is edited textually (TOML round-trips lose comments), with the
// marker comment above it for clean removal.

const tomlMarker = "# kitodock-notify"

func installTOMLNotify(path string, spec HookSpec) error {
	backupOnce(path)
	fields := strings.Fields(spec.Command)
	line := "notify = [" + quoteAll(fields) + "] " + tomlMarker
	content := ""
	if data, err := os.ReadFile(path); err == nil {
		content = string(data)
	}
	lines := strings.Split(content, "\n")
	kept := make([]string, 0, len(lines)+1)
	inserted := false
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "notify") || strings.Contains(trimmed, tomlMarker) {
			if !inserted {
				kept = append(kept, line)
				inserted = true
			}
			continue // drop old notify/marker lines
		}
		kept = append(kept, l)
	}
	if !inserted {
		kept = append(kept, line)
	}
	return os.WriteFile(path, []byte(strings.TrimRight(strings.Join(kept, "\n"), "\n")+"\n"), 0o644)
}

func removeTOMLNotify(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	lines := strings.Split(string(data), "\n")
	kept := make([]string, 0, len(lines))
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "notify") || strings.Contains(trimmed, tomlMarker) {
			continue
		}
		kept = append(kept, l)
	}
	return os.WriteFile(path, []byte(strings.TrimRight(strings.Join(kept, "\n"), "\n")+"\n"), 0o644)
}

func quoteAll(fields []string) string {
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = "\"" + f + "\""
	}
	return strings.Join(parts, ", ")
}

// --- shared JSON helpers -----------------------------------------------------

func readJSONMap(path string) map[string]any {
	doc := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &doc)
	}
	return doc
}

func writeJSONMap(path string, doc map[string]any) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
