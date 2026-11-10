package controllers

// The SSH controller: host registry (passwords sealed with DPAPI), key
// generation, interactive remote shells streamed as events, and SFTP
// browsing/transfers. Remote shells live in a separate registry from raw
// connections because one connection can host a shell, SFTP, and command
// runs at the same time.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
	// conns maps connection IDs to live SSH clients; connHosts remembers
	// which saved host each connection belongs to.
	conns     map[string]*ssh.Session
	connHosts map[string]string
	// shells maps shell IDs to live PTY-backed remote shells; shellConns
	// remembers which connection each shell owns so closing a shell also
	// closes its connection.
	shells     map[string]*ssh.Session
	shellConns map[string]string
	// sftp maps SFTP session IDs to live clients, remembering the host
	// each belongs to for freeze support.
	sftp map[string]*sftpEntry
	// transfers maps transfer IDs to cancellable upload/download state.
	transfers map[string]*sftpTransfer
	// edits maps external-edit session IDs to their watchers.
	edits map[string]*remoteEdit
	// frozen lists hosts whose SFTP listings are served from the snapshot
	// cache (set while a deploy mutates the server); listingCache keeps
	// the last live listing per host+path.
	frozen       map[string]bool
	listingCache map[string][]ssh.Entry
	seq          atomic.Int64
}

// sftpEntry couples an SFTP client with the host it was opened for.
type sftpEntry struct {
	client *ssh.SFTPSession
	host   string
}

// sftpTransfer tracks one cancellable transfer.
type sftpTransfer struct {
	cancel func()
}

