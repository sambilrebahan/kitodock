package controllers

// The SSH controller: host registry (passwords sealed with DPAPI), key
// generation, interactive remote shells streamed as events, and SFTP
// browsing/transfers. Remote shells live in a separate registry from raw
// connections because one connection can host a shell, SFTP, and command
// runs at the same time.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"kitodock/internal/credentials"
	"kitodock/internal/paths"
	"kitodock/internal/ssh"
)

// SSH manages saved hosts, keys, live connections, shells and SFTP sessions.
type SSH struct {
	bus    *EventBus
	sealer credentials.Sealer
	// sshDir resolves the directory holding keys, hosts.json and
	// known_hosts.json (injected so tests can point it at a temp dir).
	sshDir func() string

	mu sync.Mutex
	// hosts/known are loaded lazily and reloaded on each access, so
	// external edits (and a second window) are always picked up.
	// conns maps connection IDs to live SSH clients.
	conns map[string]*ssh.Session
	// shells maps shell IDs to live PTY-backed remote shells; shellConns
	// remembers which connection each shell owns so closing a shell also
	// closes its connection.
	shells     map[string]*ssh.Session
	shellConns map[string]string
	// sftp maps SFTP session IDs to live SFTP clients.
	sftp map[string]*ssh.SFTPSession
	seq  atomic.Int64
}

// NewSSH builds the controller; sshDir may be nil (defaults to paths.SSHDir).
func NewSSH(bus *EventBus, sealer credentials.Sealer, sshDir func() string) *SSH {
	if sshDir == nil {
		sshDir = defaultSSHDir
	}
	return &SSH{
		bus: bus, sealer: sealer, sshDir: sshDir,
		conns: map[string]*ssh.Session{}, shells: map[string]*ssh.Session{},
		shellConns: map[string]string{}, sftp: map[string]*ssh.SFTPSession{},
	}
}

// --- Hosts ---------------------------------------------------------------

// ListHosts returns every saved host with the sealed password stripped.
func (c *SSH) ListHosts() ([]ssh.Host, error) {
	store, err := ssh.LoadHostStore(c.hostsPath())
	if err != nil {
		return nil, err
	}
	hosts := store.List()
	for i := range hosts {
		hosts[i].Password = ""
	}
	return hosts, nil
}

// SaveHost inserts or updates a host. A non-empty passwordPlain is sealed
// with DPAPI; an empty one keeps the previously sealed value.
func (c *SSH) SaveHost(host ssh.Host, passwordPlain string) error {
	if strings.TrimSpace(host.Name) == "" {
		return fmt.Errorf("host name is required")
	}
	if host.Port == 0 {
		host.Port = 22
	}
	store, err := ssh.LoadHostStore(c.hostsPath())
	if err != nil {
		return err
	}
	if passwordPlain != "" {
		sealed, err := c.sealer.Seal(passwordPlain)
		if err != nil {
			return fmt.Errorf("seal password: %w", err)
		}
		host.Password = sealed
	} else if existing, ok := store.Find(host.Name); ok {
		host.Password = existing.Password
	}
	store.Upsert(host)
	return store.Save()
}

// DeleteHost removes a host by name.
func (c *SSH) DeleteHost(name string) error {
	store, err := ssh.LoadHostStore(c.hostsPath())
	if err != nil {
		return err
	}
	store.Remove(name)
	return store.Save()
}

// --- Keys ----------------------------------------------------------------

// GenerateSSHKey creates a key of the given type ("ed25519" | "rsa" |
// "ecdsa"), saves it under the SSH dir and returns the public key line.
func (c *SSH) GenerateSSHKey(name, keyType string, rsaBits int) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("key name is required")
	}
	privatePEM, publicKey, err := ssh.GenerateKey(ssh.KeyType(keyType), rsaBits)
	if err != nil {
		return "", err
	}
	if err := ssh.SaveKey(filepath.Join(c.keysDir(), name), privatePEM); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(c.keysDir(), name+".pub"), []byte(publicKey), 0o644); err != nil {
		return "", err
	}
	return publicKey, nil
}

// ListSSHKeys returns the saved key names.
func (c *SSH) ListSSHKeys() ([]string, error) {
	return ssh.ListKeys(c.keysDir())
}

// ReadSSHPublicKey returns the public key line for a saved key.
func (c *SSH) ReadSSHPublicKey(name string) (string, error) {
	return ssh.ReadPublicKey(filepath.Join(c.keysDir(), name))
}

