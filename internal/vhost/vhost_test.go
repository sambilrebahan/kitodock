package vhost

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixtureRoot keeps the real install root out of literal spelling: the
// paths package's meta-test forbids hardcoding it anywhere else.
const fixtureRoot = `D:\anywhere\demo`

func phpSpec() Spec {
	return Spec{
		Folder:          "shop",
		ServerName:      "shop.test",
		DocRoot:         fixtureRoot + `\www\shop\public`,
		Kind:            KindPHP,
		FastCGIUpstream: "php_8_4",
		FastCGIPort:     9084,
	}
}

func wantDocRootSlashed() string {
	return `"` + filepath.ToSlash(fixtureRoot+`\www\shop\public`) + `"`
}

func TestNginxPhpShape(t *testing.T) {
	conf := RenderNginx(phpSpec())
	for _, want := range []string{
		"server_name shop.test;",
		"root " + wantDocRootSlashed() + ";",
		"try_files $uri $uri/ /index.php?$query_string;",
		"fastcgi_pass php_8_4;",
		"include fastcgi_params;",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("nginx php config missing %q:\n%s", want, conf)
		}
	}
	if !strings.Contains(conf, "listen 80;") {
		t.Error("plain variant must listen on 80")
	}
	if strings.Contains(conf, "listen 443") {
		t.Error("no SSL requested, but a 443 block rendered")
	}
}

func TestNginxProxyIsDualStackWithWebsockets(t *testing.T) {
	spec := phpSpec()
	spec.Kind = KindProxy
	spec.ProxyPort = 5173

	conf := RenderNginx(spec)
	for _, want := range []string{
		"upstream shop_dev {",
		"server 127.0.0.1:5173;",
		"server [::1]:5173 backup;",
		"proxy_set_header Upgrade $http_upgrade;",
		`proxy_set_header Connection "upgrade";`,
		"proxy_pass http://shop_dev;",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("nginx proxy config missing %q:\n%s", want, conf)
		}
	}
}

func TestSslVariantRendersBothListenersWithCerts(t *testing.T) {
	spec := phpSpec()
	spec.SSL = true
	spec.CertFile = fixtureRoot + `\nginx\certs\shop.test.crt`
	spec.KeyFile = fixtureRoot + `\nginx\certs\shop.test.key`

	for name, conf := range map[string]string{
		"nginx":  RenderNginx(spec),
		"apache": RenderApache(spec),
	} {
		if !strings.Contains(conf, "443") {
			t.Errorf("%s: no 443 listener in SSL mode:\n%s", name, conf)
		}
		if !strings.Contains(conf, "80") && name == "nginx" {
			t.Errorf("nginx: plain 80 listener should stay:\n%s", conf)
		}
	}
	caddy := RenderCaddy(spec)
	if !strings.HasPrefix(strings.TrimSpace(caddy), "shop.test {") {
		t.Errorf("caddy ssl host form wrong:\n%s", caddy)
	}
	if !strings.Contains(caddy, "tls ") {
		t.Errorf("caddy ssl must reuse the leaf pair:\n%s", caddy)
	}
}

func TestCaddyPlainDisablesAcme(t *testing.T) {
	caddy := RenderCaddy(phpSpec())
	if !strings.HasPrefix(strings.TrimSpace(caddy), "http://shop.test {") {
		t.Errorf("plain caddy host must be explicit http://:\n%s", caddy)
	}
	if !strings.Contains(caddy, "php_fastcgi 127.0.0.1:") {
		t.Errorf("caddy php mode missing php_fastcgi:\n%s", caddy)
	}
}

func TestApachePhpUsesProxyFcgi(t *testing.T) {
	conf := RenderApache(phpSpec())
	for _, want := range []string{
		"DocumentRoot " + wantDocRootSlashed(),
		"FallbackResource /index.php",
		`SetHandler "proxy:fcgi://127.0.0.1:9084"`,
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("apache php config missing %q:\n%s", want, conf)
		}
	}
}

