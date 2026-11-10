package php

// PECL binary installs for Windows: the php.net build service publishes
// per-release zip trees of prebuilt DLLs. Installing one extension means
// matching the series' build flavor (thread safety, architecture),
// picking the newest release that ships that flavor, extracting the DLL
// into ext\, and flipping the ini entry. Network seams are injectable so
// tests run against an in-memory server.

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// PeclExtension is one catalog entry offered in the UI.
type PeclExtension struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Note  string `json:"note,omitempty"`
}

// peclCatalog are the roadmap's five plus apcu's cousinOPcache stays core.
var peclCatalog = []PeclExtension{
	{Name: "xdebug", Label: "Xdebug", Note: "step debugging and profiling"},
	{Name: "redis", Label: "Redis", Note: "phpredis client"},
	{Name: "mongodb", Label: "MongoDB", Note: "MongoDB driver"},
	{Name: "apcu", Label: "APCu", Note: "userland object cache"},
	{Name: "imagick", Label: "Imagick", Note: "ImageMagick bindings (needs ImageMagick installed)"},
	{Name: "memcached", Label: "Memcached", Note: "memcached client"},
}

// PeclCatalog returns the installable extensions.
func PeclCatalog() []PeclExtension {
	out := make([]PeclExtension, len(peclCatalog))
	copy(out, peclCatalog)
	return out
}

// PeclResult reports a completed install.
type PeclResult struct {
	Extension string `json:"extension"`
	Version   string `json:"version"`
	File      string `json:"file"`
}

// PeclInstaller downloads and installs PECL DLLs for one series.
type PeclInstaller struct {
	Manager *Manager
	// BaseURL is the releases root; production points at the php.net
	// build service, tests at an in-memory server.
	BaseURL string
	// HTTP fetches a URL (nil: default client with a timeout).
	HTTP func(ctx context.Context, url string) ([]byte, error)
	// ProbeBuild reports the series' thread safety and architecture
	// (nil: run php -i and parse it).
	ProbeBuild func(dir string) (threadSafe bool, arch string)
}

// NewPeclInstaller builds the production installer.
func NewPeclInstaller(m *Manager) *PeclInstaller {
	return &PeclInstaller{
		Manager: m,
		BaseURL: "https://downloads.php.net/~windows/pecl/releases",
		HTTP: func(ctx context.Context, url string) ([]byte, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return nil, err
			}
			resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("status %d for %s", resp.StatusCode, url)
			}
			return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		},
		ProbeBuild: probePhpBuild,
	}
}

// probePhpBuild asks the runtime for its flavor.
func probePhpBuild(dir string) (threadSafe bool, arch string) {
	arch = "x64"
	out, err := runInDir(dir, "php.exe", "-i")
	if err != nil {
		return false, arch
	}
	threadSafe = strings.Contains(out, "Thread Safety => enabled")
	if match := regexp.MustCompile(`Architecture => (\w+)`).FindStringSubmatch(out); match != nil {
		arch = strings.ToLower(match[1])
	}
	return threadSafe, arch
}

// runInDir is a tiny exec helper.
func runInDir(dir, exe string, args ...string) (string, error) {
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Install resolves, downloads and enables one extension for a series.
// progress receives human-readable steps.
func (p *PeclInstaller) Install(ctx context.Context, series, name string, progress func(string)) (*PeclResult, error) {
	dir := p.Manager.DirFor(series)
	if dir == "" {
		return nil, fmt.Errorf("PHP %s is not installed", series)
	}
	threadSafe, arch := p.ProbeBuild(dir)
	flavor := "nts"
	if threadSafe {
		flavor = "ts"
	}
	if progress != nil {
		progress(fmt.Sprintf("build flavor: %s %s", flavor, arch))
	}

	// Newest release that actually ships our flavor for this series.
	versions, err := p.listVersions(ctx, name)
	if err != nil {
		return nil, err
	}
	var file, version string
	for _, candidate := range versions {
		pattern := fmt.Sprintf("php_%s-%s-%s-%s-%s.zip", name, candidate, series, flavor, arch)
		if ok, _ := p.exists(ctx, fmt.Sprintf("%s/%s/%s/%s", p.BaseURL, name, candidate, pattern)); ok {
			file, version = pattern, candidate
			break
		}
	}
	if file == "" {
		return nil, fmt.Errorf("no %s build for PHP %s (%s %s) in the PECL archive", name, series, flavor, arch)
	}
	if progress != nil {
		progress("downloading " + file)
	}
	zipBytes, err := p.HTTP(ctx, fmt.Sprintf("%s/%s/%s/%s", p.BaseURL, name, version, file))
	if err != nil {
		return nil, err
	}

	// Extract the php_<name>.dll into ext\.
	entry, err := findZipEntry(zipBytes, "php_"+name+".dll")
	if err != nil {
		return nil, err
	}
	dest := filepath.Join(dir, "ext", "php_"+name+".dll")
	if err := writeFileSafe(dest, entry); err != nil {
		return nil, err
	}
	if progress != nil {
		progress("enabling in php.ini")
	}
	if err := p.Manager.SetExtension(series, name, true); err != nil {
		return nil, err
	}
	return &PeclResult{Extension: name, Version: version, File: file}, nil
}

// listVersions scrapes the extension's release directory (descending).
func (p *PeclInstaller) listVersions(ctx context.Context, name string) ([]string, error) {
	body, err := p.HTTP(ctx, fmt.Sprintf("%s/%s/", p.BaseURL, name))
	if err != nil {
		return nil, fmt.Errorf("pecl listing: %w", err)
	}
	re := regexp.MustCompile(`href="([0-9]+\.[0-9]+\.[0-9]+)/"`)
	seen := map[string]bool{}
	var versions []string
	for _, match := range re.FindAllStringSubmatch(string(body), -1) {
		if !seen[match[1]] {
			seen[match[1]] = true
			versions = append(versions, match[1])
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versionLess(versions[i], versions[j]) })
	if len(versions) == 0 {
		return nil, fmt.Errorf("no releases found for %s", name)
	}
	return versions, nil
}

// exists checks one URL with a HEAD-ish GET.
func (p *PeclInstaller) exists(ctx context.Context, url string) (bool, error) {
	_, err := p.HTTP(ctx, url)
	return err == nil, nil
}

// versionLess compares dotted numeric versions, newest first ordering.
func versionLess(a, b string) bool {
	pa, pb := dotted(a), dotted(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func dotted(v string) [3]int {
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

// findZipEntry locates one file inside a zip by base name.
func findZipEntry(zipBytes []byte, base string) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("pecl zip: %w", err)
	}
	for _, file := range reader.File {
		if strings.EqualFold(filepath.Base(file.Name), base) {
			rc, err := file.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, 64<<20))
		}
	}
	return nil, fmt.Errorf("zip contains no %s", base)
}

// writeFileSafe writes only inside the version's ext directory.
func writeFileSafe(dest string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dest, data, 0o644)
}
