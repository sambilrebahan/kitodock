// Package projects answers the two questions the whole app is built on:
// what kind of project is in this folder, and which runtimes does it
// declare. Detection is read-only (never executes project code, never
// writes preferences) and conservative — an unrecognized folder is a
// plain site, not an error.
package projects

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Kind is what a project is.
type Kind string

const (
	KindLaravel   Kind = "laravel"
	KindWordPress Kind = "wordpress"
	KindPlain     Kind = "plain"
)

// Project is one folder under the projects root, merged with the user's
// saved preferences.
type Project struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Kind    Kind   `json:"kind"`
	DocRoot string `json:"docRoot"`
	SSL     bool   `json:"ssl"`
	// JSFramework and PackageManager are informational until Phase 2b.
	JSFramework    string `json:"jsFramework,omitempty"`
	PackageManager string `json:"packageManager,omitempty"`
}

// Detection is what reading a folder alone reveals.
type Detection struct {
	Kind           Kind
	DocRoot        string
	JSFramework    string
	PackageManager string
}

// Detect inspects a folder: Laravel needs artisan plus laravel/framework
// in composer.json (the artisan file alone is copy-pasted far too often),
// WordPress is wp-config.php or wp-load.php, everything else is plain.
func Detect(path string) Detection {
	detected := Detection{Kind: KindPlain, DocRoot: path}

	composer := readComposer(path)
	if fileExists(filepath.Join(path, "artisan")) && composer.hasDependency("laravel/framework") {
		detected.Kind = KindLaravel
		detected.DocRoot = filepath.Join(path, "public")
	} else if fileExists(filepath.Join(path, "wp-config.php")) || fileExists(filepath.Join(path, "wp-load.php")) {
		detected.Kind = KindWordPress
	}

	if pkg := readPackageJSON(path); pkg != nil {
		detected.JSFramework = pkg.mainFramework()
		detected.PackageManager = pkg.managerName()
	}
	if detected.PackageManager == "" {
		detected.PackageManager = managerFromLockfiles(path)
	}
	return detected
}

// --- composer.json -------------------------------------------------------

type composerFile struct {
	Require map[string]string `json:"require"`
}

func readComposer(path string) *composerFile {
	data, err := os.ReadFile(filepath.Join(path, "composer.json"))
	if err != nil {
		return &composerFile{}
	}
	var doc composerFile
	if err := json.Unmarshal(data, &doc); err != nil {
		return &composerFile{}
	}
	return &doc
}

func (c *composerFile) hasDependency(name string) bool {
	_, ok := c.Require[name]
	return ok
}

// PhpConstraint returns the project's declared PHP requirement, "" when it
// declares none.
func PhpConstraint(path string) string {
	return readComposer(path).Require["php"]
}

// --- package.json --------------------------------------------------------

type packageFile struct {
	PackageManager string            `json:"packageManager"`
	Engines        map[string]string `json:"engines"`
	Dependencies   map[string]string `json:"dependencies"`
}

func readPackageJSON(path string) *packageFile {
	data, err := os.ReadFile(filepath.Join(path, "package.json"))
	if err != nil {
		return nil
	}
	var doc packageFile
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	return &doc
}

func (p *packageFile) mainFramework() string {
	// Highest-signal first; one label per project is enough for the UI.
	for _, candidate := range []struct{ dep, label string }{
		{"next", "next"}, {"nuxt", "nuxt"}, {"astro", "astro"}, {"vite", "vite"}, {"vue", "vue"}, {"react", "react"},
	} {
		if _, ok := p.Dependencies[candidate.dep]; ok {
			return candidate.label
		}
	}
	return ""
}

func (p *packageFile) managerName() string {
	// packageManager looks like "pnpm@9.1.0": the name before the @ wins.
	if name := p.PackageManager; name != "" {
		if i := strings.IndexByte(name, '@'); i > 0 {
			return name[:i]
		}
		return name
	}
	return ""
}

func managerFromLockfiles(path string) string {
	for _, candidate := range []struct{ file, name string }{
		{"pnpm-lock.yaml", "pnpm"},
		{"yarn.lock", "yarn"},
		{"bun.lockb", "bun"},
		{"bun.lock", "bun"},
		{"package-lock.json", "npm"},
	} {
		if fileExists(filepath.Join(path, candidate.file)) {
			return candidate.name
		}
	}
	return ""
}

// NodeConstraint returns the project's declared Node requirement from
// package.json engines, "" when it declares none.
func NodeConstraint(path string) string {
	if pkg := readPackageJSON(path); pkg != nil {
		return pkg.Engines["node"]
	}
	return ""
}

// --- scanner -------------------------------------------------------------

// Preferences is the subset of saved configuration the scanner merges in
// (owned by internal/config; kept structural to avoid an import cycle).
type Preferences struct {
	SSL map[string]bool
}

// Scan lists projects under root as Name-sorted Projects, one per
// immediate directory that looks like a project at all.
func Scan(root string, prefs Preferences) []Project {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []Project
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		detected := Detect(path)
		project := Project{
			Name:           entry.Name(),
			Path:           path,
			Kind:           detected.Kind,
			DocRoot:        detected.DocRoot,
			SSL:            prefs.SSL[entry.Name()],
			JSFramework:    detected.JSFramework,
			PackageManager: detected.PackageManager,
		}
		out = append(out, project)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
