// Package ssh manages SSH keys, hosts, connections with PTY, and SFTP
// transfers. Keys are generated locally, hosts are stored with DPAPI-sealed
// passwords, and known_hosts follows TOFU (trust on first use).
package ssh

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// KeyType identifies an SSH key algorithm.
type KeyType string

const (
	KeyEd25519 KeyType = "ed25519"
	KeyRSA     KeyType = "rsa"
	KeyECDSA   KeyType = "ecdsa"
)

// GenerateKey creates a new SSH private key of the given type.
// Returns the OpenSSH-format private key PEM and the public key string.
func GenerateKey(keyType KeyType, rsaBits int) (privatePEM []byte, publicKey string, err error) {
	switch keyType {
	case KeyEd25519:
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, "", fmt.Errorf("generate ed25519: %w", err)
		}
		// Marshal to OpenSSH format.
		privatePEM, err = marshalPrivateKey(priv, "kitodock")
		if err != nil {
			return nil, "", err
		}
		publicKey = marshalPublicKey(pub)
		return privatePEM, publicKey, nil

	case KeyRSA:
		if rsaBits < 2048 {
			rsaBits = 4096
		}
		priv, err := rsa.GenerateKey(rand.Reader, rsaBits)
		if err != nil {
			return nil, "", fmt.Errorf("generate rsa: %w", err)
		}
		privatePEM = pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(priv),
		})
		publicKey = marshalPublicKey(priv.Public())
		return privatePEM, publicKey, nil

	case KeyECDSA:
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, "", fmt.Errorf("generate ecdsa: %w", err)
		}
		der, err := x509.MarshalECPrivateKey(priv)
		if err != nil {
			return nil, "", err
		}
		privatePEM = pem.EncodeToMemory(&pem.Block{
			Type:  "EC PRIVATE KEY",
			Bytes: der,
		})
		publicKey = marshalPublicKey(priv.Public())
		return privatePEM, publicKey, nil

	default:
		return nil, "", fmt.Errorf("unsupported key type %q", keyType)
	}
}

// SaveKey writes a private key to disk with restrictive permissions.
func SaveKey(path string, pemData []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, pemData, 0o600); err != nil {
		return err
	}
	// Chmod explicitly so overwriting an existing wider-permmed file also
	// tightens it; the WriteFile perm only applies to fresh files.
	return os.Chmod(path, 0o600)
}

// ReadPublicKey reads the public key string from a private key file.
func ReadPublicKey(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return "", fmt.Errorf("%s is not a valid key file", path)
	}
	// Parse the private key to extract the public key.
	signer, err := parsePrivateKey(data)
	if err != nil {
		return "", err
	}
	return string(sshPublicKeyBytes(signer)), nil
}

// ListKeys returns all key names in a directory.
func ListKeys(keysDir string) ([]string, error) {
	entries, err := os.ReadDir(keysDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Skip public keys and dotfiles.
		if filepath.Ext(name) == ".pub" || strings.HasPrefix(name, ".") {
			continue
		}
		// Only list files that actually parse as private keys, so stray
		// notes or unrelated files never show up as selectable keys.
		if _, err := ReadPublicKey(filepath.Join(keysDir, name)); err != nil {
			continue
		}
		names = append(names, name)
	}
	return names, nil
}