// NewSSH builds the controller; sshDir may be nil (defaults to paths.SSHDir).
func NewSSH(bus *EventBus, sealer credentials.Sealer, sshDir func() string) *SSH {
	if sshDir == nil {
		sshDir = defaultSSHDir
	}
	return &SSH{
		bus: bus, sealer: sealer, sshDir: sshDir,
		conns: map[string]*ssh.Session{}, connHosts: map[string]string{},
		shells: map[string]*ssh.Session{}, shellConns: map[string]string{},
		sftp: map[string]*sftpEntry{}, transfers: map[string]*sftpTransfer{},
		edits: map[string]*remoteEdit{}, frozen: map[string]bool{},
		listingCache: map[string][]ssh.Entry{},
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
	if !safeName(name) {
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

// InspectSSHKeys returns the saved keys with algorithm and fingerprint.
func (c *SSH) InspectSSHKeys() ([]ssh.KeyInfo, error) {
	return ssh.InspectKeys(c.keysDir())
}

// safeName rejects path tricks before a caller-supplied name reaches
// filepath.Join: one plain file name, no separators, no parent hops.
func safeName(name string) bool {
	return name != "" && name == filepath.Base(name) && name != "." && name != ".." && !strings.ContainsAny(name, `\/:*?"<>|`)
}

// ReadSSHPublicKey returns the public key line for a saved key.
func (c *SSH) ReadSSHPublicKey(name string) (string, error) {
	if !safeName(name) {
		return "", fmt.Errorf("invalid key name")
	}
	return ssh.ReadPublicKey(filepath.Join(c.keysDir(), name))
}

// ImportSSHKey validates an externally supplied private key PEM, saves it
// under the given name and returns its public key line.
func (c *SSH) ImportSSHKey(name, pemData string) (string, error) {
	if !safeName(name) {
		return "", fmt.Errorf("invalid key name")
	}
	return ssh.ImportKey(c.keysDir(), name, []byte(pemData))
}

// --- Connections ---------------------------------------------------------

// ConnectSSH opens a connection to a saved host and returns its ID. The
// server key is verified against known_hosts with trust-on-first-use,
// jump chains and proxy commands are honored, and each dial stage is
// announced as an ssh:connect:<host> event.
func (c *SSH) ConnectSSH(hostName string) (string, error) {
	req, store, err := c.dialRequestFor(hostName)
	if err != nil {
		return "", err
	}
	host := req.Host

	// Stage timeline for the UI; one event per marker, tagged per host.
	req.OnStage = func(stage ssh.Stage) {
		c.bus.Emit(EvSSHConnectStage, map[string]any{
			"host": hostName, "stage": stage.Name,
			"detail": stage.Detail, "ms": stage.Milliseconds,
		})
	}

	session, err := ssh.DialTarget(req)
	if err != nil {
		return "", err
	}

	// Record the use for the "recent hosts" ordering.
	host.LastUsed = time.Now()
	store.Upsert(host)
	_ = store.Save()

	id := fmt.Sprintf("c%d", c.seq.Add(1))
	c.mu.Lock()
	c.conns[id] = session
	c.connHosts[id] = hostName
	c.mu.Unlock()
	return id, nil
}

// hostOfConn returns the saved-host name a connection belongs to.
func (c *SSH) hostOfConn(connID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connHosts[connID]
}

// dialRequestFor assembles the dial request for a saved host, including
// the jump resolver that pulls bastion credentials from the same store.
func (c *SSH) dialRequestFor(hostName string) (ssh.DialRequest, *ssh.HostStore, error) {
	store, err := ssh.LoadHostStore(c.hostsPath())
	if err != nil {
		return ssh.DialRequest{}, nil, err
	}
	host, ok := store.Find(hostName)
	if !ok {
		return ssh.DialRequest{}, nil, fmt.Errorf("host %q not found", hostName)
	}
	req, err := c.requestFromStore(store, host)
	if err != nil {
		return ssh.DialRequest{}, nil, err
	}
	req.ResolveJump = func(name string) (*ssh.DialRequest, error) {
		jumpHost, ok := store.Find(name)
		if !ok {
			return nil, fmt.Errorf("jump host %q is not a saved host", name)
		}
		jumpReq, err := c.requestFromStore(store, jumpHost)
		if err != nil {
			return nil, err
		}
		return &jumpReq, nil
	}
	return req, store, nil
}

// requestFromStore builds the dial request for one host from the store,
// loading its private key when it authenticates that way.
func (c *SSH) requestFromStore(store *ssh.HostStore, host ssh.Host) (ssh.DialRequest, error) {
	known, err := ssh.LoadKnownHosts(c.knownHostsPath())
	if err != nil {
		return ssh.DialRequest{}, err
	}
	req := ssh.DialRequest{
		Host: host, Unseal: c.sealer.Unseal, HostKeyCb: known.Callback(),
	}
	if host.AuthMode == "key" {
		if host.KeyName == "" {
			return ssh.DialRequest{}, fmt.Errorf("host %q has no key selected", host.Name)
		}
		req.KeyData, err = os.ReadFile(filepath.Join(c.keysDir(), host.KeyName))
		if err != nil {
			return ssh.DialRequest{}, fmt.Errorf("read key %s: %w", host.KeyName, err)
		}
	}
	return req, nil
}

// hostStoreForImport exposes the loaded host store to the deployments
// controller's bundle import.
func (c *SSH) hostStoreForImport() (*ssh.HostStore, error) {
	return ssh.LoadHostStore(c.hostsPath())
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
		delete(c.connHosts, id)
	}
	for id, s := range c.shells {
		s.Close()
		delete(c.shells, id)
		delete(c.shellConns, id)
	}
	for id, entry := range c.sftp {
		entry.client.Close()
		delete(c.sftp, id)
	}
	edits := make([]*remoteEdit, 0, len(c.edits))
	for id, edit := range c.edits {
		edits = append(edits, edit)
		delete(c.edits, id)
	}
	c.mu.Unlock()
	for _, s := range conns {
		s.Close()
	}
	for _, edit := range edits {
		close(edit.stopCh)
		os.Remove(edit.view.LocalPath)
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
			termAppend(id, string(buf[:n]))
		}
		if err != nil {
			c.bus.Emit(EvSSHExit, map[string]any{"id": id})
			termExit(id)
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
	c.sftp[id] = &sftpEntry{client: client, host: c.hostOfConnLocked(connID)}
	c.mu.Unlock()
	return id, nil
}

// hostOfConnLocked is hostOfConn for callers already holding the mutex.
func (c *SSH) hostOfConnLocked(connID string) string { return c.connHosts[connID] }

// CloseSFTP ends an SFTP session.
func (c *SSH) CloseSFTP(id string) {
	c.mu.Lock()
	entry := c.sftp[id]
	delete(c.sftp, id)
	c.mu.Unlock()
	if entry != nil {
		entry.client.Close()
	}
}

// SFTPList lists a remote directory. While the host is frozen (a deploy
// run is mutating it), previously seen paths serve their snapshot; paths
// not yet cached refuse rather than read half-written state.
func (c *SSH) SFTPList(id, path string) ([]ssh.Entry, error) {
	entry := c.sftpClient(id)
	if entry == nil {
		return nil, fmt.Errorf("sftp %s not found", id)
	}
	cacheKey := entry.host + "|" + path
	c.mu.Lock()
	frozen := c.frozen[entry.host]
	cached, hasCache := c.listingCache[cacheKey]
	c.mu.Unlock()

	if frozen {
		if !hasCache {
			return nil, fmt.Errorf("%s is being mutated by a deployment run; %s was not listed before it started",
				entry.host, path)
		}
		return cached, nil
	}
	entries, err := entry.client.List(path)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.listingCache[cacheKey] = entries
	c.mu.Unlock()
	return entries, nil
}

// SetHostFrozen toggles the snapshot mode for one host's SFTP listings.
func (c *SSH) SetHostFrozen(hostName string, frozen bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frozen[hostName] = frozen
}

// SFTPMkdir creates a remote directory.
func (c *SSH) SFTPMkdir(id, path string) error {
	client := c.sftpClient(id)
	if client == nil {
		return fmt.Errorf("sftp %s not found", id)
	}
	return client.client.Mkdir(path)
}

// SFTPRemove deletes a remote file.
func (c *SSH) SFTPRemove(id, path string) error {
	client := c.sftpClient(id)
	if client == nil {
		return fmt.Errorf("sftp %s not found", id)
	}
	return client.client.Remove(path)
}

// SFTPRename renames a remote file.
func (c *SSH) SFTPRename(id, oldPath, newPath string) error {
	client := c.sftpClient(id)
	if client == nil {
		return fmt.Errorf("sftp %s not found", id)
	}
	return client.client.Rename(oldPath, newPath)
}

// SFTPReadFile returns a small remote file's content (config editing).
func (c *SSH) SFTPReadFile(id, path string) (string, error) {
	client := c.sftpClient(id)
	if client == nil {
		return "", fmt.Errorf("sftp %s not found", id)
	}
	data, err := client.client.ReadFile(path)
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
	return client.client.WriteFile(path, []byte(content))
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
	return client.client.Download(remotePath, local)
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
	return client.client.Upload(local, remotePath)
}

// --- Progress-aware transfers ---------------------------------------------

// SFTPUploadAsync starts a cancellable upload and returns its transfer ID.
// Progress streams as sftp:progress:<id> {bytes,total}; completion (or
// cancellation/error) is announced by sftp:done:<id> {error}.
func (c *SSH) SFTPUploadAsync(id, localPath, remotePath string) (string, error) {
	entry := c.sftpClient(id)
	if entry == nil {
		return "", fmt.Errorf("sftp %s not found", id)
	}
	info, err := os.Stat(localPath)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", localPath, err)
	}
	transferID := fmt.Sprintf("t%d", c.seq.Add(1))
	cancelFlag := newCancelFlag()
	c.mu.Lock()
	c.transfers[transferID] = &sftpTransfer{cancel: cancelFlag.Cancel}
	c.mu.Unlock()

	go c.runTransfer(transferID, func() error {
		local, err := os.Open(localPath)
		if err != nil {
			return err
		}
		defer local.Close()
		return entry.client.UploadStream(local, remotePath, info.Size(),
			progressSink(c.bus, transferID), cancelFlag.Check)
	})
	return transferID, nil
}

// SFTPDownloadAsync is the download twin of SFTPUploadAsync.
func (c *SSH) SFTPDownloadAsync(id, remotePath, localPath string) (string, error) {
	entry := c.sftpClient(id)
	if entry == nil {
		return "", fmt.Errorf("sftp %s not found", id)
	}
	transferID := fmt.Sprintf("t%d", c.seq.Add(1))
	cancelFlag := newCancelFlag()
	c.mu.Lock()
	c.transfers[transferID] = &sftpTransfer{cancel: cancelFlag.Cancel}
	c.mu.Unlock()

	go c.runTransfer(transferID, func() error {
		local, err := os.Create(localPath)
		if err != nil {
			return err
		}
		defer local.Close()
		return entry.client.DownloadStream(remotePath, local,
			progressSink(c.bus, transferID), cancelFlag.Check)
	})
	return transferID, nil
}

// CancelSFTPTransfer aborts a running transfer; its sftp:done event
// carries the cancellation error.
func (c *SSH) CancelSFTPTransfer(transferID string) {
	c.mu.Lock()
	transfer := c.transfers[transferID]
	c.mu.Unlock()
	if transfer != nil {
		transfer.cancel()
	}
}

// runTransfer executes one transfer body and reports completion.
func (c *SSH) runTransfer(transferID string, body func() error) {
	err := body()
	c.mu.Lock()
	delete(c.transfers, transferID)
	c.mu.Unlock()
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	c.bus.Emit(sftpDonePrefix+transferID, map[string]any{
		"id": transferID, "error": msg,
	})
}

// progressSink turns ssh.Progress into bus events.
func progressSink(bus *EventBus, transferID string) ssh.Progress {
	return func(sent, total int64) {
		bus.Emit(sftpProgressPrefix+transferID, map[string]any{
			"id": transferID, "bytes": sent, "total": total,
		})
	}
}

// cancelFlag is a thread-safe boolean set once by Cancel.
type cancelFlag struct {
	mu        sync.Mutex
	cancelled bool
}

func newCancelFlag() *cancelFlag { return &cancelFlag{} }

func (f *cancelFlag) Cancel() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = true
}

func (f *cancelFlag) Check() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancelled
}

