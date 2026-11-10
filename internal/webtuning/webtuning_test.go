package webtuning

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureManagedConfReplacesStock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nginx.conf")

	// Stock conf from the nginx.org zip: no sites include, no marker.
	stock := "# comment\nworker_processes  1;\nevents {\n    worker_connections  1024;\n}\nhttp {\n    include       mime.types;\n}\n"
	if err := os.WriteFile(path, []byte(stock), 0o644); err != nil {
		t.Fatal(err)
	}
	if IsManaged(path) {
		t.Fatal("stock conf flagged as managed")
	}
	if err := EnsureManagedConf(path); err != nil {
		t.Fatal(err)
	}
	conf := read(t, path)
	for _, want := range []string{managedMark, "include sites/*.conf;", "include mime.types;", "http {"} {
		if !strings.Contains(conf, want) {
			t.Fatalf("managed conf missing %q:\n%s", want, conf)
		}
	}
	if !IsManaged(path) {
		t.Fatal("managed conf not detected")
	}

	// A second ensure is a no-op: user edits survive.
	SetTuning(path, "client_max_body_size", "32m")
	before := read(t, path)
	if err := EnsureManagedConf(path); err != nil {
		t.Fatal(err)
	}
	if read(t, path) != before {
		t.Fatal("re-ensure clobbered a managed conf")
	}

	// Missing conf is created from scratch.
	fresh := filepath.Join(dir, "fresh.conf")
	if err := EnsureManagedConf(fresh); err != nil {
		t.Fatal(err)
	}
	if !IsManaged(fresh) {
		t.Fatal("fresh conf not managed")
	}
}

func TestTuningRoundTripPerContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nginx.conf")
	if err := EnsureManagedConf(path); err != nil {
		t.Fatal(err)
	}

	// main-level directive.
	if err := SetTuning(path, "worker_processes", "4"); err != nil {
		t.Fatal(err)
	}
	// events-level.
	if err := SetTuning(path, "worker_connections", "2048"); err != nil {
		t.Fatal(err)
	}
	// http-level.
	if err := SetTuning(path, "client_max_body_size", "64m"); err != nil {
		t.Fatal(err)
	}
	if err := SetTuning(path, "keepalive_timeout", "75s"); err != nil {
		t.Fatal(err)
	}

	values, err := Tuning(path)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"worker_processes":     "4",
		"worker_connections":   "2048",
		"client_max_body_size": "64m",
		"keepalive_timeout":    "75s",
	} {
		if values[key] != want {
			t.Errorf("%s = %q, want %q", key, values[key], want)
		}
	}

	conf := read(t, path)
	if !strings.Contains(conf, "worker_processes 4;\n") {
		t.Fatalf("worker_processes not at main level:\n%s", conf)
	}
	if !strings.Contains(conf, "worker_connections 2048;") {
		t.Fatalf("worker_connections missing:\n%s", conf)
	}

	// Updating replaces instead of duplicating.
	if err := SetTuning(path, "client_max_body_size", "128m"); err != nil {
		t.Fatal(err)
	}
	conf = read(t, path)
	if strings.Count(conf, "client_max_body_size") != 1 || !strings.Contains(conf, "client_max_body_size 128m;") {
		t.Fatalf("directive duplicated or stale:\n%s", conf)
	}
}

func TestSetTuningRejectsUnknownAndBadValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nginx.conf")
	if err := EnsureManagedConf(path); err != nil {
		t.Fatal(err)
	}
	if err := SetTuning(path, "root", "/etc"); err == nil {
		t.Fatal("unknown directive accepted")
	}
	for directive, bad := range map[string]string{
		"worker_processes":     "-1x",
		"worker_connections":   "many",
		"client_max_body_size": "big",
		"sendfile":             "maybe",
	} {
		if err := SetTuning(path, directive, bad); err == nil {
			t.Errorf("%s accepted %q", directive, bad)
		}
	}
}

func TestInsertCreatesMissingBlocks(t *testing.T) {
	// A conf with http{} but no events{} gets one; a bare conf gets http{}.
	conf := insertInBlock("worker_processes 1;\nhttp {\n}\n", "events", "worker_connections 512;", "    ")
	if !strings.Contains(conf, "events {") || !strings.Contains(conf, "worker_connections 512;") {
		t.Fatalf("events block not created:\n%s", conf)
	}
	// events{} lands before http{}.
	if strings.Index(conf, "events {") > strings.Index(conf, "http {") {
		t.Fatalf("events after http:\n%s", conf)
	}
	bare := insertInBlock("worker_processes 1;\n", "http", "sendfile on;", "    ")
	if !strings.Contains(bare, "http {") {
		t.Fatalf("http block not created:\n%s", bare)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
