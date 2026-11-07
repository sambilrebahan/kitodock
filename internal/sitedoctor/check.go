// Package sitedoctor diagnoses project configuration problems. It is
// deliberately diagnose-first: every FixID corresponds to a check that
// found something, and fixing is a separate, explicit action with output
// the user can watch. No check ever repairs anything on its own.
package sitedoctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Severity of a finding.
type Severity string

const (
	OK         Severity = "ok"
	Failed     Severity = "failed"
	Warning    Severity = "warning"
	Optional   Severity = "optional"
	NotChecked Severity = "not-checked"
)

// Finding is one check's verdict.
type Finding struct {
	CheckID  string   `json:"checkId"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	// FixID names the repair when one exists.
	FixID string `json:"fixId,omitempty"`
}

// Report is a full diagnosis.
type Report struct {
	ProjectName string    `json:"projectName"`
	Findings    []Finding `json:"findings"`
}

// HasFailed reports whether anything needs attention.
func (r Report) HasFailed() bool {
	for _, f := range r.Findings {
		if f.Severity == Failed {
			return true
		}
	}
	return false
}

// Target is what a diagnosis runs against.
type Target struct {
	Name           string
	Path           string
	Kind           string // "laravel", "wordpress", "plain"
	DocRoot        string
	PhpExe         string // resolved PHP binary path ("" = none)
	PhpSeries      string
	HasVendor      bool // composer dependencies installed
	HasNodeModules bool
	// VhostExpected is the rendered config the sweep would write, for
	// drift comparison ("" = not computed).
	VhostExpected string
	VhostOnDisk   string
	// HostsEntryPresent is whether the hostname's hosts entry exists.
	HostsEntryPresent bool
	// FpmRunning reports whether the php-cgi pool for this project's
	// series is running.
	FpmRunning bool
	// FpmExpected reports whether one is expected at all.
	FpmExpected bool
}

// Dependencies are the injected probes the checks need.
type Dependencies struct {
	// Database probes connectivity; both are nil-safe (check skips).
	Database func(phpExe, kind, name string) (string, error)
	// Migrations reports pending count; nil-safe.
	Migrations func(phpExe, path string) (int, error)
}

// Check runs every diagnosis for a target.
func Check(target Target, deps Dependencies) Report {
	report := Report{ProjectName: target.Name}

	report.add(checkDocRoot(target))
	report.add(checkRuntime(target))
	report.add(checkVendor(target))
	report.add(checkNodeModules(target))
	report.add(checkEnvFile(target))
	report.add(checkAppKey(target))
	report.add(checkStorageLink(target))
	report.add(checkVhostDrift(target))
	report.add(checkHosts(target))
	report.add(checkFpm(target))
	report.add(checkDatabase(target, deps))
	report.add(checkMigrations(target, deps))

	return report
}

func (r *Report) add(f Finding) { r.Findings = append(r.Findings, f) }

// --- individual checks ------------------------------------------------------

func checkDocRoot(t Target) Finding {
	if t.DocRoot == "" {
		return Finding{CheckID: "doc_root", Severity: Warning,
			Message: "no document root detected"}
	}
	if _, err := os.Stat(t.DocRoot); err != nil {
		return Finding{CheckID: "doc_root", Severity: Failed,
			Message: fmt.Sprintf("document root %s does not exist", t.DocRoot)}
	}
	return Finding{CheckID: "doc_root", Severity: OK, Message: "document root present"}
}

func checkRuntime(t Target) Finding {
	if t.PhpExe == "" && t.Kind != "plain" {
		return Finding{CheckID: "runtime", Severity: Failed,
			Message: "no PHP runtime resolved for this project"}
	}
	if t.PhpExe == "" {
		return Finding{CheckID: "runtime", Severity: NotChecked, Message: "static project, no runtime needed"}
	}
	if _, err := os.Stat(t.PhpExe); err != nil {
		return Finding{CheckID: "runtime", Severity: Failed,
			Message: fmt.Sprintf("resolved PHP %s not on disk", t.PhpExe)}
	}
	return Finding{CheckID: "runtime", Severity: OK, Message: "PHP " + t.PhpSeries + " resolved"}
}

func checkVendor(t Target) Finding {
	if t.Kind != "laravel" && t.Kind != "wordpress" {
		return Finding{CheckID: "vendor", Severity: NotChecked, Message: "not a composer project"}
	}
	if !t.HasVendor {
		return Finding{CheckID: "vendor", Severity: Failed,
			Message: "composer dependencies not installed", FixID: "composer_dependencies"}
	}
	return Finding{CheckID: "vendor", Severity: OK, Message: "vendor present"}
}

func checkNodeModules(t Target) Finding {
	if !t.HasNodeModules {
		if t.Kind == "plain" {
			return Finding{CheckID: "node_modules", Severity: NotChecked, Message: "no JS project detected"}
		}
		return Finding{CheckID: "node_modules", Severity: Warning,
			Message: "node_modules not installed", FixID: "node_dependencies"}
	}
	return Finding{CheckID: "node_modules", Severity: OK, Message: "node_modules present"}
}

func checkEnvFile(t Target) Finding {
	if t.Kind != "laravel" {
		return Finding{CheckID: "env_file", Severity: NotChecked, Message: "not a Laravel project"}
	}
	envPath := filepath.Join(t.Path, ".env")
	if _, err := os.Stat(envPath); err != nil {
		if _, statErr := os.Stat(filepath.Join(t.Path, ".env.example")); statErr == nil {
			return Finding{CheckID: "env_file", Severity: Failed,
				Message: ".env missing (.env.example exists)", FixID: "env"}
		}
		return Finding{CheckID: "env_file", Severity: Failed,
			Message: ".env missing and no .env.example to copy", FixID: "env"}
	}
	return Finding{CheckID: "env_file", Severity: OK, Message: ".env present"}
}

func checkAppKey(t Target) Finding {
	if t.Kind != "laravel" {
		return Finding{CheckID: "app_key", Severity: NotChecked, Message: "not a Laravel project"}
	}
	value := dotenvLookup(t.Path, "APP_KEY")
	if value == "" {
		return Finding{CheckID: "app_key", Severity: Failed,
			Message: "APP_KEY not set", FixID: "app_key"}
	}
	if strings.HasPrefix(value, "base64:") == false && len(value) < 32 {
		return Finding{CheckID: "app_key", Severity: Warning,
			Message: "APP_KEY looks too short", FixID: "app_key"}
	}
	return Finding{CheckID: "app_key", Severity: OK, Message: "APP_KEY set"}
}

func checkStorageLink(t Target) Finding {
	if t.Kind != "laravel" {
		return Finding{CheckID: "storage_link", Severity: NotChecked, Message: "not a Laravel project"}
	}
	publicStorage := filepath.Join(t.DocRoot, "storage")
	info, err := os.Lstat(publicStorage)
	if err != nil {
		return Finding{CheckID: "storage_link", Severity: Optional,
			Message: "public/storage not linked", FixID: "storage_link"}
	}
	// A junction or symlink is fine; a real directory means the link was
	// never created.
	if info.Mode()&os.ModeSymlink == 0 && info.IsDir() && !isReparsePoint(info) {
		return Finding{CheckID: "storage_link", Severity: Optional,
			Message: "public/storage is a real directory, not a link", FixID: "storage_link"}
	}
	return Finding{CheckID: "storage_link", Severity: OK, Message: "storage linked"}
}

func checkVhostDrift(t Target) Finding {
	if t.VhostExpected == "" || t.VhostOnDisk == "" {
		return Finding{CheckID: "vhost", Severity: NotChecked, Message: "vhost not computed"}
	}
	if normalizeConfig(t.VhostExpected) != normalizeConfig(t.VhostOnDisk) {
		return Finding{CheckID: "vhost", Severity: Warning,
			Message: "vhost on disk differs from expected (customized or stale)", FixID: "vhost"}
	}
	return Finding{CheckID: "vhost", Severity: OK, Message: "vhost matches expected"}
}

func checkHosts(t Target) Finding {
	if !t.HostsEntryPresent {
		return Finding{CheckID: "hosts", Severity: Failed,
			Message: "hosts entry missing for this project", FixID: "hosts"}
	}
	return Finding{CheckID: "hosts", Severity: OK, Message: "hosts entry present"}
}

func checkFpm(t Target) Finding {
	if !t.FpmExpected {
		return Finding{CheckID: "php_fpm", Severity: NotChecked, Message: "no PHP pool needed"}
	}
	if !t.FpmRunning {
		return Finding{CheckID: "php_fpm", Severity: Failed,
			Message: "php-cgi pool not running", FixID: "php_fpm"}
	}
	return Finding{CheckID: "php_fpm", Severity: OK, Message: "php-cgi pool running"}
}

func checkDatabase(t Target, deps Dependencies) Finding {
	if deps.Database == nil || t.PhpExe == "" {
		return Finding{CheckID: "database", Severity: NotChecked, Message: "probe unavailable"}
	}
	verdict, err := deps.Database(t.PhpExe, t.Kind, t.Name)
	if err != nil {
		return Finding{CheckID: "database", Severity: Warning,
			Message: err.Error(), FixID: "database"}
	}
	return Finding{CheckID: "database", Severity: OK, Message: verdict}
}

func checkMigrations(t Target, deps Dependencies) Finding {
	if deps.Migrations == nil || t.PhpExe == "" || t.Kind != "laravel" {
		return Finding{CheckID: "migrations", Severity: NotChecked, Message: "not applicable"}
	}
	pending, err := deps.Migrations(t.PhpExe, t.Path)
	if err != nil {
		return Finding{CheckID: "migrations", Severity: NotChecked, Message: "could not read migration status"}
	}
	if pending > 0 {
		return Finding{CheckID: "migrations", Severity: Warning,
			Message: fmt.Sprintf("%d pending migrations", pending), FixID: "migrations"}
	}
	return Finding{CheckID: "migrations", Severity: OK, Message: "migrations up to date"}
}

// --- helpers -----------------------------------------------------------------

func dotenvLookup(projectPath, key string) string {
	for _, name := range []string{".env", ".env.local"} {
		data, err := os.ReadFile(filepath.Join(projectPath, name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, key+"=") {
				return strings.Trim(strings.TrimPrefix(line, key+"="), `"'`)
			}
		}
	}
	return ""
}

// normalizeConfig strips blank lines and trailing whitespace for comparison.
func normalizeConfig(s string) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// isReparsePoint checks Windows junction/symlink attribute (Go's Lstat
// reports ModeSymlink for symlinks, but junctions need this check).
func isReparsePoint(info os.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0 || info.Name() != "" && info.Mode()&0x40000000 != 0
}
