// Package localtls extends local-CA trust into subprocesses without
// touching machine trust stores, parent environments, or project files.
// Only environment variables are added; anything the user or project
// already set wins.
package localtls

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Project names what a TLS environment is for.
type Project struct {
	Path     string
	Hostname string
	CertPath string // existing leaf certificate (nothing is generated here)
	KeyPath  string
}

// PublicCertificates validates a PEM bundle for use as a CA: every block
// must be a CERTIFICATE, no private-key material may ride along, and at
// least one certificate must parse.
func PublicCertificates(data []byte) error {
	rest := data
	sawCert := false
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			return fmt.Errorf("unexpected PEM block %q in a public bundle", block.Type)
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return fmt.Errorf("unparseable certificate in bundle: %w", err)
		}
		sawCert = true
	}
	if !sawCert {
		return fmt.Errorf("no certificates in bundle")
	}
	return nil
}

// ReadCA loads and validates a CA bundle: every certificate must be a CA
// (IsCA with CertSign), and the file must be bounded (1 MiB).
func ReadCA(path string) ([]*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("CA bundle %s is %d bytes (cap 1 MiB)", path, len(data))
	}
	rest := data
	var out []*x509.Certificate
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("CA bundle %s: %w", path, err)
		}
		if !cert.IsCA || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
			return nil, fmt.Errorf("CA bundle %s contains a non-CA certificate", path)
		}
		out = append(out, cert)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("CA bundle %s is empty", path)
	}
	return out, nil
}

// Environment returns extra env pairs for a subprocess. It never mutates
// or returns the caller's env.
//
//	layer 1 — NODE_EXTRA_CA_CERTS: appended only when unset, pointing at
//	           the CA bundle so Node's TLS stack trusts *.test.
//	layer 2 — VITE_DEV_SERVER_KEY / VITE_DEV_SERVER_CERT: injected only
//	           when the project uses laravel-vite-plugin with HTTPS
//	           support AND a leaf cert exists that matches the hostname;
//	           anything the user or project already set wins.
func Environment(extra []string, caCertPath string, project Project) []string {
	var out []string
	add := func(key, value string) {
		out = append(out, key+"="+value)
	}

	// Layer 1: Node trust, appended only.
	if caCertPath != "" && !hasKey(extra, "NODE_EXTRA_CA_CERTS") {
		if err := PublicCertificates(mustRead(caCertPath)); err == nil {
			add("NODE_EXTRA_CA_CERTS", caCertPath)
		}
	}

	// Layer 2: Vite HTTPS, only when every guard passes.
	if v, ok := viteCertPair(extra, caCertPath, project); ok {
		for key, value := range v {
			if !hasKey(extra, key) {
				add(key, value)
			}
		}
	}

	return out
}

func mustRead(path string) []byte {
	data, _ := os.ReadFile(path)
	return data
}

func hasKey(env []string, key string) bool {
	for _, pair := range env {
		if strings.HasPrefix(strings.ToLower(pair), strings.ToLower(key)+"=") {
			return true
		}
	}
	return false
}

// viteCertPair decides the VITE_DEV_SERVER_* injection. Deliberately
// conservative: the plugin must support it, nothing may be set already,
// and the leaf must verify against the local CA and hostname.
func viteCertPair(extra []string, caCertPath string, project Project) (map[string]string, bool) {
	if project.CertPath == "" || project.KeyPath == "" || project.Hostname == "" {
		return nil, false
	}
	// The user's or project's own settings always win.
	for _, key := range []string{"VITE_DEV_SERVER_KEY", "VITE_DEV_SERVER_CERT"} {
		if hasKey(extra, key) {
			return nil, false
		}
	}
	// The project must actually use a plugin that understands these.
	if !pluginSupportsHTTPS(project.Path) {
		return nil, false
	}
	// APP_URL hostname must match the cert's hostname, no port.
	if !appURLMatches(project.Path, project.Hostname) {
		return nil, false
	}
	// The leaf must verify against the local CA.
	if !leafVerifies(caCertPath, project.CertPath, project.Hostname) {
		return nil, false
	}
	return map[string]string{
		"VITE_DEV_SERVER_KEY":  project.KeyPath,
		"VITE_DEV_SERVER_CERT": project.CertPath,
	}, true
}

// pluginMarker matches the compiled laravel-vite-plugin's HTTPS support.
var pluginMarker = regexp.MustCompile(`VITE_DEV_SERVER_KEY`)

func pluginSupportsHTTPS(projectPath string) bool {
	data, err := os.ReadFile(projectPath + string(os.PathSeparator) + "node_modules" + string(os.PathSeparator) + "laravel-vite-plugin" + string(os.PathSeparator) + "dist" + string(os.PathSeparator) + "index.js")
	if err != nil {
		return false
	}
	if len(data) > 1<<20 {
		return false
	}
	return pluginMarker.Match(data)
}

// appURLMatches checks the project's .env APP_URL host against hostname.
func appURLMatches(projectPath, hostname string) bool {
	url := dotenvValue(projectPath, "APP_URL")
	if url == "" {
		return false
	}
	// Strip scheme and port: what remains must equal the hostname.
	url = strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
	if i := strings.IndexByte(url, '/'); i > 0 {
		url = url[:i]
	}
	if i := strings.LastIndexByte(url, ':'); i > 0 {
		host := url[:i]
		// A colon in an IPv6 literal is not a port separator; only strip
		// when what follows is all digits.
		if port := url[i+1:]; port != "" && isAllDigits(port) {
			url = host
		}
	}
	return strings.EqualFold(url, hostname)
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

// dotenvValue reads one key from .env (first match wins; bounded read).
func dotenvValue(projectPath, key string) string {
	for _, name := range []string{".env", ".env.local"} {
		data, err := os.ReadFile(projectPath + string(os.PathSeparator) + name)
		if err != nil || len(data) > 1<<20 {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, key+"=") {
				continue
			}
			value := strings.TrimPrefix(line, key+"=")
			value = strings.Trim(value, `"'`)
			return value
		}
	}
	return ""
}

// leafVerifies checks the leaf cert against the CA and hostname.
func leafVerifies(caCertPath, leafPath, hostname string) bool {
	caPool := x509.NewCertPool()
	caData, err := os.ReadFile(caCertPath)
	if err != nil {
		return false
	}
	if !caPool.AppendCertsFromPEM(caData) {
		return false
	}
	leafData, err := os.ReadFile(leafPath)
	if err != nil {
		return false
	}
	block, _ := pem.Decode(leafData)
	if block == nil {
		return false
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: caPool, DNSName: hostname}); err != nil {
		return false
	}
	return true
}