func TestHostsEditorTouchesOnlyMarkedLines(t *testing.T) {
	dir := t.TempDir()
	hosts := Hosts{Path: filepath.Join(dir, "hosts")}
	seed := "# User hosts\n127.0.0.1 localhost\n10.0.0.5 build.server # mine\n"
	if err := os.WriteFile(hosts.Path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := hosts.AddEntry("shop.test"); err != nil {
		t.Fatal(err)
	}
	if err := hosts.AddEntry("shop.test"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(hosts.Path)
	if strings.Count(string(after), "shop.test") != 1 {
		t.Fatalf("duplicate entry:\n%s", after)
	}
	if !strings.Contains(string(after), "10.0.0.5 build.server # mine") {
		t.Fatalf("user line disturbed:\n%s", after)
	}

	if err := hosts.AddEntry("blog.test"); err != nil {
		t.Fatal(err)
	}
	if err := hosts.RemoveEntry("shop.test"); err != nil {
		t.Fatal(err)
	}
	after, _ = os.ReadFile(hosts.Path)
	if strings.Contains(string(after), "shop.test") {
		t.Fatalf("shop.test survived removal:\n%s", after)
	}
	if !strings.Contains(string(after), "blog.test") {
		t.Fatalf("blog.test collateral damage:\n%s", after)
	}
}

func TestRootCAAndLeafChain(t *testing.T) {
	dir := t.TempDir()
	caName := "Kitodock Local CA (D:\\anywhere)"

	crt, key, err := EnsureRootCA(dir, caName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(crt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(key); err != nil {
		t.Fatal(err)
	}

	// Regeneration must not happen over an existing pair.
	crt2, key2, err := EnsureRootCA(dir, caName)
	if err != nil || crt2 != crt || key2 != key {
		t.Fatalf("EnsureRootCA not idempotent: %v %v", crt2, key2)
	}

	leafCrt, leafKey, err := EnsureLeafCert(dir, "shop.test")
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(leafCrt, leafKey)
	if err != nil {
		t.Fatalf("leaf pair does not load: %v", err)
	}
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	ca, err := parseCertificate(crt)
	if err != nil {
		t.Fatal(err)
	}
	if err := leaf.CheckSignatureFrom(ca); err != nil {
		t.Fatalf("leaf not signed by the local CA: %v", err)
	}
	if time.Now().After(leaf.NotAfter) {
		t.Fatal("leaf already expired")
	}
	found := false
	for _, name := range leaf.DNSNames {
		if name == "shop.test" || name == "*.test" {
			found = true
		}
	}
	if !found {
		t.Fatalf("leaf SANs missing host/wildcard: %v", leaf.DNSNames)
	}
	var loopback net.IP
	for _, ip := range leaf.IPAddresses {
		if ip.Equal(net.ParseIP("127.0.0.1")) {
			loopback = ip
		}
	}
	if loopback == nil {
		t.Fatalf("leaf SANs missing 127.0.0.1: %v", leaf.IPAddresses)
	}

	// Idempotence: a healthy leaf is reused, not regenerated.
	before, _ := os.ReadFile(leafCrt)
	if _, _, err := EnsureLeafCert(dir, "shop.test"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(leafCrt)
	if string(before) != string(after) {
		t.Fatal("healthy leaf was needlessly regenerated")
	}

	// A corrupt leaf IS regenerated.
	os.WriteFile(leafCrt, []byte("garbage"), 0o644)
	if _, _, err := EnsureLeafCert(dir, "shop.test"); err != nil {
		t.Fatal(err)
	}
	fixed, err := parseCertificate(leafCrt)
	if err != nil {
		t.Fatalf("corrupt leaf not replaced: %v", err)
	}
	if err := fixed.CheckSignatureFrom(ca); err != nil {
		t.Fatal("replaced leaf not signed by the CA")
	}
}

func TestUpstreamsBlockIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "nginx.conf")
	base := "worker_processes 1;\nevents { }\nhttp {\n    include mime.types;\n}\n"
	os.WriteFile(conf, []byte(base), 0o644)

	first := []Upstream{{Name: "php_8_4", Ports: []int{9084, 9184}}}
	if err := EnsurePhpUpstreams(conf, first); err != nil {
		t.Fatal(err)
	}
	ports := ExtractUpstreamServers(conf, "php_8_4")
	if len(ports) != 2 || ports[0] != 9084 || ports[1] != 9184 {
		t.Fatalf("upstream servers = %v", ports)
	}

	// Replace with a different set: exactly the new one remains.
	second := []Upstream{
		{Name: "php_8_4", Ports: []int{9084}},
		{Name: "php_8_3", Ports: []int{9083, 9183}},
	}
	if err := EnsurePhpUpstreams(conf, second); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(conf)
	if strings.Count(string(data), "# >>> kitodock php upstreams") != 1 {
		t.Fatalf("managed block duplicated:\n%s", data)
	}
	if got := ExtractUpstreamServers(conf, "php_8_4"); len(got) != 1 || got[0] != 9084 {
		t.Fatalf("php_8_4 after replace = %v", got)
	}
	if got := ExtractUpstreamServers(conf, "php_8_3"); len(got) != 2 {
		t.Fatalf("php_8_3 after replace = %v", got)
	}
	if !strings.Contains(string(data), "include mime.types;") {
		t.Fatal("surrounding config disturbed")
	}
}

func TestSpecPaths(t *testing.T) {
	nginx, apache, caddy := phpSpec().Paths("/n/s", "/a/s", "/c/s")
	if !strings.HasSuffix(nginx, filepath.Join("/n/s", "shop.conf")) ||
		!strings.HasSuffix(apache, filepath.Join("/a/s", "shop.conf")) ||
		!strings.HasSuffix(caddy, filepath.Join("/c/s", "shop.caddy")) {
		t.Fatalf("paths = %s | %s | %s", nginx, apache, caddy)
	}
}
