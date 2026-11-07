// Package nodejs manages side-by-side Node.js installations under the
// environment's nodejs/ directory, mirroring internal/php's shape: a
// "current" junction for plain terminal use and per-series directories.
package nodejs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Manager struct {
	root         string
	readExeVerse func(exe string) (string, error)
}

func New(root string) *Manager {
	return &Manager{root: root, readExeVerse: versionFromExecutable}
}

// UseExeVersionReader swaps the binary-version source (tests).
func (m *Manager) UseExeVersionReader(read func(exe string) (string, error)) {
	m.readExeVerse = read
}

// Installed is one usable Node.js version directory.
type Installed struct {
	FullVersion string // "24.22.2"
	Dir         string
}

var versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+`)

// List returns every installed version, newest first.
func (m *Manager) List() []Installed {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return nil
	}
	var out []Installed
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "current" {
			continue
		}
		dir := filepath.Join(m.root, entry.Name())
		exe := filepath.Join(dir, "node.exe")
		if _, err := os.Stat(exe); err != nil {
			continue
		}
		full, err := m.readExeVerse(exe)
		if err != nil || !versionPattern.MatchString(full) {
			continue
		}
		out = append(out, Installed{FullVersion: full, Dir: dir})
	}
	sort.Slice(out, func(i, j int) bool {
		return CompareVersions(out[i].FullVersion, out[j].FullVersion) > 0
	})
	return out
}

// HasVersion reports whether an exact full version is installed.
func (m *Manager) HasVersion(version string) bool {
	for _, v := range m.List() {
		if v.FullVersion == version {
			return true
		}
	}
	return false
}

// DirFor returns the directory of an exact full version ("" when absent).
func (m *Manager) DirFor(version string) string {
	for _, v := range m.List() {
		if v.FullVersion == version {
			return v.Dir
		}
	}
	return ""
}

// ActivePath is where "current" points ("" when unset).
func (m *Manager) ActivePath() string {
	link := filepath.Join(m.root, "current")
	target, err := os.Readlink(link)
	if err != nil {
		return ""
	}
	if !filepath.IsAbs(target) {
		if abs, err := filepath.Abs(filepath.Join(m.root, target)); err == nil {
			return abs
		}
	}
	return target
}

// SetActive repoints the current junction at an exact full version.
func (m *Manager) SetActive(version string) error {
	dir := m.DirFor(version)
	if dir == "" {
		return fmt.Errorf("node %s is not installed", version)
	}
	link := filepath.Join(m.root, "current")
	os.Remove(link)
	if err := os.Symlink(dir, link); err != nil {
		cmd := exec.Command("cmd", "/c", "mklink", "/J", link, dir)
		cmd.SysProcAttr = hideWindow()
		if out, linkErr := cmd.CombinedOutput(); linkErr != nil {
			return fmt.Errorf("link nodejs/current to %s: %v: %s", version, linkErr, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// CompareVersions orders dotted versions numerically field by field.
func CompareVersions(a, b string) int {
	nums := func(s string) []int {
		var out []int
		for _, part := range strings.Split(s, ".") {
			n, _ := strconv.Atoi(part)
			out = append(out, n)
		}
		return out
	}
	pa, pb := nums(a), nums(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		x, y := 0, 0
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	return 0
}
