package ssh

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"encoding/pem"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// marshalPrivateKey serializes an ed25519 private key to OpenSSH format.
func marshalPrivateKey(key ed25519.PrivateKey, comment string) ([]byte, error) {
	// Use ssh.MarshalPrivateKey from x/crypto if available, otherwise PEM.
	// x/crypto/ssh doesn't export MarshalPrivateKey in all versions.
	// We'll use the raw seed in OpenSSH format.
	block, err := ssh.MarshalPrivateKey(key, comment)
	if err != nil {
		return nil, fmt.Errorf("marshal private key: %w", err)
	}
	return pem.EncodeToMemory(block), nil
}

// marshalPublicKey returns the public key in authorized_keys format.
func marshalPublicKey(key any) string {
	// Convert to ssh.PublicKey then marshal.
	var sshKey ssh.PublicKey
	switch k := key.(type) {
	case ed25519.PublicKey:
		sshKey, _ = ssh.NewPublicKey(k)
	case *ecdsa.PublicKey:
		sshKey, _ = ssh.NewPublicKey(k)
	case *rsa.PublicKey:
		sshKey, _ = ssh.NewPublicKey(k)
	}
	if sshKey == nil {
		return ""
	}
	return string(ssh.MarshalAuthorizedKey(sshKey))
}

// parsePrivateKey parses a PEM private key.
func parsePrivateKey(data []byte) (ssh.Signer, error) {
	return ssh.ParsePrivateKey(data)
}

// sshPublicKeyBytes returns the public key bytes from a signer.
func sshPublicKeyBytes(signer ssh.Signer) []byte {
	return ssh.MarshalAuthorizedKey(signer.PublicKey())
}
