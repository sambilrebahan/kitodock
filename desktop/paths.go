package main

// Local path helpers for the serving wiring (kept here so serving.go stays
// about behavior; internal/paths stays import-free of everything).

import (
	"os"
	"path/filepath"

	"kitodock/internal/paths"
)

func kitodockNginxDir() string      { return paths.NginxDir() }
func kitodockNginxConfDir() string  { return filepath.Join(paths.NginxDir(), "conf") }
func kitodockApacheConfDir() string { return filepath.Join(paths.ApacheDir(), "conf") }
func kitodockCaddyDir() string      { return paths.CaddyDir() }
func kitodockPHPDir() string        { return paths.PHPDir() }

func realHostsFile() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "drivers", "etc", "hosts")
}
