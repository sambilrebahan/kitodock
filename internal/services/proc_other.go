//go:build !windows

package services

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"

	"kitodock/internal/vcruntime"
)

func applyHiddenWindow(*exec.Cmd) {}

// Off Windows the process helpers are honest stubs: Kitodock's supervision
// semantics are Windows-shaped in Phase 1.
func killTree(int) error { return errors.New("process trees are Windows-only here") }

func killImage(base string) error { return errors.New("process images are Windows-only here") }

func parentDir(exe string) string { return filepath.Dir(exe) }

func baseName(exe string) string { return strings.ToLower(filepath.Base(exe)) }

func smokeCheck(exe, redistURL string) error { return vcruntime.Check(exe, redistURL) }
