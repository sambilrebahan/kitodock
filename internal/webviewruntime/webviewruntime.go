// Package webviewruntime inspects the WebView2 runtime the desktop shell
// depends on, so support flows can tell "runtime missing/corrupt" from
// other startup failures and point at the right recovery step.
package webviewruntime

import (
	"golang.org/x/sys/windows/registry"
)

// WebView2Key is the per-machine runtime registration.
const WebView2Key = `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`

// State describes the runtime's health.
type State struct {
	// Installed reports whether a runtime is registered.
	Installed bool `json:"installed"`
	// Version is the registered runtime version ("" when absent).
	Version string `json:"version"`
	// PerUser reports a per-user (non-system) install.
	PerUser bool `json:"perUser"`
	// Recoverable hints the standard repair path.
	Recoverable bool   `json:"recoverable"`
	Hint        string `json:"hint,omitempty"`
}

// Inspect reads the runtime registration.
func Inspect() State {
	if key, err := registry.OpenKey(registry.LOCAL_MACHINE, WebView2Key, registry.QUERY_VALUE); err == nil {
		defer key.Close()
		version, _, _ := key.GetStringValue("pv")
		return State{
			Installed: true, Version: version,
			Recoverable: true,
			Hint:        "runtime registered per machine",
		}
	}
	if key, err := registry.OpenKey(registry.CURRENT_USER, WebView2Key, registry.QUERY_VALUE); err == nil {
		defer key.Close()
		version, _, _ := key.GetStringValue("pv")
		return State{
			Installed: true, Version: version, PerUser: true,
			Recoverable: true,
			Hint:        "runtime registered per user; a per-machine install is sturdier",
		}
	}
	return State{
		Installed:   false,
		Recoverable: true,
		Hint:        "install the Evergreen WebView2 Runtime, then start Kitodock again",
	}
}
