package deployments

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeProtector is a no-op protector for tests.
type fakeProtector struct{}

func (fakeProtector) Protect(s string) (string, error) { return "sealed:" + s, nil }
func (fakeProtector) Unseal(s string) (string, error) {
	return strings.TrimPrefix(s, "sealed:"), nil
}

func openStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "deployments", "index.json")
	store, err := OpenStore(path, fakeProtector{})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestSaveGetDelete(t *testing.T) {
	s := openStore(t)
	created := time.Now()
	time.Sleep(time.Millisecond)
	d := Deployment{ID: "d1", Name: "myapp", CreatedAt: created}
	if err := s.Save(d); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get("d1")
	if !ok || got.Name != "myapp" {
		t.Fatalf("Get = %+v %v", got, ok)
	}
	if !got.UpdatedAt.After(got.CreatedAt) {
		t.Errorf("UpdatedAt %v not after CreatedAt %v", got.UpdatedAt, got.CreatedAt)
	}
	if err := s.Delete("d1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("d1"); ok {
		t.Fatal("delete failed")
	}
}

func TestRunLifecycle(t *testing.T) {
	s := openStore(t)
	s.Save(Deployment{ID: "d1", Name: "myapp", CreatedAt: time.Now()})

	if err := s.StartRun("d1", "r1", KindDeploy); err != nil {
		t.Fatal(err)
	}
	d, _ := s.Get("d1")
	if len(d.Runs) != 1 || d.Runs[0].Status != "running" {
		t.Fatalf("after start: %+v", d.Runs)
	}
	if err := s.FinishRun("d1", "r1", "success", ""); err != nil {
		t.Fatal(err)
	}
	d, _ = s.Get("d1")
	if d.Runs[0].Status != "success" {
		t.Fatalf("after finish: %+v", d.Runs[0])
	}

	// Interrupted recovery: start a new run, then start another — the old
	// "running" one should be flipped to "interrupted".
	s.StartRun("d1", "r2", KindApply)
	s.StartRun("d1", "r3", KindDeploy)
	d, _ = s.Get("d1")
	if d.Runs[1].Status != "interrupted" {
		t.Fatalf("r2 not interrupted: %+v", d.Runs[1])
	}
}

func TestRunLogSealedRoundTrip(t *testing.T) {
	s := openStore(t)
	s.Save(Deployment{ID: "d1", Name: "myapp", CreatedAt: time.Now()})

	output := "step 1: ok\nstep 2: done"
	if err := s.SaveRunLog("d1", "r1", output); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadRunLog("r1")
	if err != nil {
		t.Fatal(err)
	}
	if got != output {
		t.Fatalf("round-trip = %q, want %q", got, output)
	}
}

