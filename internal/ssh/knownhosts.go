package ssh

// known_hosts with TOFU (trust on first use): the first connection to an
// address records the server's public key; every later connection must
// present the same key or the handshake fails loudly.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

// KnownHosts maps "host:port" to the base64 server public key.
type KnownHosts struct {
	path    string
	entries map[string]string
}

// LoadKnownHosts reads the known_hosts file, empty when absent.
func LoadKnownHosts(path string) (*KnownHosts, error) {
	k := &KnownHosts{path: path, entries: map[string]string{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return k, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read known_hosts: %w", err)
	}
	if err := json.Unmarshal(data, &k.entries); err != nil {
		return nil, fmt.Errorf("parse known_hosts: %w", err)
	}
	return k, nil
}

// Callback returns an ssh.HostKeyCallback implementing TOFU. hostArg is
// usually the same address the dialer used; both "[host]:port" and
// "host:port" normalize to the same entry.
func (k *KnownHosts) Callback() ssh.HostKeyCallback {
	return func(hostArg string, remote net.Addr, key ssh.PublicKey) error {
		addr := normalizeHostKeyAddr(hostArg)
		b64 := base64.StdEncoding.EncodeToString(key.Marshal())
		known, seen := k.entries[addr]
		if !seen {
			k.entries[addr] = b64
			if err := k.save(); err != nil {
				return fmt.Errorf("record host key: %w", err)
			}
			return nil
		}
		if known != b64 {
			return fmt.Errorf(
				"host key for %s changed: recorded %s… now %s… — possible man-in-the-middle; remove the entry manually if the server was legitimately rebuilt",
				addr, known[:12], b64[:12])
		}
		return nil
	}
}

// Trust replaces/records the key for an address without connecting (used
// when the user explicitly accepts a changed key).
func (k *KnownHosts) Trust(address string, key ssh.PublicKey) error {
	k.entries[normalizeHostKeyAddr(address)] = base64.StdEncoding.EncodeToString(key.Marshal())
	return k.save()
}

// Forget drops an address from the store.
func (k *KnownHosts) Forget(address string) bool {
	addr := normalizeHostKeyAddr(address)
	if _, ok := k.entries[addr]; !ok {
		return false
	}
	delete(k.entries, addr)
	return k.save() == nil
}

// Fingerprints returns addr → human-readable SHA256 fingerprint.
func (k *KnownHosts) Fingerprints() map[string]string {
	out := make(map[string]string, len(k.entries))
	for addr, b64 := range k.entries {
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(raw)
		out[addr] = "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
	}
	return out
}

func (k *KnownHosts) save() error {
	if err := os.MkdirAll(filepath.Dir(k.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(k.entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := k.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, k.path)
}

func normalizeHostKeyAddr(hostArg string) string {
	if _, _, err := net.SplitHostPort(hostArg); err != nil {
		return net.JoinHostPort(hostArg, "22")
	}
	return hostArg
}
