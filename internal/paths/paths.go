// Package paths is the single authority for where Kitodock keeps everything
// on disk. Every other package resolves locations through it, so moving the
// whole environment only ever means changing the root here.
//
// Root resolution order (decided once, at process start):
//
//  1. the KITODOCK_ROOT environment variable — the test and support escape
//     hatch, which wins over everything;
//  2. the directory holding the executable, when it contains the
//     ".kitodock-portable" marker shipped inside the portable ZIP — that
//     folder *is* the environment;
//  3. C:\kitodock — the fixed install location for setup-based installs.
package paths

import (
	"os"
	"path/filepath"
)

const (
	// EnvRoot is the environment variable that overrides the install root.
	EnvRoot = "KITODOCK_ROOT"

	// PortableMarker names the flag file that marks a portable distribution.
	PortableMarker = ".kitodock-portable"

	// fixedRoot is where installed (non-portable) environments live.
	fixedRoot = `C:\kitodock`
)

var (
	root     string
	portable bool
)

// resolve is split out so tests can exercise every branch with their own
// directories instead of relying on where the test binary happens to sit.
func resolve(exeDir, envValue string) (string, bool) {
	if envValue != "" {
		return envValue, false
	}
	if exeDir != "" {
		if _, err := os.Stat(filepath.Join(exeDir, PortableMarker)); err == nil {
			return exeDir, true
		}
	}
	return fixedRoot, false
}

func init() {
	exeDir := ""
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}
	root, portable = resolve(exeDir, os.Getenv(EnvRoot))
}

// Root is the environment root this process runs against.
func Root() string { return root }

// IsPortable reports whether the executable lives inside a portable
// distribution, which changes how updates are applied later.
func IsPortable() bool { return portable }

// ConfigFile is the path of the persisted preferences document.
func ConfigFile() string { return filepath.Join(Root(), "config.json") }

// StateDir holds the app's persistent state documents (notification history,
// route table, and so on).
func StateDir() string { return filepath.Join(Root(), "state") }

// ProjectsDir is the default folder projects are created under. The user may
// point the config elsewhere; this is only the default.
func ProjectsDir() string { return filepath.Join(Root(), "www") }

// LogsDir is where rotating application logs will land once logging ships.
func LogsDir() string { return filepath.Join(Root(), "logs") }

// Service homes. Each service owns one folder under the root; Phase 1 wires
// nginx, MySQL, Redis and Mailpit, the rest join with their phases.
func NginxDir() string    { return filepath.Join(Root(), "nginx") }
func ApacheDir() string   { return filepath.Join(Root(), "apache") }
func CaddyDir() string    { return filepath.Join(Root(), "caddy") }
func MySQLDir() string    { return filepath.Join(Root(), "mysql") }
func PostgresDir() string { return filepath.Join(Root(), "postgres") }
func RedisDir() string    { return filepath.Join(Root(), "redis") }
func MailpitDir() string  { return filepath.Join(Root(), "mailpit") }
func PHPDir() string      { return filepath.Join(Root(), "php") }
func CmderRoot() string   { return filepath.Join(Root(), "cmder") }
func NodeDir() string     { return filepath.Join(Root(), "nodejs") }

// DownloadsCache is where installer archives wait for extraction. It lives
// in the OS temp area on purpose: it is a cache, never user data, and a
// portable environment should not carry hundreds of megabytes of zips
// around.
func DownloadsCache() string {
	return filepath.Join(os.TempDir(), "kitodock-cache")
}
