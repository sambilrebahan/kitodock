package atomicfile

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteCreatesNewFileWithMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "doc.json")

	if err := Write(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("content = %q, want %q", got, "hello")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		return // POSIX mode bits are advisory there; content is the contract
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestWriteReplacesExistingContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doc")
	if err := Write(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Write(path, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second" {
		t.Fatalf("content = %q after the second write, want %q", got, "second")
	}
}

func TestWriteLeavesNoTemporaryFilesBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc")
	for _, content := range []string{"one", "two", "three"} {
		if err := Write(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "doc" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("directory holds %v after writes, want just [doc]", names)
	}
}

// The permission-preserving variant must not let a save widen access: a
// distinctive but user-reachable ACL (owner full, Everyone read-only,
// inheritance off) has to round-trip across the replacement unchanged.
func TestWriteKeepingPermissionsDoesNotWidenAccess(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("DACL preservation needs Windows")
	}
	user := os.Getenv("USERNAME")
	if user == "" {
		t.Skip("no USERNAME in the environment to grant")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "protected.json")
	if err := Write(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Grant by well-known SID (S-1-1-0 = Everyone) so a localized system
	// account name cannot make the setup flaky.
	args := []string{path, "/inheritance:r", "/grant:r", user + ":F", "/grant:r", "*S-1-1-0:R"}
	if out, err := exec.Command("icacls", args...).CombinedOutput(); err != nil {
		t.Skipf("could not set a distinctive ACL here: %v: %s", err, out)
	}

	before := aclLines(t, path)

	if err := WriteKeepingPermissions(path, []byte(`{"v":2}`), 0o644); err != nil {
		t.Fatal(err)
	}

	after := aclLines(t, path)
	if strings.Join(after, "|") != strings.Join(before, "|") {
		t.Fatalf("ACL changed across the save.\nbefore:\n  %s\nafter:\n  %s",
			strings.Join(before, "\n  "), strings.Join(after, "\n  "))
	}
	if !hasEveryoneReadonly(before) {
		t.Fatalf("test setup failed to produce a distinctive ACL: %v", before)
	}
}

// aclLines asks icacls for the access list, normalizing away the echoed
// path. icacls prints the first ACE on the same line as the path and the
// rest below it, so the path is stripped as a prefix rather than dropped
// with its whole line.
func aclLines(t *testing.T, path string) []string {
	t.Helper()
	out, err := exec.Command("icacls", path).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.ToLower(strings.TrimSpace(line))
		if line == "" || strings.Contains(line, "successfully processed") || strings.Contains(line, "failed processing") {
			continue
		}
		line = strings.TrimPrefix(line, strings.ToLower(path))
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func hasEveryoneReadonly(lines []string) bool {
	for _, line := range lines {
		// icacls renders S-1-1-0 as "Everyone" on English systems and as the
		// raw SID elsewhere; accept either rendering.
		isEveryone := strings.HasPrefix(line, "everyone:") || strings.HasPrefix(line, "s-1-1-0:")
		if isEveryone && strings.Contains(line, "(r)") && !strings.Contains(line, "(f)") {
			return true
		}
	}
	return false
}

func TestWriteKeepingPermissionsFallsBackToWriteForNewFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh")

	if err := WriteKeepingPermissions(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "x" {
		t.Fatalf("content = %q, want %q", got, "x")
	}
}

func TestWriteKeepingPermissionsRefusesNonRegularFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory swap semantics differ on Windows; the useful case is POSIX")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "adirectory")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteKeepingPermissions(target, []byte("x"), 0o600); err == nil {
		t.Fatal("replacing a directory must be refused, not attempted")
	}
}
