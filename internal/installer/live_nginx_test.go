package installer

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"kitodock/internal/paths"
)

// TestLiveNginxInstallStartServe proves the whole Phase 1 chain against the
// real network and a real binary: pinned download → extract → start → an
// actual HTTP answer.
//
// paths resolves the environment root at process start, so the run needs
// the env up front (CI and local use the same line):
//
//	KITODOCK_LIVE=1 KITODOCK_ROOT=$(mktemp -d) go test ./internal/installer/ -run Live -v
func TestLiveNginxInstallStartServe(t *testing.T) {
	if os.Getenv("KITODOCK_LIVE") != "1" {
		t.Skip("set KITODOCK_LIVE=1 (and KITODOCK_ROOT) to run the live chain")
	}
	if runtime.GOOS != "windows" {
		t.Skip("real services need Windows")
	}
	root := paths.Root()
	t.Logf("environment root: %s", root)

	src, ok := Find("nginx", "")
	if !ok {
		t.Fatal("nginx missing from the catalog")
	}
	cache := filepath.Join(root, "..", "kitodock-live-cache")
	manager := NewManager(cache, func(p Progress) {
		if p.Phase == PhaseDownload && p.Percent%25 == 0 {
			t.Logf("download %d%% (%.1f KiB/s)", p.Percent, float64(p.SpeedBps)/1024)
		}
	})
	if err := manager.Install(context.Background(), src); err != nil {
		t.Fatalf("install nginx: %v", err)
	}
	if !manager.IsInstalled(src) {
		t.Fatal("nginx not reported installed after install")
	}
	exe := filepath.Join(paths.NginxDir(), "nginx.exe")
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("nginx.exe missing after install: %v", err)
	}

	// :80 is commonly outside our control on Windows (http.sys holds it,
	// WinNAT excludes ranges), so the live run serves on a scratch port
	// with its own minimal config — the point is nginx really serving,
	// not the number.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	conf := fmt.Sprintf("events { }\nhttp { server { listen 127.0.0.1:%d; root html; index index.html; } }\n", port)
	confPath := filepath.Join(paths.NginxDir(), "conf", "kitodock-live.conf")
	if err := os.WriteFile(confPath, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}

	start := exec.Command(exe, "-c", "conf/kitodock-live.conf")
	start.Dir = paths.NginxDir()
	if err := start.Start(); err != nil {
		t.Fatalf("start nginx: %v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command("taskkill", "/F", "/T", "/IM", "nginx.exe").Run()
	})

	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	answered := false
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if readErr == nil && (resp.StatusCode == 200 || strings.Contains(strings.ToLower(string(body)), "nginx")) {
				answered = true
				break
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !answered {
		t.Fatalf("nginx never answered on %s", url)
	}
	t.Logf("nginx answered on :%d", port)
}
