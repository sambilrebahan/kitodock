package controllers

// The updates controller: release checks against GitHub, verified
// downloads, and the apply step (MSI relaunch or portable swap). The
// version string comes from internal/version so builds stamp it once.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"kitodock/internal/updater"
	"kitodock/internal/version"
	"kitodock/internal/webviewruntime"
)

// Updates wraps release checking and staged applies.
type Updates struct {
	bus  *EventBus
	repo string
	// stagedPath is the verified artifact awaiting apply.
	stagedPath string
}

// NewUpdates builds the controller; repo is "owner/name".
func NewUpdates(bus *EventBus, repo string) *Updates {
	return &Updates{bus: bus, repo: repo}
}

// UpdateCheck is one release lookup's result.
type UpdateCheck struct {
	Current    string `json:"current"`
	Latest     string `json:"latest"`
	Newer      bool   `json:"newer"`
	ReleaseURL string `json:"releaseUrl,omitempty"`
	AssetName  string `json:"assetName,omitempty"`
	Error      string `json:"error,omitempty"`
}

// CheckUpdates looks up the newest release.
func (u *Updates) CheckUpdates() UpdateCheck {
	check := updater.Checker{Repo: u.repo, Current: version.Version}
	checkResult := UpdateCheck{Current: version.Version}
	rel, err := check.Latest(context.Background())
	if err != nil {
		checkResult.Error = err.Error()
		return checkResult
	}
	checkResult.Latest = rel.TagName
	checkResult.Newer = updater.IsNewer(version.Version, rel.TagName)
	checkResult.ReleaseURL = rel.HTMLURL
	suffix := "_windows_amd64.msi"
	if runtime.GOOS != "windows" {
		suffix = "_linux_amd64.tar.gz"
	}
	if asset := rel.FindAsset(suffix); asset != nil {
		checkResult.AssetName = asset.Name
	}
	return checkResult
}

// DownloadUpdate fetches and verifies the newest artifact, staging it for
// apply. Returns the staged path.
func (u *Updates) DownloadUpdate() (string, error) {
	check := updater.Checker{Repo: u.repo, Current: version.Version}
	rel, err := check.Latest(context.Background())
	if err != nil {
		return "", err
	}
	suffix := "_windows_amd64.msi"
	if runtime.GOOS != "windows" {
		suffix = "_linux_amd64.tar.gz"
	}
	asset := rel.FindAsset(suffix)
	if asset == nil {
		return "", fmt.Errorf("release %s has no %s artifact", rel.TagName, suffix)
	}
	body, err := updater.DownloadAndVerify(context.Background(), nil,
		asset.BrowserDownloadURL, asset.BrowserDownloadURL+".sha256", asset.Size)
	if err != nil {
		return "", err
	}
	staged := filepath.Join(os.TempDir(), "kitodock-update", asset.Name)
	if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(staged, body, 0o644); err != nil {
		return "", err
	}
	u.stagedPath = staged
	return staged, nil
}

// ApplyUpdate runs the staged installer (MSI) and asks the caller to
// relaunch. Portable distributions get the staged path back to swap
// manually — we refuse to self-replace a running exe.
func (u *Updates) ApplyUpdate() (map[string]any, error) {
	if u.stagedPath == "" {
		return nil, fmt.Errorf("no update staged; download first")
	}
	if filepath.Ext(u.stagedPath) != ".msi" {
		return map[string]any{
			"portable": true, "stagedPath": u.stagedPath,
			"hint": "replace the folder contents with the staged archive while Kitodock is closed",
		}, nil
	}
	cmd := exec.Command("msiexec", "/i", u.stagedPath)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start installer: %w", err)
	}
	return map[string]any{"installer": "started", "relaunch": true}, nil
}

// WebViewRuntimeState exposes the runtime health check for diagnostics.
func (u *Updates) WebViewRuntimeState() webviewruntime.State {
	return webviewruntime.Inspect()
}
