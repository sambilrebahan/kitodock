package nodejs

import (
	"os"
	"path/filepath"
	"testing"
)

func fakeTree(t *testing.T, versions ...string) *Manager {
	t.Helper()
	root := t.TempDir()
	for _, v := range versions {
		dir := filepath.Join(root, v)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "node.exe"), []byte("stub"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := New(root)
	m.UseExeVersionReader(func(exe string) (string, error) {
		return filepath.Base(filepath.Dir(exe)), nil
	})
	return m
}

func TestListsNewestFirst(t *testing.T) {
	m := fakeTree(t, "24.22.2", "26.13.0", "22.20.0")
	got := m.List()
	if len(got) != 3 {
		t.Fatalf("List() = %d, want 3", len(got))
	}
	if got[0].FullVersion != "26.13.0" || got[1].FullVersion != "24.22.2" || got[2].FullVersion != "22.20.0" {
		t.Fatalf("order wrong: %v", got)
	}
}

func TestSetActiveAndDirFor(t *testing.T) {
	m := fakeTree(t, "24.22.2", "26.13.0")
	if m.DirFor("24.22.2") == "" {
		t.Fatal("DirFor missed an installed version")
	}
	if err := m.SetActive("24.22.2"); err != nil {
		t.Fatal(err)
	}
	if m.ActivePath() == "" {
		t.Fatal("current junction not readable")
	}
	if got := filepath.Base(m.ActivePath()); got != "24.22.2" {
		t.Fatalf("current points at %q", got)
	}
	if err := m.SetActive("19.0.0"); err == nil {
		t.Fatal("activating a missing version must fail")
	}
}

func TestCompareVersions(t *testing.T) {
	if CompareVersions("10.1.0", "9.9.9") <= 0 {
		t.Error("10.x must beat 9.x numerically")
	}
	if CompareVersions("1.2.3", "1.2.3") != 0 {
		t.Error("equal versions must compare equal")
	}
}
