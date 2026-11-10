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
	"time"

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
// "host:port" normalize to the same entry. First use records the key with
// a locked read-modify-write so a concurrent process's entries survive;
// a mismatch is re-checked from disk before failing so stale in-memory
// views never raise a false man-in-the-middle alarm.
func (k *KnownHosts) Callback() ssh.HostKeyCallback {
	return func(hostArg string, remote net.Addr, key ssh.PublicKey) error {
		addr := normalizeHostKeyAddr(hostArg)
		b64 := base64.StdEncoding.EncodeToString(key.Marshal())

		if known, seen := k.entries[addr]; seen {
			if known == b64 {
				return nil
			}
			disk, err := k.currentEntry(addr)
			if err != nil {
				return err
			}
			if disk != b64 {
				return changedKeyError(addr, disk, b64)
			}
			k.entries[addr] = b64
			return nil
		}
		return k.recordFirstUse(addr, b64)
	}
}

// changedKeyError is the loud failure for a server presenting a different
// key than the recorded one.
func changedKeyError(addr, recorded, presented string) error {
	return fmt.Errorf(
		"host key for %s changed: recorded %s… now %s… — possible man-in-the-middle; remove the entry manually if the server was legitimately rebuilt",
		addr, clip12(recorded), clip12(presented))
}

// clip12 truncates safely: empty or short digests must not panic the
// error path that exists to report them.
func clip12(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// currentEntry re-reads one entry from disk while holding the lock.
func (k *KnownHosts) currentEntry(addr string) (string, error) {
	release, err := acquireLock(k.path)
	if err != nil {
		return "", err
	}
	defer release()
	disk, err := LoadKnownHosts(k.path)
	if err != nil {
		return "", err
	}
	return disk.entries[addr], nil
}

// recordFirstUse merges a first-seen key into the on-disk store under the
// lock, so parallel writers each preserve the others' entries.
func (k *KnownHosts) recordFirstUse(addr, b64 string) error {
	release, err := acquireLock(k.path)
	if err != nil {
		return fmt.Errorf("record host key: %w", err)
	}
	defer release()

	if disk, err := LoadKnownHosts(k.path); err == nil {
		for a, existing := range disk.entries {
			if _, mine := k.entries[a]; !mine {
				k.entries[a] = existing
			}
		}
		if known, seen := k.entries[addr]; seen && known != b64 {
			return changedKeyError(addr, known, b64)
		}
	}
	k.entries[addr] = b64
	if err := k.saveLocked(); err != nil {
		return fmt.Errorf("record host key: %w", err)
	}
	return nil
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

// Algorithms returns addr → SSH key type of the recorded server key
// (ssh-ed25519, ssh-rsa, …), so users can see which algorithms they trust.
func (k *KnownHosts) Algorithms() map[string]string {
	out := make(map[string]string, len(k.entries))
	for addr, b64 := range k.entries {
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			continue
		}
		if key, err := ssh.ParsePublicKey(raw); err == nil {
			out[addr] = key.Type()
		}
	}
	return out
}

// lockPath derives the advisory lock file path for a known_hosts file.
func lockPath(path string) string { return path + ".lock" }

// acquireLock takes the advisory lock guarding known_hosts writes across
// processes. The lock is a create-exclusive marker file; a holder that died
// without releasing leaves it behind, so a lock older than lockStaleAfter
// is taken over.
const lockStaleAfter = 5 * time.Second

func acquireLock(path string) (release func(), err error) {
	lock := lockPath(path)
	deadline := time.Now().Add(lockStaleAfter)
	for {
		f, cerr := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if cerr == nil {
			_ = f.Close()
			return func() { os.Remove(lock) }, nil
		}
		if info, serr := os.Stat(lock); serr == nil && time.Since(info.ModTime()) > lockStaleAfter {
			// Stale holder: reclaim. A racing reclaim shows up as a
			// failing remove below, which is harmless.
			_ = os.Remove(lock)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("known_hosts lock %s held by another process", lock)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (k *KnownHosts) save() error {
	release, err := acquireLock(k.path)
	if err != nil {
		return err
	}
	defer release()
	return k.saveLocked()
}

// saveLocked persists without touching the lock; the caller holds it.
func (k *KnownHosts) saveLocked() error {
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
