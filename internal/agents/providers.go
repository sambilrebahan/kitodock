// Package agents manages local coding-agent CLIs (Claude Code, Codex,
// Gemini CLI, Copilot CLI): probing what is installed, launching them in
// project folders, discovering their past sessions for resume, installing
// the lifecycle hooks that report activity back to Kitodock, and keeping
// an activity feed of what every agent is doing.
package agents

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ProviderKind identifies one supported agent CLI.
type ProviderKind string

const (
	Claude  ProviderKind = "claude"
	Codex   ProviderKind = "codex"
	Gemini  ProviderKind = "gemini"
	Copilot ProviderKind = "copilot"
)

// Provider describes one agent CLI integration.
type Provider struct {
	Kind  ProviderKind `json:"kind"`
	Label string       `json:"label"`
	// Exe is the command name looked up on PATH.
	Exe string `json:"exe"`
	// ProbeArgs ask the CLI for its version.
	ProbeArgs []string `json:"-"`
	// ResumeArgs precede a session id when resuming.
	ResumeArgs []string `json:"-"`
	// HookStyle names the config format hooks are written to; "" means the
	// provider has no hookable lifecycle today.
	HookStyle string `json:"hookStyle"`
}

// Hook styles supported by the hooks editor.
const (
	HookStyleSettingsJSON = "settings-json" // ~/.<dir>/settings.json (Claude, Gemini)
	HookStyleConfigTOML   = "config-toml"   // ~/.<dir>/config.toml (Codex notify)
	HookStyleNone         = ""              // launch-only provider
)

// catalog is the provider registry; order drives the launcher menu.
var catalog = []Provider{
	{
		Kind: Claude, Label: "Claude Code", Exe: "claude",
		ProbeArgs:  []string{"--version"},
		ResumeArgs: []string{"--resume"},
		HookStyle:  HookStyleSettingsJSON,
	},
	{
		Kind: Codex, Label: "Codex CLI", Exe: "codex",
		ProbeArgs:  []string{"--version"},
		ResumeArgs: []string{"resume"},
		HookStyle:  HookStyleConfigTOML,
	},
	{
		Kind: Gemini, Label: "Gemini CLI", Exe: "gemini",
		ProbeArgs:  []string{"--version"},
		ResumeArgs: []string{"--resume"},
		HookStyle:  HookStyleSettingsJSON,
	},
	{
		Kind: Copilot, Label: "Copilot CLI", Exe: "copilot",
		ProbeArgs: []string{"--version"},
		// Copilot CLI has no session resume yet.
		HookStyle: HookStyleNone,
	},
}

// Catalog returns the known providers.
func Catalog() []Provider {
	out := make([]Provider, len(catalog))
	copy(out, catalog)
	return out
}

// ProviderByKind looks one provider up.
func ProviderByKind(kind ProviderKind) (Provider, bool) {
	for _, p := range catalog {
		if p.Kind == kind {
			return p, true
		}
	}
	return Provider{}, false
}

// Installed describes the probe result for one provider.
type Installed struct {
	Provider Provider `json:"provider"`
	// Path is the resolved executable ("" when absent).
	Path string `json:"path"`
	// Version is the first line of `exe --version` ("" on failure).
	Version string `json:"version"`
}

// ProbeVersion runs the provider's version command with a short timeout.
// An absent executable yields ("", nil) — not installed is normal state.
func ProbeVersion(ctx context.Context, p Provider) (string, error) {
	path, err := exec.LookPath(p.Exe)
	if err != nil {
		return "", nil
	}
	if len(p.ProbeArgs) == 0 {
		return "", nil
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, path, p.ProbeArgs...)
	// npm-style CLIs are .cmd shims whose node child survives a context
	// kill and holds stdout open; WaitDelay makes Output return anyway.
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	if err != nil {
		// Present but unhealthy still counts as installed, version unknown.
		return "", nil
	}
	first := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if len(first) > 120 {
		first = first[:120]
	}
	return first, nil
}

// ProbeAll probes every provider quickly.
func ProbeAll(ctx context.Context) []Installed {
	out := make([]Installed, 0, len(catalog))
	for _, p := range catalog {
		path, _ := exec.LookPath(p.Exe)
		version, _ := ProbeVersion(ctx, p)
		out = append(out, Installed{Provider: p, Path: path, Version: version})
	}
	return out
}

// sessionsRoot returns the directory a provider stores session transcripts
// under, given the user's home directory.
func sessionsRoot(p ProviderKind, home string) string {
	switch p {
	case Claude:
		return filepath.Join(home, ".claude", "projects")
	case Codex:
		return filepath.Join(home, ".codex", "sessions")
	case Gemini:
		return filepath.Join(home, ".gemini", "tmp")
	case Copilot:
		return filepath.Join(home, ".copilot", "sessions")
	}
	return ""
}

// configPath returns the provider's config file hooks are written to.
func configPath(p ProviderKind, home string) string {
	switch p {
	case Claude:
		return filepath.Join(home, ".claude", "settings.json")
	case Codex:
		return filepath.Join(home, ".codex", "config.toml")
	case Gemini:
		return filepath.Join(home, ".gemini", "settings.json")
	case Copilot:
		return ""
	}
	return ""
}

// homeDir resolves the current home directory (seam for tests).
var homeDir = os.UserHomeDir

// mustHome returns home or "." when unavailable.
func mustHome() string {
	home, err := homeDir()
	if err != nil {
		return "."
	}
	return home
}