// --- Connections ---------------------------------------------------------

// ConnectSSH opens a connection to a saved host and returns its ID. The
// server key is verified against known_hosts with trust-on-first-use.
func (c *SSH) ConnectSSH(hostName string) (string, error) {
	store, err := ssh.LoadHostStore(c.hostsPath())
	if err != nil {
		return "", err
	}
	host, ok := store.Find(hostName)
	if !ok {
		return "", fmt.Errorf("host %q not found", hostName)
	}

	var keyData []byte
	if host.AuthMode == "key" {
		if host.KeyName == "" {
			return "", fmt.Errorf("host %q has no key selected", hostName)
		}
		keyData, err = os.ReadFile(filepath.Join(c.keysDir(), host.KeyName))
		if err != nil {
			return "", fmt.Errorf("read key %s: %w", host.KeyName, err)
		}
	}

	known, err := ssh.LoadKnownHosts(c.knownHostsPath())
	if err != nil {
		return "", err
	}
	session, err := ssh.Connect(host, keyData, c.sealer.Unseal, known.Callback())
	if err != nil {
		return "", err
	}

	id := fmt.Sprintf("c%d", c.seq.Add(1))
	c.mu.Lock()
	c.conns[id] = session
	c.mu.Unlock()
	return id, nil
}

// SSHRun executes one command over a connection.
func (c *SSH) SSHRun(connID, command string, sudo bool) (string, error) {
	session := c.conn(connID)
	if session == nil {
		return "", fmt.Errorf("connection %s not found", connID)
	}
	return session.Run(command, sudo)
}

// DisconnectSSH closes a connection and everything riding on it.
func (c *SSH) DisconnectSSH(connID string) {
	c.mu.Lock()
	session := c.conns[connID]
	delete(c.conns, connID)
	c.mu.Unlock()
	if session != nil {
		session.Close()
	}
}

// CloseAllSSH tears down every live session (app shutdown).
func (c *SSH) CloseAllSSH() {
	c.mu.Lock()
	conns := make([]*ssh.Session, 0, len(c.conns))
	for id, s := range c.conns {
		conns = append(conns, s)
		delete(c.conns, id)
	}
	for id, s := range c.shells {
		s.Close()
		delete(c.shells, id)
	}
	for id, s := range c.sftp {
		s.Close()
		delete(c.sftp, id)
	}
	c.mu.Unlock()
	for _, s := range conns {
		s.Close()
	}
}

// conn returns a live connection or nil.
func (c *SSH) conn(id string) *ssh.Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conns[id]
}

// --- Remote shells -------------------------------------------------------

// OpenSSHShell starts an interactive shell over a saved host with a PTY and
// returns its shell ID. Output streams as ssh:data events; the end of the
// shell is announced with ssh:exit.
func (c *SSH) OpenSSHShell(hostName string, cols, rows int) (string, error) {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	connID, err := c.ConnectSSH(hostName)
	if err != nil {
		return "", err
	}
	session := c.conn(connID)
	if err := session.RequestPty("xterm-256color", cols, rows); err != nil {
		c.DisconnectSSH(connID)
		return "", err
	}
	id := fmt.Sprintf("s%d", c.seq.Add(1))
	c.mu.Lock()
	c.shells[id] = session
	// The shell owns the connection now: disconnecting the shell
	// disconnects connID too, so remember it for cleanup.
	c.shellConns[id] = connID
	c.mu.Unlock()

	go c.pumpShell(id, session)
	return id, nil
}

// pumpShell streams a remote shell's output until it closes.
func (c *SSH) pumpShell(id string, session *ssh.Session) {
	buf := make([]byte, 8192)
	for {
		n, err := session.Read(buf)
		if n > 0 {
			c.bus.Emit(EvSSHData, map[string]any{"id": id, "data": string(buf[:n])})
		}
		if err != nil {
			c.bus.Emit(EvSSHExit, map[string]any{"id": id})
			c.closeShell(id)
			return
		}
	}
}

// WriteSSHShell sends keystrokes to a remote shell.
func (c *SSH) WriteSSHShell(id, data string) error {
	c.mu.Lock()
	session := c.shells[id]
	c.mu.Unlock()
	if session == nil {
		return fmt.Errorf("shell %s not found", id)
	}
	_, err := session.Write([]byte(data))
	return err
}

