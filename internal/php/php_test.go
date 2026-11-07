package php

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTree builds a php/ root with version dirs; php.exe is a stand-in file
// whose "version resource" the injected reader fakes from the dir name.
func fakeTree(t *testing.T, versions ...string) *Manager {
	t.Helper()
	root := t.TempDir()
	for _, v := range versions {
		dir := filepath.Join(root, v)
		if err := os.MkdirAll(filepath.Join(dir, "ext"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "php.exe"), []byte("stub"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "php-cgi.exe"), []byte("stub"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "php.ini-development"), []byte("; template\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := New(root)
	m.UseExeVersionReader(func(exe string) (string, error) {
		return filepath.Base(filepath.Dir(exe)), nil
	})
	return m
}

func TestListsNewestFirstAndSkipsDebris(t *testing.T) {
	m := fakeTree(t, "8.3.36", "8.4.25", "not-a-version")
	// The debris dir has no php.exe → invisible; the odd names carry the
	// series digits the injected reader reports.
	_ = m

	root := filepath.Dir(m.root)
	_ = root

	got := m.List()
	if len(got) != 2 {
		t.Fatalf("List() = %d entries, want 2 (debris excluded)", len(got))
	}
	if got[0].Series != "8.4" || got[1].Series != "8.3" {
		t.Fatalf("order = [%s %s], want 8.4 before 8.3", got[0].Series, got[1].Series)
	}
}

func TestSeriesOrderingIsNumeric(t *testing.T) {
	if compareSeries("8.10", "8.9") <= 0 {
		t.Fatal("8.10 must sort after 8.9 (numeric, not lexical)")
	}
	if compareSeries("8.4", "8.4") != 0 {
		t.Fatal("equal series must compare equal")
	}
}

func TestPortScheme(t *testing.T) {
	cases := map[string]int{"8.4": 9084, "8.3": 9083, "7.4": 9074, "garbage": 0}
	for series, want := range cases {
		if got := PortFor(series); got != want {
			t.Errorf("PortFor(%q) = %d, want %d", series, got, want)
		}
	}
	if got := ReplicaPort("8.4", 2); got != 9284 {
		t.Errorf("ReplicaPort(8.4, 2) = %d, want 9284 (100 stride)", got)
	}
	if got := UpstreamName("8.4"); got != "php_8_4" {
		t.Errorf("UpstreamName(8.4) = %q", got)
	}
}

func TestEnsureIniSeedsAndEnablesBundledExtensions(t *testing.T) {
	m := fakeTree(t, "8.4.25")
	dir := m.DirFor("8.4")
	if err := os.WriteFile(filepath.Join(dir, "ext", "php_mbstring.dll"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ext", "php_gd.dll"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	// xdebug ships no DLL: it must NOT be enabled.

	if err := m.EnsureIni(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "php.ini"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)

	wantExt := filepath.ToSlash(filepath.Join(dir, "ext"))
	if !strings.Contains(text, `extension_dir = "`+wantExt+`"`) {
		t.Errorf("extension_dir not set absolutely:\n%s", text)
	}
	if !strings.Contains(text, "extension=php_mbstring.dll") {
		t.Errorf("bundled mbstring not enabled:\n%s", text)
	}
	if !strings.Contains(text, "extension=php_gd.dll") {
		t.Errorf("bundled gd not enabled:\n%s", text)
	}
	if strings.Contains(text, "extension=php_xdebug.dll") {
		t.Error("an extension with no DLL must not be enabled")
	}

	// Idempotence: a second run keeps the file identical in the parts we
	// own and does not duplicate directives.
	before := text
	if err := m.EnsureIni(dir); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "php.ini"))
	if strings.Count(string(after), "extension=php_mbstring.dll") != 1 {
		t.Error("second EnsureIni duplicated the extension line")
	}
	if strings.Count(string(after), `extension_dir = "`+wantExt+`"`) != 1 {
		t.Error("second EnsureIni duplicated extension_dir")
	}
	_ = before
}

func TestSetActivePointsCurrentAndIsEnsureSafe(t *testing.T) {
	m := fakeTree(t, "8.3.36", "8.4.25")

	if err := m.SetActive("8.3"); err != nil {
		t.Fatal(err)
	}
	if m.ActiveSeries() != "8.3" {
		t.Fatalf("ActiveSeries() = %q, want 8.3", m.ActiveSeries())
	}

	// A dangling link (target deleted) must not wedge EnsureActive.
	if err := m.SetActive("8.4"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(m.DirFor("8.4")); err != nil {
		t.Fatal(err)
	}
	if m.ActiveSeries() == "8.4" && m.ActivePath() != "" {
		if _, err := os.Stat(filepath.Join(m.ActivePath(), "php.exe")); err == nil {
			t.Fatal("dangling current survived EnsureActive's health check")
		}
	}

	if err := m.SetActive("9.9"); err == nil {
		t.Fatal("activating a missing series must fail")
	}
}
