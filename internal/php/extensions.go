package php

// Extension management on top of a version's php.ini: list what the ini
// offers (shipped extension DLLs), what is actually loaded, and toggle
// entries one by one. DLLs the distribution does not ship are reported
// unavailable — fetching PECL binaries is catalog work, not ini work.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Extension is one extension slot in a PHP version's ini.
type Extension struct {
	Name string `json:"name"`
	// Enabled reflects the ini entry (commented or absent = false).
	Enabled bool `json:"enabled"`
	// Shipped marks a DLL present in the version's ext directory; toggling
	// an unshipped name would produce a startup warning.
	Shipped bool `json:"shipped"`
}

var extLineRe = regexp.MustCompile(`^\s*;?\s*extension\s*=\s*(?:php_)?([A-Za-z0-9_]+)\.dll`)

// Extensions lists every extension the version's php.ini mentions (either
// state) plus every DLL shipped in ext\ that the ini forgot, with the
// live "loaded" answer from `php -m` when the runtime can run.
func (m *Manager) Extensions(series string) ([]Extension, error) {
	dir := m.DirFor(series)
	if dir == "" {
		return nil, fmt.Errorf("PHP %s is not installed", series)
	}
	iniPath := filepath.Join(dir, "php.ini")
	lines, err := readIniLines(iniPath)
	if err != nil {
		return nil, fmt.Errorf("php.ini for %s: %w", series, err)
	}

	byName := map[string]*Extension{}
	// Names the ini references.
	for _, line := range lines {
		match := extLineRe.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		name := strings.ToLower(match[1])
		enabled := !strings.HasPrefix(strings.TrimSpace(line), ";")
		if existing, ok := byName[name]; ok {
			existing.Enabled = existing.Enabled || enabled
			continue
		}
		byName[name] = &Extension{Name: name, Enabled: enabled}
	}
	// DLLs shipped but absent from the ini.
	extDir := filepath.Join(dir, "ext")
	if entries, err := os.ReadDir(extDir); err == nil {
		for _, e := range entries {
			name := strings.TrimSuffix(strings.TrimPrefix(e.Name(), "php_"), ".dll")
			if e.IsDir() || strings.EqualFold(name, e.Name()) {
				continue // not a php_<name>.dll
			}
			name = strings.ToLower(name)
			if _, ok := byName[name]; !ok {
				byName[name] = &Extension{Name: name, Enabled: false}
			}
		}
	}

	out := make([]Extension, 0, len(byName))
	for _, ext := range byName {
		ext.Shipped = fileExists(filepath.Join(extDir, "php_"+ext.Name+".dll"))
		out = append(out, *ext)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// SetExtension toggles one extension's ini entry. Disabling comments the
// line out (history stays visible to the user); enabling activates or
// appends it. Refuses names whose DLL the version does not ship.
func (m *Manager) SetExtension(series, name string, enabled bool) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if !regexp.MustCompile(`^[a-z0-9_]+$`).MatchString(name) {
		return fmt.Errorf("invalid extension name %q", name)
	}
	dir := m.DirFor(series)
	if dir == "" {
		return fmt.Errorf("PHP %s is not installed", series)
	}
	iniPath := filepath.Join(dir, "php.ini")
	lines, err := readIniLines(iniPath)
	if err != nil {
		return fmt.Errorf("php.ini for %s: %w", series, err)
	}
	if enabled && !fileExists(filepath.Join(dir, "ext", "php_"+name+".dll")) {
		return fmt.Errorf("PHP %s does not ship php_%s.dll", series, name)
	}

	entry := "extension=php_" + name + ".dll"
	commented := "; " + entry
	re := regexp.MustCompile(`^\s*;?\s*extension\s*=\s*(?:php_)?` + regexp.QuoteMeta(name) + `\.dll`)
	changed := false
	for i, line := range lines {
		if !re.MatchString(line) {
			continue
		}
		target := commented
		if enabled {
			target = entry
		}
		if lines[i] != target {
			lines[i] = target
			changed = true
		}
	}
	if enabled && !changed {
		lines = append(lines, entry)
	}
	if !changed && !enabled {
		return nil // disabling something already off is a no-op
	}
	return writeIniLines(iniPath, lines)
}

// LoadedExtensions runs `php -m` in a version's directory and returns the
// lowercase module list ("" slice when the runtime cannot run — callers
// treat loaded-state as best effort).
func (m *Manager) LoadedExtensions(series string) []string {
	dir := m.DirFor(series)
	if dir == "" {
		return nil
	}
	exe := filepath.Join(dir, "php.exe")
	if !fileExists(exe) {
		return nil
	}
	cmd := exec.Command(exe, "-m")
	cmd.Dir = dir
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		if name := strings.ToLower(strings.TrimSpace(line)); name != "" && !strings.HasPrefix(name, "[") {
			names = append(names, name)
		}
	}
	return names
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
