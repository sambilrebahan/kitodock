package php

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeVersionDir builds a version directory with a php.ini, an ext\
// folder of shipped DLLs, and (optionally) a stand-in php.exe.
func fakeVersionDir(t *testing.T, withExe bool) string {
	t.Helper()
	root := t.TempDir()
	mgr := New(root)
	dir := filepath.Join(root, "8.3")
	if err := os.MkdirAll(filepath.Join(dir, "ext"), 0o755); err != nil {
		t.Fatal(err)
	}
	ini := `engine = On
extension=php_curl.dll
; extension=php_mbstring.dll
; extension=php_xdebug.dll
`
	if err := os.WriteFile(filepath.Join(dir, "php.ini"), []byte(ini), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, dll := range []string{"php_curl.dll", "php_mbstring.dll", "php_gd.dll", "php_pdo_mysql.dll"} {
		if err := os.WriteFile(filepath.Join(dir, "ext", dll), []byte("MZ"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if withExe {
		// A stub exe that "runs" — LoadedExtensions treats failure as
		// no-data, so a non-executable placeholder is enough to exercise
		// the nil path; the parse path is covered via the ini assertions.
		os.WriteFile(filepath.Join(dir, "php.exe"), []byte("stub"), 0o644)
	}
	_ = mgr
	return dir
}

func newFakeManager(t *testing.T) (*Manager, string) {
	dir := fakeVersionDir(t, true)
	mgr := New(filepath.Dir(dir))
	mgr.UseExeVersionReader(func(string) (string, error) { return "8.3.0", nil })
	return mgr, dir
}

func TestExtensionsListsIniAndShipped(t *testing.T) {
	mgr, _ := newFakeManager(t)

	exts, err := mgr.Extensions("8.3")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Extension{}
	for _, e := range exts {
		byName[e.Name] = e
	}
	// Ini-referenced entries carry their state.
	if e := byName["curl"]; !e.Enabled || !e.Shipped {
		t.Fatalf("curl = %+v", e)
	}
	if e := byName["mbstring"]; e.Enabled || !e.Shipped {
		t.Fatalf("mbstring = %+v", e)
	}
	if e := byName["xdebug"]; e.Shipped {
		t.Fatalf("xdebug should be unshipped: %+v", e)
	}
	// Shipped-but-unlisted DLLs appear as off.
	if e := byName["gd"]; e.Enabled || !e.Shipped {
		t.Fatalf("gd = %+v", e)
	}
	// Sorted output.
	for i := 1; i < len(exts); i++ {
		if exts[i-1].Name > exts[i].Name {
			t.Fatal("extensions not sorted")
		}
	}
}

func TestSetExtensionTogglesIni(t *testing.T) {
	mgr, dir := newFakeManager(t)

	// Enable a commented shipped extension.
	if err := mgr.SetExtension("8.3", "mbstring", true); err != nil {
		t.Fatal(err)
	}
	ini := readFile(t, filepath.Join(dir, "php.ini"))
	if !strings.Contains(ini, "extension=php_mbstring.dll") ||
		strings.Contains(ini, "; extension=php_mbstring.dll") {
		t.Fatalf("mbstring not enabled:\n%s", ini)
	}

	// Disable it: the line is commented, not deleted.
	if err := mgr.SetExtension("8.3", "mbstring", false); err != nil {
		t.Fatal(err)
	}
	ini = readFile(t, filepath.Join(dir, "php.ini"))
	if !strings.Contains(ini, "; extension=php_mbstring.dll") {
		t.Fatalf("mbstring line not preserved commented:\n%s", ini)
	}

	// Enable a never-mentioned shipped extension: appended.
	if err := mgr.SetExtension("8.3", "pdo_mysql", true); err != nil {
		t.Fatal(err)
	}
	ini = readFile(t, filepath.Join(dir, "php.ini"))
	if strings.Count(ini, "extension=php_pdo_mysql.dll") != 1 {
		t.Fatalf("pdo_mysql not appended once:\n%s", ini)
	}

	// Unshipped names are refused; malformed names too.
	if err := mgr.SetExtension("8.3", "xdebug", true); err == nil {
		t.Fatal("unshipped dll accepted")
	}
	if err := mgr.SetExtension("8.3", "../evil", true); err == nil {
		t.Fatal("malformed name accepted")
	}

	// State round-trips through Extensions.
	exts, _ := mgr.Extensions("8.3")
	for _, e := range exts {
		if e.Name == "pdo_mysql" && !e.Enabled {
			t.Fatal("pdo_mysql state lost")
		}
	}
}

func TestExtensionsUnknownSeries(t *testing.T) {
	mgr := New(t.TempDir())
	if _, err := mgr.Extensions("9.9"); err == nil {
		t.Fatal("unknown series accepted")
	}
	if err := mgr.SetExtension("9.9", "curl", true); err == nil {
		t.Fatal("unknown series set accepted")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
