package php

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// newTuningManager builds a Manager over a fake 8.3 tree with a php.ini.
func newTuningManager(t *testing.T) (*Manager, string) {
	t.Helper()
	return newFakeManager(t)
}

func TestPhpTuningRoundTrip(t *testing.T) {
	mgr, _ := newTuningManager(t)
	iniPath := filepath.Join(filepath.Dir(filepath.Dir(mgr.DirFor("8.3"))), "") // unused; direct checks below

	values, err := mgr.Tuning("8.3")
	if err != nil {
		t.Fatal(err)
	}
	if values["memory_limit"] != "" {
		t.Fatalf("fresh ini should report unset memory_limit, got %q", values["memory_limit"])
	}
	_ = iniPath

	// Write one, read back, then update in place.
	if err := mgr.SetTuning("8.3", "memory_limit", "512M"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.SetTuning("8.3", "upload_max_filesize", "64M"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.SetTuning("8.3", "max_execution_time", "120"); err != nil {
		t.Fatal(err)
	}
	values, err = mgr.Tuning("8.3")
	if err != nil {
		t.Fatal(err)
	}
	if values["memory_limit"] != "512M" || values["upload_max_filesize"] != "64M" || values["max_execution_time"] != "120" {
		t.Fatalf("round trip = %+v", values)
	}
	if err := mgr.SetTuning("8.3", "memory_limit", "1G"); err != nil {
		t.Fatal(err)
	}
	values, _ = mgr.Tuning("8.3")
	if values["memory_limit"] != "1G" {
		t.Fatalf("update = %q", values["memory_limit"])
	}

	// Reject unknown keys and malformed values.
	if err := mgr.SetTuning("8.3", "disable_functions", "all"); err == nil {
		t.Fatal("unknown directive accepted")
	}
	if err := mgr.SetTuning("8.3", "memory_limit", "lots"); err == nil {
		t.Fatal("bad size accepted")
	}
	if err := mgr.SetTuning("8.3", "opcache.enable", "yes"); err == nil {
		t.Fatal("bad flag accepted")
	}
	// A commented directive counts as a slot and gets activated.
	if err := mgr.SetTuning("8.3", "opcache.enable", "1"); err != nil {
		t.Fatal(err)
	}
	if values, _ = mgr.Tuning("8.3"); values["opcache.enable"] != "1" {
		t.Fatalf("opcache = %+v", values)
	}
}

// fakePeclServer serves a releases tree with one good version and one
// that lacks our build flavor, plus zip payloads containing the DLL.
func fakePeclServer(t *testing.T, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/redis/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<a href="6.1.0/">6.1.0/</a> <a href="6.0.2/">6.0.2/</a>`))
	})
	mux.HandleFunc("/redis/6.1.0/", func(w http.ResponseWriter, r *http.Request) {
		// Newest release ships only ts builds for 8.3.
		if strings.Contains(r.URL.Path, "-ts-x64.zip") {
			serveZip(t, w, "php_redis.dll")
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/redis/6.0.2/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "php_redis-6.0.2-8.3-nts-x64.zip") {
			hits.Add(1)
			serveZip(t, w, "php_redis.dll")
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/imagick/", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`<a href="3.7.0/">3.7.0/</a>`))
	})
	mux.HandleFunc("/imagick/3.7.0/", func(w http.ResponseWriter, _ *http.Request) {
		// A zip without the DLL we need.
		serveZip(t, w, "README.txt")
	})
	return httptest.NewServer(mux)
}

func serveZip(t *testing.T, w http.ResponseWriter, name string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fw, _ := zw.Create(name)
	fw.Write([]byte("MZ fake dll for " + name))
	zw.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Write(buf.Bytes())
}

func TestPeclInstallPicksMatchingFlavor(t *testing.T) {
	var hits atomic.Int64
	server := fakePeclServer(t, &hits)
	t.Cleanup(server.Close)

	mgr, dir := newTuningManager(t)
	inst := &PeclInstaller{
		Manager:    mgr,
		BaseURL:    server.URL,
		HTTP:       func(_ context.Context, url string) ([]byte, error) { return httpGetTest(server, url) },
		ProbeBuild: func(string) (bool, string) { return false, "x64" }, // NTS x64
	}
	var steps []string
	result, err := inst.Install(context.Background(), "8.3", "redis", func(s string) { steps = append(steps, s) })
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != "6.0.2" {
		t.Fatalf("picked %s, want 6.0.2 (6.1.0 lacks nts)", result.Version)
	}
	if result.File != "php_redis-6.0.2-8.3-nts-x64.zip" {
		t.Fatalf("file = %s", result.File)
	}
	// The DLL landed in ext\ and the ini entry is on.
	dll := filepath.Join(dir, "ext", "php_redis.dll")
	if data, err := os.ReadFile(dll); err != nil || !strings.Contains(string(data), "fake dll") {
		t.Fatalf("dll = %v err=%v", data, err)
	}
	exts, _ := mgr.Extensions("8.3")
	for _, e := range exts {
		if e.Name == "redis" && !e.Enabled {
			t.Fatal("redis not enabled in ini")
		}
	}
	if len(steps) < 2 {
		t.Fatalf("progress steps = %v", steps)
	}
}

func TestPeclInstallFailures(t *testing.T) {
	var hits atomic.Int64
	server := fakePeclServer(t, &hits)
	t.Cleanup(server.Close)

	mgr, _ := newTuningManager(t)
	inst := &PeclInstaller{
		Manager:    mgr,
		BaseURL:    server.URL,
		HTTP:       func(_ context.Context, url string) ([]byte, error) { return httpGetTest(server, url) },
		ProbeBuild: func(string) (bool, string) { return true, "x86" }, // arch nobody ships
	}
	if _, err := inst.Install(context.Background(), "8.3", "redis", nil); err == nil {
		t.Fatal("missing flavor accepted")
	}
	// Zip without the DLL.
	inst.ProbeBuild = func(string) (bool, string) { return false, "x64" }
	if _, err := inst.Install(context.Background(), "8.3", "imagick", nil); err == nil {
		t.Fatal("zip without dll accepted")
	}
	// Unknown series.
	if _, err := inst.Install(context.Background(), "9.9", "redis", nil); err == nil {
		t.Fatal("unknown series accepted")
	}
}

func TestVersionLessNewestFirst(t *testing.T) {
	got := []string{"6.0.2", "6.1.0", "6.0.10"}
	// 6.0.10 vs 6.0.2 must compare numerically.
	if !versionLess("6.0.10", "6.0.2") {
		t.Fatal("6.0.10 should sort before 6.0.2")
	}
	if versionLess("6.0.2", "6.1.0") {
		t.Fatal("6.1.0 should sort before 6.0.2")
	}
	_ = got
}

func httpGetTest(server *httptest.Server, url string) ([]byte, error) {
	resp, err := server.Client().Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d for %s", resp.StatusCode, url)
	}
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	return buf.Bytes(), nil
}
