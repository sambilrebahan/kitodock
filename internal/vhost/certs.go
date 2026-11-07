package vhost

// The local certificate authority: one root CA per install, per-host leaf
// certificates signed by it, and optional Windows trust so browsers stop
// warning about *.test. Everything is plain Go crypto — no openssl.

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // fingerprint display, not a password hash
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CAStatus describes the install's root CA for the Settings card.
type CAStatus struct {
	Exists     bool   `json:"exists"`
	CommonName string `json:"commonName,omitempty"`
	// Thumbprint is the SHA-1 fingerprint certutil reports (matches what
	// Windows certificate stores show).
	Thumbprint string `json:"thumbprint,omitempty"`
	Trusted    bool   `json:"trusted"`
	CheckError string `json:"checkError,omitempty"`
}

func caNames(certsDir string) (crt, key string) {
	return filepath.Join(certsDir, "kitodock-rootCA.crt"), filepath.Join(certsDir, "kitodock-rootCA.key")
}

// EnsureRootCA loads or creates the root CA in certsDir. commonName ties
// the CA to this install (the root path is truncated to fit the CN bound).
func EnsureRootCA(certsDir, commonName string) (crtPath, keyPath string, err error) {
	crtPath, keyPath = caNames(certsDir)
	if _, err := os.Stat(crtPath); err == nil {
		if _, keyErr := os.Stat(keyPath); keyErr == nil {
			return crtPath, keyPath, nil
		}
		// A CA without its key can never sign leaves: refuse to silently
		// regenerate over an existing certificate.
		return "", "", fmt.Errorf("root CA certificate exists but its key is missing: %s", crtPath)
	}
	if err := os.MkdirAll(certsDir, 0o755); err != nil {
		return "", "", err
	}

	caKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return "", "", err
	}
	// The CN bound is 64 characters; keep the root path's tail.
	name := commonName
	if len(name) > 64 {
		name = name[len(name)-64:]
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          randomSerial(),
		Subject:               pkix.Name{CommonName: name, Organization: []string{"Kitodock"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(20, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &caKey.PublicKey, caKey)
	if err != nil {
		return "", "", err
	}
	if err := writePem(crtPath, "CERTIFICATE", der, 0o644); err != nil {
		return "", "", err
	}
	if err := writePem(keyPath, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(caKey), 0o600); err != nil {
		return "", "", err
	}
	return crtPath, keyPath, nil
}

// EnsureLeafCert produces a host certificate signed by the local CA:
// the host itself, its wildcard parent, and loopback as SANs.
func EnsureLeafCert(certsDir, host string) (certPath, keyPath string, err error) {
	if err := os.MkdirAll(certsDir, 0o755); err != nil {
		return "", "", err
	}
	caCrt, caKeyPath, err := EnsureRootCA(certsDir, "Kitodock Local CA")
	if err != nil {
		return "", "", err
	}
	ca, caPriv, err := loadCA(caCrt, caKeyPath)
	if err != nil {
		return "", "", err
	}

	certPath = filepath.Join(certsDir, host+".crt")
	keyPath = filepath.Join(certsDir, host+".key")
	if _, err := os.Stat(certPath); err == nil {
		if leaf, parseErr := parseCertificate(certPath); parseErr == nil &&
			leaf.CheckSignatureFrom(ca) == nil && time.Now().Before(leaf.NotAfter) {
			if _, keyErr := os.Stat(keyPath); keyErr == nil {
				return certPath, keyPath, nil
			}
		}
		// Wrong CA, expired, or keyless: replace with a fresh pair.
	}

	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", err
	}
	sans := []string{host, "127.0.0.1", "::1"}
	if parts := strings.Split(host, "."); len(parts) >= 2 {
		sans = append(sans, "*."+strings.Join(parts[1:], "."))
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: randomSerial(),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     sans,
		IPAddresses:  nil, // loopback handled as DNS SAN entries above keep it simple
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(2, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	// 127.0.0.1 and ::1 as IP SANs (parsed properly).
	template.DNSNames = sans[:0]
	template.DNSNames = append(template.DNSNames, host)
	if parts := strings.Split(host, "."); len(parts) >= 2 {
		template.DNSNames = append(template.DNSNames, "*."+strings.Join(parts[1:], "."))
	}
	template.IPAddresses = loopbackIPs()

	der, err := x509.CreateCertificate(rand.Reader, template, ca, &leafKey.PublicKey, caPriv)
	if err != nil {
		return "", "", err
	}
	if err := writePem(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return "", "", err
	}
	if err := writePem(keyPath, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(leafKey), 0o600); err != nil {
		return "", "", err
	}
	return certPath, keyPath, nil
}

// LocalCAStatus reports whether the CA exists and whether Windows trusts
// it (certutil's presence check, deliberately not a full verify).
func LocalCAStatus(certsDir string) CAStatus {
	status := CAStatus{}
	crtPath, keyPath := caNames(certsDir)
	if _, err := os.Stat(crtPath); err != nil {
		if !os.IsNotExist(err) {
			status.CheckError = err.Error()
		}
		return status
	}
	status.Exists = true
	leaf, err := parseCertificate(crtPath)
	if err != nil {
		status.CheckError = err.Error()
		return status
	}
	status.CommonName = leaf.Subject.CommonName
	status.Thumbprint = fmt.Sprintf("%X", sha1.Sum(leaf.Raw)) //nolint:gosec
	if _, err := os.Stat(keyPath); err != nil {
		status.CheckError = "key missing"
	}
	status.Trusted = trustedByWindows(status.Thumbprint)
	return status
}

// TrustRootCA imports the CA into the machine root store (needs
// elevation), then reports the new status.
func TrustRootCA(certsDir string) error {
	crtPath, _ := caNames(certsDir)
	if _, err := os.Stat(crtPath); err != nil {
		return fmt.Errorf("no local CA to trust yet")
	}
	out, err := runCertutil("-addstore", "-f", "ROOT", crtPath)
	if err != nil {
		return fmt.Errorf("certutil addstore: %v: %s", err, strings.TrimSpace(out))
	}
	return nil
}

func trustedByWindows(thumbprint string) bool {
	if thumbprint == "" {
		return false
	}
	out, err := runCertutil("-store", "ROOT", thumbprint)
	return err == nil && strings.Contains(strings.ToLower(out), strings.ToLower(thumbprint))
}

func runCertutil(args ...string) (string, error) {
	cmd := exec.Command("certutil", args...)
	cmd.SysProcAttr = hideCertutilWindow()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// --- plumbing -------------------------------------------------------------

func loadCA(crtPath, keyPath string) (*x509.Certificate, *rsa.PrivateKey, error) {
	ca, err := parseCertificate(crtPath)
	if err != nil {
		return nil, nil, fmt.Errorf("root CA unreadable: %w", err)
	}
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(keyData)
	if block == nil {
		return nil, nil, fmt.Errorf("root CA key is not PEM")
	}
	caKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	return ca, caKey, nil
}

func parseCertificate(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("%s is not PEM", path)
	}
	return x509.ParseCertificate(block.Bytes)
}

func writePem(path, blockType string, der []byte, mode os.FileMode) error {
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), mode)
}

func randomSerial() *big.Int {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return big.NewInt(1)
	}
	return n
}

func loopbackIPs() []net.IP {
	return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
}
