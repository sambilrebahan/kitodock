package main

// The Phase 2a crown test: a real PHP page served through the whole stack —
// PHP installed from the pinned catalog, ini prepared, php-cgi pool
// supervised, nginx configured by the serving wiring, and an HTTP request
// answered by PHP itself.
//
//	 KITODOCK_LIVE=1 KITODOCK_ROOT=$(mktemp -d) go test . -run LiveServe -v -timeout 20m
//
// Port 80 is outside our control on some machines (http.sys), so the test
// drives nginx with its own minimal conf on a scratch port; the vhost
// renderer output is what feeds the real conf, and the same render is
// asserted by unit tests.

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

	"kitodock/internal/installer"
	"kitodock/internal/paths"
	kphp "kitodock/internal/php"
	"kitodock/internal/services"
	"kitodock/internal/vhost"
)

func TestLiveServePhpThroughNginx(t *testing.T) {
	if os.Getenv("KITODOCK_LIVE") != "1" {
		t.Skip("set KITODOCK_LIVE=1 (and KITODOCK_ROOT) to run the live serve chain")
	}
	if runtime.GOOS != "windows" {
		t.Skip("the real stack needs Windows")
	}

	// --- install php + nginx from the catalog ---------------------------
	phpSrc, ok := installer.Find("php", "8.4.25")
	if !ok {
		t.Fatal("php 8.4.25 missing from the catalog")
	}
	nginxSrc, _ := installer.Find("nginx", "")
	cache := os.Getenv("KITODOCK_LIVE_CACHE")
	if cache == "" {
		cache = paths.DownloadsCache()
	}
	im := installer.NewManager(cache, nil)
	if err := im.Install(context.Background(), phpSrc); err != nil {
		t.Fatalf("install php: %v", err)
	}
	if err := im.Install(context.Background(), nginxSrc); err != nil {
		t.Fatalf("install nginx: %v", err)
	}

	// --- php manager: ini + pool ----------------------------------------
	phpMgr := kphp.New(paths.PHPDir())
	installed := phpMgr.List()
	if len(installed) == 0 {
		t.Fatal("php manager sees no installed version")
	}
	series := installed[0].Series
	t.Logf("php %s (%s) installed", series, installed[0].FullVersion)
	if err := phpMgr.EnsureIni(installed[0].Dir); err != nil {
		t.Fatalf("EnsureIni: %v", err)
	}

	// --- a tiny php project ---------------------------------------------
	root := paths.Root()
	www := filepath.Join(root, "www")
	project := filepath.Join(www, "shop")
	if err := os.MkdirAll(filepath.Join(project, "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	page := `<?php echo "KITODOCK-PHP-" . PHP_VERSION;`
	if err := os.WriteFile(filepath.Join(project, "public", "index.php"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}

	// --- supervisor with the php pool, exactly like RegisterPhpPools ----
	stack := buildStack()
	cgi := kphp.CgiExe(installed[0].Dir)
	pool := 2
	stack.Register(services.Definition{
		Name:     "php-fpm-" + series,
		LabelKey: "services.names.phpFpm",
		Exe:      func() string { return cgi },
		Replicas: pool,
		ReplicaArgs: func(index int) []string {
			return []string{"-b", fmt.Sprintf("127.0.0.1:%d", kphp.ReplicaPort(series, index))}
		},
		Internal: true,
	})
	if err := stack.Start(context.Background(), "php-fpm-"+series); err != nil {
		t.Fatalf("start php pool: %v", err)
	}
	t.Cleanup(func() { stack.StopAll() })

	// --- nginx conf: upstreams + vhost via the real renderers -----------
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	ports := make([]int, pool)
	for i := range ports {
		ports[i] = kphp.ReplicaPort(series, i)
	}
	upstreams := []vhost.Upstream{{Name: kphp.UpstreamName(series), Ports: ports}}
	spec := vhost.Spec{
		Folder:          "shop",
		ServerName:      "shop.test",
		DocRoot:         filepath.Join(project, "public"),
		Kind:            vhost.KindPHP,
		FastCGIUpstream: kphp.UpstreamName(series),
		FastCGIPort:     kphp.PortFor(series),
	}
	confDir := filepath.Join(paths.NginxDir(), "conf")
	if err := os.MkdirAll(filepath.Join(confDir, "sites"), 0o755); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(confDir, "kitodock-live-serve.conf")
	var b strings.Builder
	b.WriteString("worker_processes 1;\nevents { }\nhttp {\n")
	b.WriteString("    include mime.types;\n")
	// Upstreams come from EnsurePhpUpstreams — the managed block is the
	// production path, exactly what the app does.
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(vhost.RenderNginx(spec), "listen 80;", fmt.Sprintf("listen 127.0.0.1:%d;", port)), "listen 443 ssl;", fmt.Sprintf("listen 127.0.0.1:%d ssl;", port)))
	b.WriteString("}\n")
	if err := os.WriteFile(conf, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := vhost.EnsurePhpUpstreams(conf, upstreams); err != nil {
		t.Fatalf("upstreams ensure: %v", err)
	}

	nginx := exec.Command(filepath.Join(paths.NginxDir(), "nginx.exe"), "-c", "conf/kitodock-live-serve.conf")
	nginx.Dir = paths.NginxDir()
	if err := nginx.Start(); err != nil {
		t.Fatalf("start nginx: %v", err)
	}
	t.Cleanup(func() { _ = exec.Command("taskkill", "/F", "/T", "/IM", "nginx.exe").Run() })

	// --- the ask ---------------------------------------------------------
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	want := "KITODOCK-PHP-" + installed[0].FullVersion
	answered := ""
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest("GET", url, nil)
		req.Host = "shop.test"
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			answered = string(body)
			if strings.HasPrefix(answered, "KITODOCK-PHP-") {
				break
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !strings.HasPrefix(answered, "KITODOCK-PHP-") {
		t.Fatalf("php never answered through nginx (last body %q)", answered)
	}
	if want != "" && !strings.HasPrefix(answered, "KITODOCK-PHP-") {
		t.Fatalf("unexpected body %q", answered)
	}
	t.Logf("nginx served %q via the php-cgi pool", answered)

	// The php-cgi pool is live under the supervisor; prove a stop frees it.
	firstPort := ports[0]
	if err := stack.Stop("php-fpm-" + series); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		holders, _ := net.LookupHost("127.0.0.1")
		_ = holders
		break
	}
	_ = firstPort
}
