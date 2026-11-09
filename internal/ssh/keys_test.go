package ssh

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGenerateEd25519(t *testing.T) {
	priv, pub, err := GenerateKey(KeyEd25519, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(priv) == 0 || !strings.Contains(string(priv), "OPENSSH PRIVATE KEY") {
		t.Fatal("private key not in OpenSSH format")
	}
	if !strings.HasPrefix(pub, "ssh-ed25519 ") {
		t.Fatalf("public key = %q", pub[:20])
	}
}

func TestGenerateECDSA(t *testing.T) {
	priv, pub, err := GenerateKey(KeyECDSA, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(priv), "EC PRIVATE KEY") {
		t.Fatal("private key not EC format")
	}
	if !strings.HasPrefix(pub, "ecdsa-sha2-") {
		t.Fatalf("public key = %q", pub[:20])
	}
}

func TestGenerateUnsupported(t *testing.T) {
	if _, _, err := GenerateKey("dsa", 0); err == nil {
		t.Fatal("unsupported type accepted")
	}
}

func TestSaveAndReadPublicKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "test_key")

	priv, pub, err := GenerateKey(KeyEd25519, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveKey(keyPath, priv); err != nil {
		t.Fatal(err)
	}

	// On Unix the key must be owner-only. Windows has no POSIX perm mode —
	// Stat synthesizes 0666 for any writable file — so only Unix asserts.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(keyPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("key permissions = %v, want 0600", info.Mode().Perm())
		}
	}

	// Round-trip the public key.
	readPub, err := ReadPublicKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if readPub != pub {
		t.Fatalf("public key round-trip: %q != %q", readPub, pub)
	}
}

func TestListKeys(t *testing.T) {
	dir := t.TempDir()
	// Create some keys.
	for _, name := range []string{"deploy_key", "personal"} {
		priv, _, _ := GenerateKey(KeyEd25519, 0)
		SaveKey(filepath.Join(dir, name), priv)
	}
	// Create a non-key file and a hidden file.
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, ".hidden"), []byte("x"), 0644)

	keys, err := ListKeys(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("ListKeys = %v, want 2", keys)
	}
	found := map[string]bool{}
	for _, k := range keys {
		found[k] = true
	}
	if !found["deploy_key"] || !found["personal"] {
		t.Errorf("missing keys: %v", keys)
	}
}

func TestEffectiveDomainPrefix(t *testing.T) {
	// The Host struct only round-trips its fields here; the domain-prefix
	// behavior built on it is covered by the deploy package tests.
	h := Host{
		Name: "prod", Address: "203.0.113.1", Port: 22,
		User: "root", AuthMode: "key", KeyName: "deploy_key",
		Tags: []string{"production"},
	}
	if h.Name != "prod" || h.Port != 22 || len(h.Tags) != 1 {
		t.Fatalf("host = %+v", h)
	}
}
