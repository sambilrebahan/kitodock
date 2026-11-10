package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"kitodock/internal/paths"
)

func openAt(t *testing.T, path string) *Store {
	t.Helper()
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	return store
}

func freshStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	return openAt(t, path), path
}

func withFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFreshEnvironmentGetsDefaultsAndPersistsThem(t *testing.T) {
	store, path := freshStore(t)

	snapshot := store.Get()
	if snapshot.Projects.TLD != "test" {
		t.Errorf("TLD = %q, want the default", snapshot.Projects.TLD)
	}
	if snapshot.Projects.Root != paths.ProjectsDir() {
		t.Errorf("Projects.Root = %q, want %q", snapshot.Projects.Root, paths.ProjectsDir())
	}
	if !paths.IsSameRoot(snapshot.InstallRoot, paths.Root()) {
		t.Errorf("InstallRoot = %q, want the current root", snapshot.InstallRoot)
	}
	for _, service := range []string{"nginx", "mysql", "mailpit"} {
		if !snapshot.Stack.Enabled[service] {
			t.Errorf("default stack should show %s", service)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a fresh environment must persist its config: %v", err)
	}
}

func TestBackfillUpgradesOldDocumentsWithoutDiscardingChoices(t *testing.T) {
	path := withFile(t, `{"stack":{"enabled":{"redis":true}},"projects":{"root":"D:\\code"}}`)
	store := openAt(t, path)

	snapshot := store.Get()
	if snapshot.Projects.TLD != "test" {
		t.Errorf("missing TLD not backfilled: %q", snapshot.Projects.TLD)
	}
	if len(snapshot.Stack.Enabled) != 1 || !snapshot.Stack.Enabled["redis"] {
		t.Errorf("an explicit enabled map is the user's choice; got %v", snapshot.Stack.Enabled)
	}
	if snapshot.Projects.Root != `D:\code` {
		t.Errorf("Projects.Root = %q, want it untouched", snapshot.Projects.Root)
	}
}

func TestCorruptDocumentFailsTheOpen(t *testing.T) {
	path := withFile(t, "{oops")

	if _, err := Open(path); err == nil {
		t.Fatal("a corrupt config must fail loudly, not reset silently")
	}
}

func TestLanguageRoundTripsThroughDisk(t *testing.T) {
	store, path := freshStore(t)

	if err := store.SetLanguage("id"); err != nil {
		t.Fatal(err)
	}
	if store.Language() != "id" {
		t.Fatalf("Language() = %q right after the set", store.Language())
	}
	if got := openAt(t, path).Language(); got != "id" {
		t.Fatalf("language did not survive a reopen: %q", got)
	}
}

func TestServiceVisibilityToggles(t *testing.T) {
	store, path := freshStore(t)

	for _, step := range []struct {
		name    string
		visible bool
	}{
		{"postgres", true},
		{"nginx", false},
	} {
		if err := store.SetServiceEnabled(step.name, step.visible); err != nil {
			t.Fatal(err)
		}
	}
	enabled := openAt(t, path).Get().Stack.Enabled
	if !enabled["postgres"] || enabled["nginx"] {
		t.Fatalf("toggles did not persist: %v", enabled)
	}
}

func TestGetHandsBackAnIndependentCopy(t *testing.T) {
	store, _ := freshStore(t)

	first := store.Get()
	first.Stack.Enabled["nginx"] = false
	first.Projects.TLD = "leak"

	second := store.Get()
	if !second.Stack.Enabled["nginx"] || second.Projects.TLD == "leak" {
		t.Fatal("mutating a snapshot leaked into the store")
	}
}

// Relocation: an environment last used at another root adopts the current
// root, rebases paths that lived under the old one, and reports the move.
func TestRelocationIsDetectedAndFollowed(t *testing.T) {
	previous := `E:\usb\Kitodock`
	path := withFile(t, fmt.Sprintf(`{"installRoot":%q,"projects":{"root":%q}}`,
		previous, filepath.Join(previous, "www")))

	store := openAt(t, path)
	snapshot := store.Get()

	if want := filepath.Join(paths.Root(), "www"); snapshot.Projects.Root != want {
		t.Errorf("Projects.Root = %q, want it rebased to %q", snapshot.Projects.Root, want)
	}
	if store.MovedFrom() != previous {
		t.Errorf("MovedFrom() = %q, want %q", store.MovedFrom(), previous)
	}
	if !paths.IsSameRoot(snapshot.InstallRoot, paths.Root()) {
		t.Errorf("InstallRoot = %q, want the current root", snapshot.InstallRoot)
	}
	// The adoption must be on disk, or the next start rediscovers the move.
	if again := openAt(t, path); again.MovedFrom() != "" {
		t.Errorf("the move is detected again after reload: %q", again.MovedFrom())
	}
}

// A projects directory outside the environment belongs to the user, not to
// the environment, and survives a move untouched.
func TestPathsOutsideTheEnvironmentAreLeftAlone(t *testing.T) {
	path := withFile(t, fmt.Sprintf(`{"installRoot":%q,"projects":{"root":%q}}`,
		`E:\usb\Kitodock`, `D:\my-code`))

	snapshot := openAt(t, path).Get()
	if snapshot.Projects.Root != `D:\my-code` {
		t.Fatalf("Projects.Root = %q, want it untouched", snapshot.Projects.Root)
	}
}

// A document with no recorded root is a first run, not a move: adopt quietly.
func TestFirstRunAdoptsTheRootWithoutReportingAMove(t *testing.T) {
	path := withFile(t, `{"projects":{"root":"C:\\elsewhere\\www"}}`)

	store := openAt(t, path)
	if store.MovedFrom() != "" {
		t.Fatalf("first run reported a move from %q", store.MovedFrom())
	}
	if store.Get().Projects.Root != `C:\elsewhere\www` {
		t.Fatal("first run must not rewrite the projects root")
	}
}

func TestPersistedDocumentIsValidJSONWithExpectedShape(t *testing.T) {
	store, path := freshStore(t)
	if err := store.SetLanguage("en"); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("persisted config is not valid JSON: %v", err)
	}
	if doc["language"] != "en" {
		t.Errorf("language = %v, want en", doc["language"])
	}
	if _, ok := doc["stack"].(map[string]any); !ok {
		t.Errorf("stack missing or not an object: %v", doc["stack"])
	}
}

func TestGetReturnsDeepCopies(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectSSL("shop", true); err != nil {
		t.Fatal(err)
	}
	view := s.Get()
	view.Projects.SSL["shop"] = false
	if s.Get().Projects.SSL["shop"] != true {
		t.Fatal("caller mutation leaked into the store")
	}
}
