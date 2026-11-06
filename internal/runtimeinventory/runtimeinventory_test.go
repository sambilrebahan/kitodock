package runtimeinventory

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReadsTheBinaryOwnVersion(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("version resources need Windows")
	}
	// The test binary itself is a PE built by Go with a version resource —
	// read from our own executable, which always exists and is always ours.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	meta, err := Read(exe)
	if err != nil {
		t.Skipf("this Go build carries no version resource: %v", err)
	}
	if !looksLikeVersion(meta.FullVersion) {
		t.Fatalf("FullVersion = %q, want something dot-numeric", meta.FullVersion)
	}
}

func TestMissingFileFailsCleanly(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("version resources need Windows")
	}
	if _, err := Read(filepath.Join(t.TempDir(), "ghost.exe")); err == nil {
		t.Fatal("reading a missing executable must fail")
	}
}

func TestLooksLikeVersion(t *testing.T) {
	yes := []string{"8.4.25", "22.22.2", "1.2", "5", "8.6.0beta1", "9.7.0-rc2", "8.4-beta-only"}
	no := []string{"", ".", "x.y", "1..2", "v1.2", ".5", "5."}
	for _, v := range yes {
		if !looksLikeVersion(v) {
			t.Errorf("looksLikeVersion(%q) = false, want true", v)
		}
	}
	for _, v := range no {
		if looksLikeVersion(v) {
			t.Errorf("looksLikeVersion(%q) = true, want false", v)
		}
	}
}
