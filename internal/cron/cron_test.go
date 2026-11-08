package cron

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func newTestManager(t *testing.T) (*Manager, *[]Job) {
	t.Helper()
	var persisted []Job
	m := NewManager(Config{
		Persist: func(jobs []Job) error {
			persisted = jobs
			return nil
		},
	})
	return m, &persisted
}

func TestSaveValidatesSchedule(t *testing.T) {
	m, _ := newTestManager(t)
	if err := m.Save(Job{Name: "x", Schedule: "not-a-cron", Command: "echo hi"}); err == nil {
		t.Fatal("invalid schedule accepted")
	}
	if err := m.Save(Job{Name: "x", Schedule: "* * * * *", Command: "echo hi"}); err != nil {
		t.Fatal(err)
	}
}

func TestToggleEnableDisable(t *testing.T) {
	m, persisted := newTestManager(t)
	job := Job{Name: "tick", Schedule: "* * * * *", Command: "echo hi", Enabled: true}
	m.Save(job)
	jobID := m.List()[0].ID

	if err := m.Toggle(jobID, false); err != nil {
		t.Fatal(err)
	}
	if (*persisted)[0].Enabled {
		t.Fatal("disable not persisted")
	}
	if err := m.Toggle(jobID, true); err != nil {
		t.Fatal(err)
	}
	if !(*persisted)[0].Enabled {
		t.Fatal("re-enable not persisted")
	}
}

func TestDeleteRemoves(t *testing.T) {
	m, persisted := newTestManager(t)
	m.Save(Job{Name: "x", Schedule: "* * * * *", Command: "echo hi"})
	id := m.List()[0].ID
	if err := m.Delete(id); err != nil {
		t.Fatal(err)
	}
	if len(*persisted) != 0 {
		t.Fatal("delete not persisted")
	}
	if len(m.List()) != 0 {
		t.Fatal("job still in manager")
	}
}

func TestRunNowExecutes(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("cmd /c needs Windows")
	}
	m, _ := newTestManager(t)
	var events []Progress
	m.cfg.OnProgress = func(p Progress) { events = append(events, p) }

	m.Save(Job{Name: "now", Schedule: "* * * * *", Command: "echo KITODOCK-CRON-OK", Enabled: true})
	id := m.List()[0].ID
	if err := m.RunNow(id); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		done := false
		for _, e := range events {
			if e.Phase == "done" || e.Phase == "error" {
				done = true
			}
		}
		if done {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	sawDone := false
	for _, e := range events {
		if e.Phase == "done" && contains(e.Output, "KITODOCK-CRON-OK") {
			sawDone = true
		}
	}
	if !sawDone {
		t.Fatalf("expected done with marker, events: %+v", events)
	}
	// LastOutput should be recorded.
	if !contains(m.List()[0].LastOutput, "KITODOCK-CRON-OK") {
		t.Fatalf("LastOutput not recorded: %q", m.List()[0].LastOutput)
	}
}

func TestIsFiringMinute(t *testing.T) {
	// "* * * * *" fires every minute.
	if !isFiringMinute("* * * * *", time.Now()) {
		t.Error("* * * * * should always fire")
	}
	// "0 0 1 1 *" fires only Jan 1 midnight.
	newYear := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if !isFiringMinute("0 0 1 1 *", newYear) {
		t.Error("Jan 1 midnight should fire")
	}
	midYear := time.Date(2027, 6, 15, 12, 30, 0, 0, time.UTC)
	if isFiringMinute("0 0 1 1 *", midYear) {
		t.Error("June 15 should not fire for a Jan 1 schedule")
	}
}

func TestRunOnceFromConfig(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("cmd /c needs Windows")
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	job := Job{ID: "test-job", Name: "t", Schedule: "* * * * *",
		Command: "echo KITODOCK-RUNONCE", Enabled: true}
	doc := map[string]any{"crons": []Job{job}}
	data, _ := json.Marshal(doc)
	os.WriteFile(configPath, data, 0o644)

	if err := RunOnceFromConfig(configPath, "test-job"); err != nil {
		t.Fatal(err)
	}
	// A disabled job should be a no-op.
	job.Enabled = false
	doc["crons"] = []Job{job}
	data, _ = json.Marshal(doc)
	os.WriteFile(configPath, data, 0o644)
	if err := RunOnceFromConfig(configPath, "test-job"); err == nil {
		t.Fatal("disabled job should error")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr))
}

func containsAt(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
