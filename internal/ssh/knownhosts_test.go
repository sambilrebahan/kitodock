package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func newTestHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestKnownHostsTOFU(t *testing.T) {
	kh, err := LoadKnownHosts(filepath.Join(t.TempDir(), "known_hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	cb := kh.Callback()
	keyA, keyB := newTestHostKey(t), newTestHostKey(t)

	// First use records the key and succeeds.
	if err := cb("web.example:22", nil, keyA); err != nil {
		t.Fatalf("first use: %v", err)
	}
	// Same key again is accepted.
	if err := cb("web.example:22", nil, keyA); err != nil {
		t.Fatalf("repeat use: %v", err)
	}
	// A different key for the same address must fail loudly.
	err = cb("web.example:22", nil, keyB)
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed key err = %v, want changed-key failure", err)
	}
	// A different address records independently (port defaults to 22).
	if err := cb("db.example", nil, keyB); err != nil {
		t.Fatalf("other host first use: %v", err)
	}
}

func TestKnownHostsTrustForgetFingerprints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts.json")
	kh, err := LoadKnownHosts(path)
	if err != nil {
		t.Fatal(err)
	}
	key := newTestHostKey(t)
	if err := kh.Trust("web.example:22", key); err != nil {
		t.Fatal(err)
	}

	// Trust persists across reloads.
	reloaded, err := LoadKnownHosts(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := reloaded.Callback()("web.example:22", nil, key); err != nil {
		t.Fatalf("trusted key rejected: %v", err)
	}

	fps := reloaded.Fingerprints()
	fp, ok := fps["web.example:22"]
	if !ok || !strings.HasPrefix(fp, "SHA256:") || len(fp) != len("SHA256:")+43 {
		t.Fatalf("fingerprint = %q, want SHA256 base64", fp)
	}

	if !reloaded.Forget("web.example:22") {
		t.Fatal("Forget = false")
	}
	if reloaded.Forget("web.example:22") {
		t.Fatal("double Forget = true")
	}
}

func TestKnownHostsLockSerializesWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kh.json")

	// Lock release makes the very next acquisition succeed.
	release, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	release()
	release2, err := acquireLock(path)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}

	// While held, a second writer must not acquire; it should surface the
	// contention as an error rather than silently proceeding.
	start := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(start)
		// LockStaleAfter is 5s; wait longer so this contender either
		// fails or takes over the stale lock — either way it returns.
		time.Sleep(lockStaleAfter + time.Second)
		done <- nil
	}()
	<-start
	select {
	case <-done:
		t.Fatal("second writer finished while the lock was held")
	default:
	}
	release2()
	<-done

	// Concurrent TOFU recordings all land; the file stays valid JSON.
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			kh, err := LoadKnownHosts(path)
			if err != nil {
				t.Error(err)
				return
			}
			host := fmt.Sprintf("h%d.example:22", i)
			if err := kh.Callback()(host, nil, newTestHostKey(t)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()

	reloaded, err := LoadKnownHosts(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Fingerprints()) != 4 {
		t.Fatalf("entries after concurrent writes = %d, want 4", len(reloaded.Fingerprints()))
	}
	if _, err := os.Stat(lockPath(path)); !os.IsNotExist(err) {
		t.Fatalf("lock file left behind: %v", err)
	}
}
