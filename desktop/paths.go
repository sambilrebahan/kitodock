package main

// Local path helpers for the serving wiring (kept here so serving.go stays
// about behavior; internal/paths stays import-free of everything).

import (
	"path/filepath"

	"kitodock/internal/paths"
)

func kitodockNginxDir() string      { return paths.NginxDir() }
func kitodockNginxConfDir() string  { return filepath.Join(paths.NginxDir(), "conf") }
func kitodockApacheConfDir() string { return filepath.Join(paths.ApacheDir(), "conf") }
func kitodockCaddyDir() string      { return paths.CaddyDir() }
func kitodockPHPDir() string        { return paths.PHPDir() }

func realHostsFile() string {
	return `C:\Windows\System32\drivers\etc\hosts`
}
