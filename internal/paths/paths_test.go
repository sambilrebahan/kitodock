package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveEnvOverrideBeatsEverything(t *testing.T) {
	dir := t.TempDir()

	got, isPortable := resolve(dir, `D:\custom-root`)

	if got != `D:\custom-root` {
		t.Fatalf("resolve() root = %q, want the env override", got)
	}
	if isPortable {
		t.Error("an env override is a support escape hatch, not a portable install")
	}
}

func TestResolvePortableMarkerWinsOverFixedRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, PortableMarker), nil, 0644); err != nil {
		t.Fatal(err)
	}

	got, isPortable := resolve(dir, "")

	if got != dir {
		t.Fatalf("resolve() root = %q, want the executable folder %q", got, dir)
	}
	if !isPortable {
		t.Error("marker present must report a portable install")
	}
}

func TestResolveFallsBackToFixedRoot(t *testing.T) {
	got, isPortable := resolve(t.TempDir(), "")

	if got != fixedRoot {
		t.Fatalf("resolve() root = %q, want the fixed root %q", got, fixedRoot)
	}
	if isPortable {
		t.Error("no marker means not portable")
	}
}

func TestResolveWithEmptyExecutableDir(t *testing.T) {
	got, _ := resolve("", "")

	if got != fixedRoot {
		t.Fatalf("resolve() root = %q, want %q even without an exe dir", got, fixedRoot)
	}
}

func TestDerivedLocationsLiveUnderRoot(t *testing.T) {
	previous := root
	root = filepath.Join(t.TempDir(), "env")
	t.Cleanup(func() { root = previous })

	cases := []struct {
		name string
		got  string
		want string
	}{
		{"config", ConfigFile(), filepath.Join(root, "config.json")},
		{"state", StateDir(), filepath.Join(root, "state")},
		{"projects", ProjectsDir(), filepath.Join(root, "www")},
		{"logs", LogsDir(), filepath.Join(root, "logs")},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// One hardcoded spelling of the install root inside this package is the
// design; a second one anywhere else would silently pin that code to a
// non-portable location. This walks the module's Go sources (outside this
// package) and fails on any string literal containing the root path.
func TestFixedRootAppearsOnlyInThisPackage(t *testing.T) {
	moduleRoot := filepath.Join("..", "..")
	var offenders []string

	err := filepath.Walk(moduleRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case "node_modules", "frontend", "build", "bin", ".git":
				return filepath.SkipDir
			}
			if sameDir(path, filepath.Join(moduleRoot, "internal", "paths")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue // comments may explain the root, only code may pin it
			}
			if strings.Contains(strings.ToLower(trimmed), `c:\kitodock`) {
				offenders = append(offenders, path+": "+trimmed)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("hardcoded install root outside the paths package:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

func sameDir(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && absA == absB
}