// LocalEntry is one item in a local directory listing (SFTP dual pane).
type LocalEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

// ListLocalDir lists a local directory for the SFTP browser's local pane.
// An empty path lists the user's home directory as the starting point.
func (c *SSH) ListLocalDir(path string) ([]LocalEntry, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = home
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := make([]LocalEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue // vanished between listing and stat
		}
		out = append(out, LocalEntry{
			Name: e.Name(), IsDir: e.IsDir(), Size: info.Size(),
		})
	}
	return out, nil
}

// ReadLocalTextFile reads a small local text file (bundle import).
func (c *SSH) ReadLocalTextFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// WriteLocalTextFile writes a small local text file (bundle export).
func (c *SSH) WriteLocalTextFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

// --- External edit sessions -------------------------------------------------

// RemoteEditView describes one open "edit remote file in a local editor"
// session.
type RemoteEditView struct {
	ID         string `json:"id"`
	RemotePath string `json:"remotePath"`
	LocalPath  string `json:"localPath"`
	Host       string `json:"host"`
}

// remoteEdit watches a downloaded copy's mtime and re-uploads on change.
type remoteEdit struct {
	view     RemoteEditView
	sftpID   string
	lastMod  time.Time
	lastSize int64
	stopCh   chan struct{}
}

// StartRemoteEdit downloads a remote file to a temp copy, watches the
// copy's mtime/size and re-uploads whenever the local editor saves. The
// returned view carries the local path to reveal to the user.
func (c *SSH) StartRemoteEdit(sftpID, remotePath string) (RemoteEditView, error) {
	entry := c.sftpClient(sftpID)
	if entry == nil {
		return RemoteEditView{}, fmt.Errorf("sftp %s not found", sftpID)
	}
	name := filepath.Base(strings.ReplaceAll(remotePath, `\`, "/"))
	tmp, err := os.CreateTemp("", "kitodock-edit-*-"+name)
	if err != nil {
		return RemoteEditView{}, err
	}
	localPath := tmp.Name()
	tmp.Close()

	if err := c.SFTPDownload(sftpID, remotePath, localPath); err != nil {
		os.Remove(localPath)
		return RemoteEditView{}, err
	}
	info, err := os.Stat(localPath)
	if err != nil {
		return RemoteEditView{}, err
	}

	id := fmt.Sprintf("e%d", c.seq.Add(1))
	edit := &remoteEdit{
		view:   RemoteEditView{ID: id, RemotePath: remotePath, LocalPath: localPath, Host: entry.host},
		sftpID: sftpID, lastMod: info.ModTime(), lastSize: info.Size(),
		stopCh: make(chan struct{}),
	}
	c.mu.Lock()
	c.edits[id] = edit
	c.mu.Unlock()
	go c.watchEdit(edit)
	return edit.view, nil
}

// watchEdit polls the local copy and re-uploads on every save.
func (c *SSH) watchEdit(edit *remoteEdit) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-edit.stopCh:
			return
		case <-ticker.C:
			info, err := os.Stat(edit.view.LocalPath)
			if err != nil {
				continue // temp copy vanished; keep watching
			}
			if info.ModTime().Equal(edit.lastMod) && info.Size() == edit.lastSize {
				continue
			}
			edit.lastMod = info.ModTime()
			edit.lastSize = info.Size()
			data, err := os.ReadFile(edit.view.LocalPath)
			if err != nil {
				continue
			}
			err = c.SFTPWriteFile(edit.sftpID, edit.view.RemotePath, string(data))
			action := "reuploaded"
			if err != nil {
				action = "error: " + err.Error()
			}
			c.bus.Emit(EvSSHExternalEdit, map[string]any{
				"id": edit.view.ID, "action": action,
				"remotePath": edit.view.RemotePath,
			})
		}
	}
}

// StopRemoteEdit ends a watch session; the temp copy is removed unless
// keepLocal is true.
func (c *SSH) StopRemoteEdit(id string, keepLocal bool) {
	c.mu.Lock()
	edit := c.edits[id]
	delete(c.edits, id)
	c.mu.Unlock()
	if edit == nil {
		return
	}
	close(edit.stopCh)
	if !keepLocal {
		os.Remove(edit.view.LocalPath)
	}
}

// RevealLocalPath opens the file's folder in Explorer with the file
// selected, so the user can edit it with any local tool.
func (c *SSH) RevealLocalPath(path string) error {
	return exec.Command("explorer", "/select,"+path).Start()
}

func (c *SSH) sftpClient(id string) *sftpEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sftp[id]
}

// --- Paths ---------------------------------------------------------------

func (c *SSH) hostsPath() string      { return filepath.Join(c.sshDir(), "hosts.json") }
func (c *SSH) knownHostsPath() string { return filepath.Join(c.sshDir(), "known_hosts.json") }
func (c *SSH) keysDir() string        { return filepath.Join(c.sshDir(), "keys") }

func defaultSSHDir() string { return paths.SSHDir() }
