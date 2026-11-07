package localtls

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// caFixture generates a real CA + leaf pair in dir, matching hostname.
func caFixture(t *testing.T, dir, hostname string) (caPath, leafPath, keyPath string) {
	t.Helper()
	caKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	caPath = filepath.Join(dir, "ca.crt")
	os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o644)

	leafKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	caCert, _ := x509.ParseCertificate(caDER)
	leafDER, _ := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	leafPath = filepath.Join(dir, hostname+".crt")
	os.WriteFile(leafPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), 0o644)
	keyPath = filepath.Join(dir, hostname+".key")
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(leafKey)}), 0o600)
	return
}

func TestPublicCertificatesAcceptsCABundle(t *testing.T) {
	dir := t.TempDir()
	caPath, _, _ := caFixture(t, dir, "x.test")
	data, _ := os.ReadFile(caPath)
	if err := PublicCertificates(data); err != nil {
		t.Fatalf("valid CA rejected: %v", err)
	}
	if err := PublicCertificates([]byte("-----BEGIN RSA PRIVATE KEY-----\nAAAA\n-----END RSA PRIVATE KEY-----\n")); err == nil {
		t.Fatal("private-key material accepted in a public bundle")
	}
	if err := PublicCertificates([]byte("not pem")); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestReadCAValidatesCAFlags(t *testing.T) {
	dir := t.TempDir()
	caPath, _, _ := caFixture(t, dir, "x.test")
	if _, err := ReadCA(caPath); err != nil {
		t.Fatalf("valid CA rejected: %v", err)
	}
	// A leaf (non-CA) in the CA slot must be refused.
	_, leaf, _ := caFixture(t, dir, "y.test")
	if _, err := ReadCA(leaf); err == nil {
		t.Fatal("non-CA certificate accepted as CA")
	}
}

func TestEnvironmentAppendsNodeTrustOnlyWhenUnset(t *testing.T) {
	dir := t.TempDir()
	caPath, _, _ := caFixture(t, dir, "x.test")
	project := Project{Path: dir, Hostname: "shop.test"}

	extra := Environment(nil, caPath, project)
	if !hasKey(extra, "NODE_EXTRA_CA_CERTS") {
		t.Fatalf("NODE_EXTRA_CA_CERTS not injected: %v", extra)
	}

	// User-set value wins: nothing extra is injected (the function returns
	// only additions, never the caller's own pairs).
	userSet := []string{"NODE_EXTRA_CA_CERTS=C:\\my\\ca.pem"}
	extra = Environment(userSet, caPath, project)
	if countKey(extra, "NODE_EXTRA_CA_CERTS") != 0 {
		t.Fatalf("user value overridden: %v", extra)
	}
}

func TestVitePairOnlyWhenPluginAndCertAlign(t *testing.T) {
	dir := t.TempDir()
	caPath, leafPath, keyPath := caFixture(t, dir, "shop.test")

	// Simulate laravel-vite-plugin with HTTPS support.
	pluginDir := filepath.Join(dir, "node_modules", "laravel-vite-plugin", "dist")
	os.MkdirAll(pluginDir, 0o755)
	os.WriteFile(filepath.Join(pluginDir, "index.js"), []byte("VITE_DEV_SERVER_KEY"), 0o644)

	// .env with matching APP_URL.
	os.WriteFile(filepath.Join(dir, ".env"), []byte("APP_URL=https://shop.test\n"), 0o644)

	project := Project{Path: dir, Hostname: "shop.test", CertPath: leafPath, KeyPath: keyPath}
	extra := Environment(nil, caPath, project)
	if !hasKey(extra, "VITE_DEV_SERVER_KEY") || !hasKey(extra, "VITE_DEV_SERVER_CERT") {
		t.Fatalf("vite pair not injected: %v", extra)
	}

	// Hostname mismatch: no injection.
	project.Hostname = "other.test"
	extra = Environment(nil, caPath, project)
	if hasKey(extra, "VITE_DEV_SERVER_KEY") {
		t.Fatal("injected despite hostname mismatch")
	}

	// No plugin: no injection.
	project.Hostname = "shop.test"
	os.RemoveAll(pluginDir)
	extra = Environment(nil, caPath, project)
	if hasKey(extra, "VITE_DEV_SERVER_KEY") {
		t.Fatal("injected without the plugin")
	}
}

func countKey(env []string, key string) int {
	n := 0
	for _, pair := range env {
		if len(pair) > len(key) && pair[:len(key)+1] == key+"=" {
			n++
		}
	}
	return n
}