func TestMaxRuns(t *testing.T) {
	s := openStore(t)
	s.Save(Deployment{ID: "d1", Name: "myapp", CreatedAt: time.Now()})
	for i := 0; i < MaxRuns+10; i++ {
		runID := GenerateID()
		s.StartRun("d1", runID, KindDeploy)
		s.FinishRun("d1", runID, "success", "")
	}
	d, _ := s.Get("d1")
	if len(d.Runs) > MaxRuns {
		t.Fatalf("runs = %d, want cap %d", len(d.Runs), MaxRuns)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deployments", "index.json")
	s1, _ := OpenStore(path, fakeProtector{})
	s1.Save(Deployment{ID: "d1", Name: "persisted", CreatedAt: time.Now()})
	s1.StartRun("d1", "r1", KindDeploy)
	s1.FinishRun("d1", "r1", "success", "")

	s2, err := OpenStore(path, fakeProtector{})
	if err != nil {
		t.Fatal(err)
	}
	d, ok := s2.Get("d1")
	if !ok || d.Name != "persisted" {
		t.Fatalf("reopened = %+v %v", d, ok)
	}
	if len(d.Runs) != 1 || d.Runs[0].Status != "success" {
		t.Fatalf("runs after reopen: %+v", d.Runs)
	}
}

func seedDeployment(t *testing.T, s *Store, name, host, pat, dbPass string) {
	t.Helper()
	doc := fmt.Sprintf(`{"hostName":%q,"presetId":"","config":{"gitPat":%q,"database":{"password":%q,"engine":"mysql","name":"db"}}}`,
		host, pat, dbPass)
	sealed, _ := fakeProtector{}.Protect(doc)
	if err := s.Save(Deployment{ID: GenerateID(), Name: name, Config: sealed}); err != nil {
		t.Fatal(err)
	}
}

func TestExportBundleSanitizesSecrets(t *testing.T) {
	s := openStore(t)
	seedDeployment(t, s, "shop", "prod", "ghp_topsecret123", "hunter2hunter2")

	bundle, err := s.ExportBundle(nil)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Version != BundleVersion || len(bundle.Deployments) != 1 {
		t.Fatalf("bundle = %+v", bundle)
	}
	raw, _ := json.Marshal(bundle.Deployments[0].Config)
	if strings.Contains(string(raw), "ghp_topsecret123") || strings.Contains(string(raw), "hunter2hunter2") {
		t.Fatalf("bundle leaked secrets: %s", raw)
	}
	if !strings.Contains(string(raw), `"engine":"mysql"`) {
		t.Fatalf("non-secret config dropped: %s", raw)
	}
	// The stored record keeps its secrets — only the export is clean.
	rec, _ := s.Get(s.List()[0].ID)
	if !strings.Contains(rec.Config, "ghp_topsecret123") {
		t.Fatal("export mutated the stored record")
	}

	// Selective export by name.
	partial, err := s.ExportBundle([]string{"shop"})
	if err != nil || len(partial.Deployments) != 1 {
		t.Fatalf("selective export = %+v err=%v", partial, err)
	}
	partial, err = s.ExportBundle([]string{"missing"})
	if err != nil || len(partial.Deployments) != 0 {
		t.Fatalf("export of unknown id = %+v err=%v", partial, err)
	}
}

func TestImportBundleIdempotent(t *testing.T) {
	s := openStore(t)
	entries := []BundleDeployment{{
		Name: "blog", HostName: "prod",
		Config: json.RawMessage(`{"projectName":"blog","domain":"blog.io"}`),
	}}

	added, skipped, err := s.ImportDeployments(entries)
	if err != nil || added != 1 || skipped != 0 {
		t.Fatalf("first import = %d/%d err=%v", added, skipped, err)
	}
	// Re-import: everything skipped, nothing duplicated.
	added, skipped, err = s.ImportDeployments(entries)
	if err != nil || added != 0 || skipped != 1 {
		t.Fatalf("second import = %d/%d err=%v", added, skipped, err)
	}
	if len(s.List()) != 1 {
		t.Fatalf("duplicate records after re-import: %d", len(s.List()))
	}

	// Imported config unseals back to the original document.
	view, ok := s.Get(s.List()[0].ID)
	if !ok {
		t.Fatal("imported record missing")
	}
	plain, _ := fakeProtector{}.Unseal(view.Config)
	if !strings.Contains(plain, `"projectName":"blog"`) || !strings.Contains(plain, `"hostName":"prod"`) {
		t.Fatalf("imported doc = %s", plain)
	}

	// Nameless entries are rejected outright.
	if _, _, err := s.ImportDeployments([]BundleDeployment{{Name: "", HostName: "x"}}); err == nil {
		t.Fatal("nameless entry accepted")
	}
}

func TestSanitizeConfigKeepsStructure(t *testing.T) {
	in := json.RawMessage(`{"gitPat":"p","gitUrl":"u","database":{"password":"p","engine":"mysql"},"domain":"d.io"}`)
	out, err := SanitizeConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{`"gitUrl":"u"`, `"engine":"mysql"`, `"domain":"d.io"`} {
		if !strings.Contains(s, want) {
			t.Errorf("sanitized config lost %s: %s", want, s)
		}
	}
	for _, leak := range []string{`"gitPat":"p"`, `"password":"p"`} {
		if strings.Contains(s, leak) {
			t.Errorf("sanitized config leaked %s", leak)
		}
	}
	// Invalid input errors instead of silently passing through.
	if _, err := SanitizeConfig(json.RawMessage(`{nope`)); err == nil {
		t.Error("invalid JSON accepted")
	}
}
