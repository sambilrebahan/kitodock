package projectroutes

import (
	"os"
	"path/filepath"
	"testing"
)

func openedStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "routes.json")
	store, notice, err := OpenStore(path)
	if err != nil || notice != nil {
		t.Fatalf("OpenStore: %v %+v", err, notice)
	}
	return store, path
}

func demoProject() Project {
	return Project{Name: "shop", MainPath: `D:\dev\www\shop`, TLD: "test", SSL: true}
}

func TestCanonicalRouteMaterializes(t *testing.T) {
	store, _ := openedStore(t)
	routes := store.List(demoProject())
	if len(routes) != 1 || !routes[0].Canonical || routes[0].Hostname != "shop.test" {
		t.Fatalf("List() = %+v, want the materialized canonical", routes)
	}
	if routes[0].ServeMode != "docroot" || routes[0].TargetKind != "main-checkout" {
		t.Fatalf("bad defaults: %+v", routes[0])
	}
}

func TestPreviewLifecycle(t *testing.T) {
	store, _ := openedStore(t)
	project := demoProject()

	route, err := store.CreatePreview(project, "feat-x", `D:\dev\worktrees\shop-feat-x`, 5173)
	if err != nil {
		t.Fatal(err)
	}
	if route.Hostname != "feat-x.shop.test" || route.ServeMode != "proxy" || route.ProxyPort != 5173 {
		t.Fatalf("preview = %+v", route)
	}

	// Duplicate hostname is refused.
	if _, err := store.CreatePreview(project, "feat-x", `D:\other`, 0); err == nil {
		t.Fatal("duplicate preview hostname accepted")
	}

	// Reserved labels are refused.
	for _, label := range []string{"www", "localhost", "main", "-bad", "a" + string(make([]byte, 70))} {
		if _, err := store.CreatePreview(project, label, `D:\x`, 0); err == nil {
			t.Errorf("reserved/invalid label %q accepted", label)
		}
	}

	// Canonical can be redirected to the worktree.
	if err := store.SetCanonical(project, route.TargetPath); err != nil {
		t.Fatal(err)
	}
	routes := store.List(project)
	for _, r := range routes {
		if r.Canonical {
			if r.TargetKind != "worktree" || r.TargetPath != route.TargetPath {
				t.Fatalf("canonical not redirected: %+v", r)
			}
		}
	}

	// And restored.
	if err := store.RestoreCanonical(project); err != nil {
		t.Fatal(err)
	}
	for _, r := range store.List(project) {
		if r.Canonical && r.TargetKind != "main-checkout" {
			t.Fatalf("canonical not restored: %+v", r)
		}
	}

	// Non-canonical removal works; canonical does not.
	if err := store.Remove(project.Name, route.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(project.Name, "canonical:shop"); err == nil {
		t.Fatal("canonical removal accepted")
	}
}

func TestTldMigrationRewritesHostnames(t *testing.T) {
	store, _ := openedStore(t)
	project := demoProject()
	store.CreatePreview(project, "feat", `D:\wt`, 0)

	if err := store.MigrateTLD("test", "local"); err != nil {
		t.Fatal(err)
	}
	routes := store.List(Project{Name: "shop", MainPath: project.MainPath, TLD: "local"})
	for _, r := range routes {
		wantSuffix := ".local"
		if !hasSuffix(r.Hostname, wantSuffix) {
			t.Errorf("hostname %q not migrated", r.Hostname)
		}
	}
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

func TestCorruptStorePreservedAndRestarted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.json")
	os.WriteFile(path, []byte("{broken"), 0o644)

	store, notice, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if notice == nil {
		t.Fatal("corrupt store should produce a notice")
	}
	if _, statErr := os.Stat(notice.PreservedPath); statErr != nil {
		t.Fatal("preserved copy missing")
	}
	// Fresh store works.
	if routes := store.List(demoProject()); len(routes) != 1 {
		t.Fatal("store not restarted fresh")
	}
}

func TestPersistenceRoundTrips(t *testing.T) {
	store, path := openedStore(t)
	project := demoProject()
	if _, err := store.CreatePreview(project, "feat", `D:\wt`, 5173); err != nil {
		t.Fatal(err)
	}

	reopened, notice, err := OpenStore(path)
	if err != nil || notice != nil {
		t.Fatalf("reopen: %v %+v", err, notice)
	}
	routes := reopened.List(project)
	if len(routes) != 2 {
		t.Fatalf("routes lost after reopen: %+v", routes)
	}
}
