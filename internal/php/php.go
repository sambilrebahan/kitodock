// Package php manages side-by-side PHP installations under the
// environment's php/ directory: which versions exist, which is "current"
// for plain terminal use, and the php.ini every freshly installed version
// needs before it can serve anything (an absolute extension_dir and the
// bundled extensions enabled — PHP on Windows resolves relative paths
// against the process CWD, which is never what anyone wants).
package php

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

// Manager owns the php/ tree. Version identity comes from the binary's own
// version resource by default; tests inject a reader so fixture trees need
// no real executables.
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

// Installed is one usable PHP version directory.
type Installed struct {
	// Series is the major.minor spelling used everywhere ("8.4").
	Series string
	// FullVersion is what the binary itself reported.
	FullVersion string
	Dir         string
}

var seriesPattern = regexp.MustCompile(`^(\d+)\.(\d+)`)

func seriesOf(version string) (string, bool) {
	m := seriesPattern.FindStringSubmatch(version)
	if m == nil {
		return "", false
	}
	return m[1] + "." + m[2], true
}

// List returns every installed version, newest series first.
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
		exe := filepath.Join(dir, "php.exe")
		full := entry.Name()
		if _, err := os.Stat(exe); err == nil {
			if v, err := m.readExeVerse(exe); err == nil && v != "" {
				full = v
			}
		} else {
			continue // no php.exe: interrupted install, not a version
		}
		series, ok := seriesOf(full)
		if !ok {
			continue
		}
		out = append(out, Installed{Series: series, FullVersion: full, Dir: dir})
	}
	sort.Slice(out, func(i, j int) bool {
		return compareSeries(out[i].Series, out[j].Series) > 0
	})
	return out
}

// HasSeries reports whether a major.minor series is installed.
func (m *Manager) HasSeries(series string) bool {
	for _, v := range m.List() {
		if v.Series == series {
			return true
		}
	}
	return false
}

// DirFor returns the installation directory of a series ("" when absent).
func (m *Manager) DirFor(series string) string {
	for _, v := range m.List() {
		if v.Series == series {
			return v.Dir
		}
	}
	return ""
}

// ActivePath is where the "current" junction points ("" when unset).
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

// ActiveSeries is the active version's major.minor ("" when unset).
func (m *Manager) ActiveSeries() string {
	dir := m.ActivePath()
	if dir == "" {
		return ""
	}
	for _, v := range m.List() {
		if strings.EqualFold(v.Dir, dir) {
			return v.Series
		}
	}
	return ""
}

// EnsureActive points "current" at the newest installed version when the
// junction is missing or dangling — terminals and tools read this link.
func (m *Manager) EnsureActive() {
	if m.ActiveSeries() != "" {
		if _, err := os.Stat(filepath.Join(m.ActivePath(), "php.exe")); err == nil {
			return
		}
	}
	list := m.List()
	if len(list) == 0 {
		return
	}
	_ = m.SetActive(list[0].Series)
}

// SetActive repoints the current junction at a series (creating the ini on
// the way, so the link never leads to a half-ready install).
func (m *Manager) SetActive(series string) error {
	dir := m.DirFor(series)
	if dir == "" {
		return fmt.Errorf("php %s is not installed", series)
	}
	if err := m.EnsureIni(dir); err != nil {
		return err
	}
	link := filepath.Join(m.root, "current")
	os.Remove(link)
	// A junction needs no privilege on Windows and survives without
	// developer mode; a symlink is cleaner where it works.
	if err := os.Symlink(dir, link); err != nil {
		cmd := exec.Command("cmd", "/c", "mklink", "/J", link, dir)
		cmd.SysProcAttr = hideWindow()
		if out, linkErr := cmd.CombinedOutput(); linkErr != nil {
			return fmt.Errorf("link php/current to %s: %v: %s", series, linkErr, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// PortFor is the php-cgi base port of a series: 9000 + major*10 + minor,
// so 8.4→9084 and versions never collide.
func PortFor(series string) int {
	m := seriesPattern.FindStringSubmatch(series)
	if m == nil {
		return 0
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return 9000 + major*10 + minor
}

// ReplicaPort is worker index n's port (stride 100 keeps pools apart).
func ReplicaPort(series string, index int) int {
	return PortFor(series) + index*100
}

// UpstreamName is the nginx upstream id of a series ("php_8_4" — dots are
// not legal there).
func UpstreamName(series string) string {
	return "php_" + strings.ReplaceAll(series, ".", "_")
}

// CgiExe is the FastCGI binary inside a version dir.
func CgiExe(versionDir string) string { return filepath.Join(versionDir, "php-cgi.exe") }

// compareSeries orders "8.10" after "8.9" numerically, not lexically.
func compareSeries(a, b string) int {
	pa := strings.Split(a, ".")
	pb := strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		x, y := 0, 0
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
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
