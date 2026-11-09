package deployments

import (
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
