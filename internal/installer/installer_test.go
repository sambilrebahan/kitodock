package installer

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// zipFixture builds an in-memory archive: map of path → content.
func zipFixture(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, body := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

type harness struct {
	manager  *Manager
	server   *httptest.Server
	root     string
	progress []Progress
}

func newHarness(t *testing.T, archive []byte) *harness {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Write(archive)
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	h := &harness{server: server, root: root}
	h.manager = NewManager(filepath.Join(root, "cache"), func(p Progress) {
		h.progress = append(h.progress, p)
	})
	return h
}

func (h *harness) source(mutate func(*Source)) Source {
	src := Source{
		Kind: KindTool, ID: "thing", Version: "1.0",
		URL:       h.server.URL + "/thing.zip",
		InstallTo: func() string { return filepath.Join(h.root, "thing") },
	}
	if mutate != nil {
		mutate(&src)
	}
	return src
}

func TestInstallExtractsAndMarks(t *testing.T) {
	archive := zipFixture(t, map[string]string{
		"thing.exe":       "binary-ish",
		"conf/thing.conf": "listen 1",
		"README.md":       "hello",
	})
	h := newHarness(t, archive)
	src := h.source(nil)

	if h.manager.IsInstalled(src) {
		t.Fatal("nothing is installed before the install")
	}
	if err := h.manager.Install(context.Background(), src); err != nil {
		t.Fatal(err)
	}

	for _, expected := range []string{"thing.exe", filepath.Join("conf", "thing.conf"), "README.md", ".kitodock-version"} {
		if _, err := os.Stat(filepath.Join(src.InstallTo(), expected)); err != nil {
			t.Errorf("missing %s after install: %v", expected, err)
		}
	}
	if !h.manager.IsInstalled(src) {
		t.Error("the marker plus files must count as installed")
	}
}

func TestFlattenStripsTheWrapperDirectory(t *testing.T) {
	archive := zipFixture(t, map[string]string{
		"thing-1.0/thing.exe":  "x",
		"thing-1.0/conf/a.cnf": "y",
	})
	h := newHarness(t, archive)
	src := h.source(func(s *Source) { s.Flatten = true })

	if err := h.manager.Install(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(src.InstallTo(), "thing.exe")); err != nil {
		t.Fatal("flatten did not lift the files out of the wrapper directory")
	}
}

func TestZipSlipIsRefused(t *testing.T) {
	// An archive whose entry climbs out of the destination must be refused
	// wholesale, not partially extracted.
	archive := zipFixture(t, map[string]string{
		"ok.txt":        "fine",
		"../escape.txt": "evil",
	})
	h := newHarness(t, archive)
	src := h.source(nil)

	err := h.manager.Install(context.Background(), src)
	if err == nil || !strings.Contains(err.Error(), "escape") {
		t.Fatalf("Install() = %v, want an escape refusal", err)
	}
	if _, statErr := os.Stat(filepath.Join(h.root, "escape.txt")); statErr == nil {
		t.Fatal("the escaping file landed outside the destination")
	}
	if _, statErr := os.Stat(filepath.Join(src.InstallTo(), ".kitodock-version")); statErr == nil {
		t.Fatal("a refused archive must not leave an installed marker")
	}
}

func TestAbsoluteEntryNamesAreRefused(t *testing.T) {
	archive := zipFixture(t, map[string]string{
		"/windows/system32/evil.dll": "no",
	})
	h := newHarness(t, archive)
	src := h.source(nil)

	if err := h.manager.Install(context.Background(), src); err == nil {
		t.Fatal("an absolute entry name must be refused")
	}
}

func TestSecondInstallReusesTheCacheWithoutRefetching(t *testing.T) {
	archive := zipFixture(t, map[string]string{"thing.exe": "x"})
	h := newHarness(t, archive)
	src := h.source(nil)

	if err := h.manager.Install(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	// Stop serving: a second install (marker cleared, cache warm) must
	// succeed without the network.
	h.server.Close()
	os.Remove(filepath.Join(src.InstallTo(), ".kitodock-version"))
	if err := h.manager.Install(context.Background(), src); err != nil {
		t.Fatalf("cached archive not reused: %v", err)
	}
}

func TestTruncatedCacheArchiveIsRejected(t *testing.T) {
	archive := zipFixture(t, map[string]string{"thing.exe": "x"})
	h := newHarness(t, archive)
	src := h.source(nil)

	// A cache file that is not a zip must not extract.
	if err := os.MkdirAll(filepath.Join(h.root, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.manager.archivePath(src), []byte("<html>error page</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.server.Close() // and no network fallback available

	if err := h.manager.Install(context.Background(), src); err == nil {
		t.Fatal("an HTML error page in the cache must fail, not extract")
	}
}

func TestManualSourceWantsItsArchiveInPlace(t *testing.T) {
	h := newHarness(t, nil)
	src := h.source(func(s *Source) {
		s.Manual = true
		s.URL = ""
		s.PageURL = "https://example.invalid/thing"
	})

	err := h.manager.Install(context.Background(), src)
	var manual *ErrManualNeeded
	if e, ok := err.(*ErrManualNeeded); ok {
		manual = e
	} else {
		t.Fatalf("Install() = %v, want ErrManualNeeded", err)
	}
	if manual.Source.PageURL != src.PageURL {
		t.Errorf("the manual error lost the page URL")
	}

	// Drop the archive into the cache by hand, exactly as the UI instructs
	// the user to, and the install proceeds.
	archive := zipFixture(t, map[string]string{"thing.exe": "x"})
	if err := os.MkdirAll(filepath.Join(h.root, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.manager.archivePath(src), archive, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.manager.Install(context.Background(), src); err != nil {
		t.Fatalf("manual install from the placed archive: %v", err)
	}
}

func TestDirectFileGetsWrappers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("PHAR-CONTENT"))
	}))
	defer server.Close()
	root := t.TempDir()
	manager := NewManager(filepath.Join(root, "cache"), nil)
	src := Source{
		Kind: KindTool, ID: "composer", Version: "2.8.9",
		URL:       server.URL + "/composer.phar",
		Direct:    true,
		InstallTo: func() string { return filepath.Join(root, "composer") },
	}

	if err := manager.Install(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	phar, err := os.ReadFile(filepath.Join(src.InstallTo(), "composer.phar"))
	if err != nil || string(phar) != "PHAR-CONTENT" {
		t.Fatalf("direct file body = %q (%v)", phar, err)
	}
	for _, wrapper := range []string{"composer.bat", "composer"} {
		if _, err := os.Stat(filepath.Join(src.InstallTo(), wrapper)); err != nil {
			t.Errorf("missing wrapper %s: %v", wrapper, err)
		}
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	archive := zipFixture(t, map[string]string{"thing.exe": "x"})
	h := newHarness(t, archive)
	src := h.source(nil)

	if err := h.manager.Install(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	h.server.Close()
	if err := h.manager.Install(context.Background(), src); err != nil {
		t.Fatalf("installing something already installed should be free: %v", err)
	}
}

func TestProgressReportsBothPhases(t *testing.T) {
	archive := zipFixture(t, map[string]string{"thing.exe": strings.Repeat("x", 4096)})
	h := newHarness(t, archive)
	src := h.source(nil)

	if err := h.manager.Install(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	sawDownload, sawExtract, sawComplete := false, false, false
	for _, p := range h.progress {
		switch p.Phase {
		case PhaseDownload:
			sawDownload = true
		case PhaseExtract:
			sawExtract = true
			if p.Percent > 100 {
				t.Errorf("extract percent %d exceeds 100", p.Percent)
			}
		}
		if p.Bytes > 0 {
			sawComplete = true
		}
	}
	if !sawDownload || !sawExtract || !sawComplete {
		t.Fatalf("progress phases missing: download=%v extract=%v anyBytes=%v", sawDownload, sawExtract, sawComplete)
	}
}

func TestCatalogIsSound(t *testing.T) {
	ids := map[string]bool{}
	for _, src := range Catalog() {
		if src.ID == "" || src.Version == "" || src.LabelKey == "" || src.InstallTo == nil {
			t.Errorf("catalog entry %+v is incomplete", src)
		}
		if !src.Manual && src.URL == "" {
			t.Errorf("%s %s: no URL for a non-manual source", src.ID, src.Version)
		}
		if src.Manual && src.PageURL == "" {
			t.Errorf("%s %s: manual source without a page URL", src.ID, src.Version)
		}
		// Every download must be https; plain http invites tampered payloads.
		if !src.Manual && strings.HasPrefix(src.URL, "http://") {
			t.Errorf("%s %s: insecure download URL %s", src.ID, src.Version, src.URL)
		}
		key := src.ID + "/" + src.Version
		if ids[key] {
			t.Errorf("duplicate catalog entry %s", key)
		}
		ids[key] = true
	}
}

func TestFindPicksTheExactPinOrTheNewest(t *testing.T) {
	if src, ok := Find("nginx", "1.28.0"); !ok || src.ID != "nginx" {
		t.Fatalf("exact pin lookup failed: %+v", src)
	}
	if src, ok := Find("php", ""); !ok || compareVersions(src.Version, "8.3") <= 0 {
		t.Fatalf("empty version should pick the newest php pin, got %+v", src)
	}
	if _, ok := Find("ghost", ""); ok {
		t.Fatal("unknown ids must not resolve")
	}
}

func TestVersionOrdering(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.28.0", "1.9.9", 1},
		{"8.4.25", "8.5.12", -1},
		{"24.22.2", "24.22.2", 0},
		{"5.0.14.1", "5.0.14", 1},
	}
	for _, tc := range cases {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
