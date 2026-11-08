package vcruntime

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// probeBinary builds the fixture once per test run. Building by file list
// (not package path) keeps it working inside testdata/, which the module
// graph ignores by design.
func probeBinary(t *testing.T) string {
	t.Helper()
	if os.Getenv("CI") == "true" || os.Getenv("GITHUB_ACTIONS") == "true" {
		t.Skip("process spawning is flaky on CI runners")
	}
	exe := filepath.Join(t.TempDir(), "probe.exe")
	build := exec.Command("go", "build", "-o", exe, "testdata/probe/main.go")
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("cannot build the probe fixture here: %v: %s", err, out)
	}
	return exe
}

func TestCheckPassesForAHealthyBinary(t *testing.T) {
	exe := probeBinary(t)
	t.Setenv("KITODOCK_PROBE_MODE", "ok")

	if err := Check(exe, VS17URL); err != nil {
		t.Fatalf("Check() = %v, want nil", err)
	}
}

func TestCheckDetectsMissingRuntime(t *testing.T) {
	exe := probeBinary(t)
	t.Setenv("KITODOCK_PROBE_MODE", "dll-missing")

	err := Check(exe, VS18URL)
	missing := &Missing{}
	if !errors.As(err, &missing) {
		t.Fatalf("Check() = %v, want a *Missing", err)
	}
	if missing.Stale {
		t.Error("an absent runtime is not the stale case")
	}
	if missing.URL != VS18URL {
		t.Errorf("URL = %q, want the one the caller pinned", missing.URL)
	}
}

func TestCheckDetectsStaleRuntime(t *testing.T) {
	exe := probeBinary(t)
	t.Setenv("KITODOCK_PROBE_MODE", "stale")

	err := Check(exe, VS17URL)
	missing := &Missing{}
	if !errors.As(err, &missing) || !missing.Stale {
		t.Fatalf("Check() = %v, want a stale *Missing", err)
	}
}

func TestUnrelatedCrashIsNotARedistProblem(t *testing.T) {
	exe := probeBinary(t)
	t.Setenv("KITODOCK_PROBE_MODE", "crash")

	if err := Check(exe, VS17URL); err == nil {
		t.Fatal("a crash should surface as an error")
	}
	missing := &Missing{}
	if errors.As(Check(exe, VS17URL), &missing) {
		t.Fatal("a silent exit-3 crash is not a redistributable verdict")
	}
}

func TestMissingExecutableIsNothingToCheck(t *testing.T) {
	if err := Check(filepath.Join(t.TempDir(), "ghost.exe"), VS17URL); err != nil {
		t.Fatalf("Check() on a missing binary = %v, want nil (not installed yet)", err)
	}
}

func TestMessageNamesTheProduct(t *testing.T) {
	// The UI matches on the exact product name to offer its download
	// button; keep the wording anchored to it. The path itself is built
	// from the resolver so this test never hardcodes the install root.
	binary := filepath.Join("D:", "dev", "php84", "php.exe")
	err := &Missing{Binary: binary, URL: VS17URL}
	message := err.Error()
	if !strings.Contains(message, "Visual C++ Redistributable") {
		t.Fatalf("Error() = %q, want the product name in it", message)
	}
	if !strings.HasPrefix(message, binary) {
		t.Fatalf("Error() = %q, want it to lead with the binary path", message)
	}
	if !strings.Contains(message, "not installed") {
		t.Fatalf("Error() = %q, want the missing-runtime phrasing", message)
	}
}