// ResizeSSHShell mirrors a pane resize to the remote PTY.
func (c *SSH) ResizeSSHShell(id string, cols, rows int) error {
	c.mu.Lock()
	session := c.shells[id]
	c.mu.Unlock()
	if session == nil {
		return fmt.Errorf("shell %s not found", id)
	}
	return session.Resize(cols, rows)
}

// CloseSSHShell ends a remote shell and its connection.
func (c *SSH) CloseSSHShell(id string) { c.closeShell(id) }

func (c *SSH) closeShell(id string) {
	c.mu.Lock()
	session := c.shells[id]
	connID := c.shellConns[id]
	delete(c.shells, id)
	delete(c.shellConns, id)
	if connID != "" {
		delete(c.conns, connID)
	}
	c.mu.Unlock()
	if session != nil {
		session.Close()
	}
}

// --- SFTP ----------------------------------------------------------------

// OpenSFTP starts an SFTP session over a live connection.
func (c *SSH) OpenSFTP(connID string) (string, error) {
	session := c.conn(connID)
	if session == nil {
		return "", fmt.Errorf("connection %s not found", connID)
	}
	client, err := session.OpenSFTP()
	if err != nil {
		return "", err
	}
	id := fmt.Sprintf("f%d", c.seq.Add(1))
	c.mu.Lock()
	c.sftp[id] = client
	c.mu.Unlock()
	return id, nil
}

// CloseSFTP ends an SFTP session.
func (c *SSH) CloseSFTP(id string) {
	c.mu.Lock()
	client := c.sftp[id]
	delete(c.sftp, id)
	c.mu.Unlock()
	if client != nil {
		client.Close()
	}
}

// SFTPList lists a remote directory.
func (c *SSH) SFTPList(id, path string) ([]ssh.Entry, error) {
	client := c.sftpClient(id)
	if client == nil {
		return nil, fmt.Errorf("sftp %s not found", id)
	}
	return client.List(path)
}

// SFTPMkdir creates a remote directory.
func (c *SSH) SFTPMkdir(id, path string) error {
	client := c.sftpClient(id)
	if client == nil {
		return fmt.Errorf("sftp %s not found", id)
	}
	return client.Mkdir(path)
}

// SFTPRemove deletes a remote file.
func (c *SSH) SFTPRemove(id, path string) error {
	client := c.sftpClient(id)
	if client == nil {
		return fmt.Errorf("sftp %s not found", id)
	}
	return client.Remove(path)
}

// SFTPRename renames a remote file.
func (c *SSH) SFTPRename(id, oldPath, newPath string) error {
	client := c.sftpClient(id)
	if client == nil {
		return fmt.Errorf("sftp %s not found", id)
	}
	return client.Rename(oldPath, newPath)
}

// SFTPReadFile returns a small remote file's content (config editing).
func (c *SSH) SFTPReadFile(id, path string) (string, error) {
	client := c.sftpClient(id)
	if client == nil {
		return "", fmt.Errorf("sftp %s not found", id)
	}
	data, err := client.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// SFTPWriteFile replaces a small remote file's content.
func (c *SSH) SFTPWriteFile(id, path, content string) error {
	client := c.sftpClient(id)
	if client == nil {
		return fmt.Errorf("sftp %s not found", id)
	}
	return client.WriteFile(path, []byte(content))
}

// SFTPDownload copies a remote file to a local path.
func (c *SSH) SFTPDownload(id, remotePath, localPath string) error {
	client := c.sftpClient(id)
	if client == nil {
		return fmt.Errorf("sftp %s not found", id)
	}
	local, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", localPath, err)
	}
	defer local.Close()
	return client.Download(remotePath, local)
}

// SFTPUpload copies a local file to a remote path.
func (c *SSH) SFTPUpload(id, localPath, remotePath string) error {
	client := c.sftpClient(id)
	if client == nil {
		return fmt.Errorf("sftp %s not found", id)
	}
	local, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", localPath, err)
	}
	defer local.Close()
	return client.Upload(local, remotePath)
}

func (c *SSH) sftpClient(id string) *ssh.SFTPSession {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sftp[id]
}

// --- Paths ---------------------------------------------------------------

func (c *SSH) hostsPath() string      { return filepath.Join(c.sshDir(), "hosts.json") }
func (c *SSH) knownHostsPath() string { return filepath.Join(c.sshDir(), "known_hosts.json") }
func (c *SSH) keysDir() string        { return filepath.Join(c.sshDir(), "keys") }

func defaultSSHDir() string { return paths.SSHDir() }
