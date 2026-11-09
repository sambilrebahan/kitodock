// Package updater checks GitHub releases for a newer Kitodock, downloads
// the artifact, verifies its SHA-256 against the published digest, and
// stages an apply command (MSI relaunch or portable swap) for the shell
// to run once the app exits.
package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Release is one GitHub release, reduced to what we need.
type Release struct {
	TagName string  `json:"tag_name"`
	HTMLURL string  `json:"html_url"`
	Assets  []Asset `json:"assets"`
}

// Asset is one downloadable artifact.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// Getter fetches a URL; tests stub it. Nil uses net/http.
type Getter func(ctx context.Context, url string) ([]byte, error)

// Checker looks up releases.
type Checker struct {
	// Repo is "owner/name" on GitHub.
	Repo string
	// Current is the running version (e.g. "0.6.0"); a release whose
	// tag differs and sorts newer triggers an update.
	Current string
	Get     Getter
}

// Latest returns the newest published release.
func (c *Checker) Latest(ctx context.Context) (*Release, error) {
	get := c.Get
	if get == nil {
		get = httpGet
	}
	body, err := get(ctx, fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", c.Repo))
	if err != nil {
		return nil, fmt.Errorf("release lookup: %w", err)
	}
	var rel Release
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, fmt.Errorf("release parse: %w", err)
	}
	if rel.TagName == "" {
		return nil, fmt.Errorf("no published release")
	}
	return &rel, nil
}

// IsNewer compares a tag against Current, numerically by dot segments.
func IsNewer(current, tag string) bool {
	cur := versionParts(current)
	rel := versionParts(strings.TrimPrefix(tag, "v"))
	for i := 0; i < 3; i++ {
		if rel[i] != cur[i] {
			return rel[i] > cur[i]
		}
	}
	return false
}

// FindAsset picks the MSI or portable zip for windows-amd64 by naming
// convention (…windows_amd64.msi / …windows_amd64.zip).
func (r *Release) FindAsset(suffix string) *Asset {
	for i := range r.Assets {
		if strings.HasSuffix(strings.ToLower(r.Assets[i].Name), suffix) {
			return &r.Assets[i]
		}
	}
	return nil
}

// DownloadAndVerify fetches an asset and checks it against the published
// sidecar digest (asset name + ".sha256", hex text).
func DownloadAndVerify(ctx context.Context, get Getter, assetURL, digestURL string, wantSize int64) ([]byte, error) {
	if get == nil {
		get = httpGet
	}
	digestBody, err := get(ctx, digestURL)
	if err != nil {
		return nil, fmt.Errorf("digest download: %w", err)
	}
	want := strings.ToLower(strings.TrimSpace(string(digestBody)))
	// Accept "<hex>  <file>" digest file formatting.
	if fields := strings.Fields(want); len(fields) > 0 {
		want = fields[0]
	}
	if len(want) != 64 {
		return nil, fmt.Errorf("malformed digest")
	}

	body, err := get(ctx, assetURL)
	if err != nil {
		return nil, fmt.Errorf("artifact download: %w", err)
	}
	if wantSize > 0 && int64(len(body)) != wantSize {
		return nil, fmt.Errorf("artifact size %d, expected %d", len(body), wantSize)
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != want {
		return nil, fmt.Errorf("sha256 mismatch: got %s", got[:12])
	}
	return body, nil
}

// versionParts parses "v1.2.3" into [1,2,3]; junk segments read as 0.
func versionParts(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	var out [3]int
	for i, part := range strings.SplitN(v, ".", 3) {
		if i >= 3 {
			break
		}
		n := 0
		for _, r := range part {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		out[i] = n
	}
	return out
}

// httpGet is the production getter with a timeout.
func httpGet(ctx context.Context, url string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d for %s", resp.StatusCode, url)
	}
	return io.ReadAll(resp.Body)
}
