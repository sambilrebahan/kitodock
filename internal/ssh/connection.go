package ssh

// Connection: dial, authenticate, request PTY, open shell; SFTP layering.

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Host is one saved SSH target.
type Host struct {
	Name     string   `json:"name"`
	Address  string   `json:"address"`
	Port     int      `json:"port"`
	User     string   `json:"user"`
	AuthMode string   `json:"authMode"`           // "password" or "key"
	Password string   `json:"password,omitempty"` // DPAPI-sealed
	KeyName  string   `json:"keyName,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	// JumpHosts names saved hosts to dial through first, ordered like
	// `ssh -J a,b`: the last entry is the direct parent of this host.
	JumpHosts []string `json:"jumpHosts,omitempty"`
	// ProxyCommand starts a local helper providing the raw connection to
	// the first hop; %h/%p expand to that hop's address and port.
	ProxyCommand string    `json:"proxyCommand,omitempty"`
	LastUsed     time.Time `json:"lastUsed,omitempty"`
}

// Session is an active SSH connection with an optional PTY.
type Session struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
	host    Host
	// chain holds the bastion clients ridden to reach client; all of them
	// are closed together with the session.
	chain []*ssh.Client
}

// Connect dials and authenticates. keyData is the private key PEM (nil for
// password auth). hostKeyCb verifies the server key — pass a KnownHosts
// TOFU callback in production; nil falls back to insecure skip (tests only).
// For jump chains and stage tracking use DialTarget.
func Connect(host Host, keyData []byte, unseal func(string) (string, error), hostKeyCb ssh.HostKeyCallback) (*Session, error) {
	return DialTarget(DialRequest{
		Host: host, KeyData: keyData, Unseal: unseal, HostKeyCb: hostKeyCb,
	})
}

// RequestPty requests a pseudo-terminal and starts a shell.
func (s *Session) RequestPty(term string, cols, rows int) error {
	session, err := s.client.NewSession()
	if err != nil {
		return fmt.Errorf("new session: %w", err)
	}
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if term == "" {
		term = "xterm-256color"
	}
	if err := session.RequestPty(term, cols, rows, modes); err != nil {
		session.Close()
		return fmt.Errorf("request pty: %w", err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		return err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return err
	}
	if err := session.Shell(); err != nil {
		session.Close()
		return fmt.Errorf("shell: %w", err)
	}
	s.session = session
	s.stdin = stdin
	s.stdout = stdout
	return nil
}

// Write sends data to the PTY.
func (s *Session) Write(data []byte) (int, error) {
	if s.stdin == nil {
		return 0, fmt.Errorf("no pty")
	}
	return s.stdin.Write(data)
}

// Read reads PTY output.
func (s *Session) Read(buf []byte) (int, error) {
	if s.stdout == nil {
		return 0, fmt.Errorf("no pty")
	}
	return s.stdout.Read(buf)
}

// Resize changes the PTY dimensions.
func (s *Session) Resize(cols, rows int) error {
	if s.session == nil {
		return fmt.Errorf("no session")
	}
	return s.session.WindowChange(rows, cols)
}

// Run executes a command and returns combined output.
func (s *Session) Run(command string, sudo bool) (string, error) {
	session, err := s.client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	if sudo {
		command = "sudo -n bash -lc " + quoteSingle(command)
	}
	out, err := session.CombinedOutput(command)
	return string(out), err
}

// Close ends the session and connection, including every bastion hop.
func (s *Session) Close() {
	if s.session != nil {
		s.session.Close()
	}
	for _, hop := range s.chain {
		hop.Close()
	}
	if s.client != nil {
		s.client.Close()
	}
}

// quoteSingle wraps a command in single quotes for a remote sh -c call,
// escaping embedded quotes the POSIX way: close the quote, emit a literal
// quote, reopen.
func quoteSingle(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('\'')
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			b.WriteString(`'\''`)
		} else {
			b.WriteByte(s[i])
		}
	}
	b.WriteByte('\'')
	return b.String()
}

// --- SFTP ----------------------------------------------------------------

// SFTPSession wraps an SFTP client over the SSH connection.
type SFTPSession struct {
	client *sftp.Client
}

// OpenSFTP opens an SFTP subsystem on the connection.
func (s *Session) OpenSFTP() (*SFTPSession, error) {
	client, err := sftp.NewClient(s.client)
	if err != nil {
		return nil, fmt.Errorf("open sftp: %w", err)
	}
	return &SFTPSession{client: client}, nil
}

// Close ends the SFTP session.
func (s *SFTPSession) Close() { s.client.Close() }

// Entry is one item in an SFTP directory listing.
type Entry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

// List lists a remote directory.
func (s *SFTPSession) List(path string) ([]Entry, error) {
	infos, err := s.client.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, info := range infos {
		out = append(out, Entry{
			Name:  info.Name(),
			IsDir: info.IsDir(),
			Size:  info.Size(),
		})
	}
	return out, nil
}

// Mkdir creates a remote directory.
func (s *SFTPSession) Mkdir(path string) error { return s.client.Mkdir(path) }

// Remove deletes a remote file.
func (s *SFTPSession) Remove(path string) error { return s.client.Remove(path) }

// Rename moves a remote file.
func (s *SFTPSession) Rename(oldPath, newPath string) error {
	return s.client.Rename(oldPath, newPath)
}

// Download streams a remote file to a local writer.
func (s *SFTPSession) Download(remotePath string, local io.Writer) error {
	remote, err := s.client.Open(remotePath)
	if err != nil {
		return fmt.Errorf("open remote %s: %w", remotePath, err)
	}
	defer remote.Close()
	_, err = io.Copy(local, remote)
	return err
}

// Upload streams a local reader to a remote file.
func (s *SFTPSession) Upload(reader io.Reader, remotePath string) error {
	remote, err := s.client.Create(remotePath)
	if err != nil {
		return fmt.Errorf("create remote %s: %w", remotePath, err)
	}
	defer remote.Close()
	_, err = io.Copy(remote, reader)
	return err
}

// ReadFile reads a remote file into memory.
func (s *SFTPSession) ReadFile(path string) ([]byte, error) {
	remote, err := s.client.Open(path)
	if err != nil {
		return nil, err
	}
	defer remote.Close()
	var buf strings.Builder
	_, err = io.Copy(&buf, remote)
	return []byte(buf.String()), err
}

// WriteFile writes data to a remote file.
func (s *SFTPSession) WriteFile(path string, data []byte) error {
	remote, err := s.client.Create(path)
	if err != nil {
		return err
	}
	defer remote.Close()
	_, err = remote.Write(data)
	return err
}

// --- Progress-aware transfers ---------------------------------------------

// ErrTransferCancelled is returned when a progress-aware transfer was
// cancelled through its cancel callback.
var ErrTransferCancelled = errors.New("transfer cancelled")

// Progress reports bytes moved out of total; total is 0 when unknown.
type Progress func(sent, total int64)

// Cancelled returns true to abort the transfer at the next write.
type Cancelled func() bool

// countingWriter forwards writes while reporting progress and honoring
// cancellation between writes.
type countingWriter struct {
	w         io.Writer
	sent      int64
	total     int64
	progress  Progress
	cancelled Cancelled
}

func (c *countingWriter) Write(p []byte) (int, error) {
	if c.cancelled != nil && c.cancelled() {
		return 0, ErrTransferCancelled
	}
	n, err := c.w.Write(p)
	c.sent += int64(n)
	if c.progress != nil {
		c.progress(c.sent, c.total)
	}
	return n, err
}

// countingReader is the read-side twin of countingWriter.
type countingReader struct {
	r         io.Reader
	sent      int64
	total     int64
	progress  Progress
	cancelled Cancelled
}

func (c *countingReader) Read(p []byte) (int, error) {
	if c.cancelled != nil && c.cancelled() {
		return 0, ErrTransferCancelled
	}
	n, err := c.r.Read(p)
	c.sent += int64(n)
	if c.progress != nil {
		c.progress(c.sent, c.total)
	}
	return n, err
}

// Stat returns a remote file's size (0 when unknown).
func (s *SFTPSession) Stat(remotePath string) (int64, error) {
	info, err := s.client.Stat(remotePath)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// UploadStream copies reader to remotePath, reporting progress (total may
// be supplied by the caller, e.g. the local file size) and aborting with
// ErrTransferCancelled once cancelled returns true.
func (s *SFTPSession) UploadStream(reader io.Reader, remotePath string, total int64, progress Progress, cancelled Cancelled) error {
	remote, err := s.client.Create(remotePath)
	if err != nil {
		return fmt.Errorf("create remote %s: %w", remotePath, err)
	}
	defer remote.Close()
	counter := &countingReader{r: reader, total: total, progress: progress, cancelled: cancelled}
	_, err = io.Copy(remote, counter)
	return err
}

// DownloadStream copies remotePath to writer with progress and
// cancellation, sizing the total from the remote file.
func (s *SFTPSession) DownloadStream(remotePath string, writer io.Writer, progress Progress, cancelled Cancelled) error {
	remote, err := s.client.Open(remotePath)
	if err != nil {
		return fmt.Errorf("open remote %s: %w", remotePath, err)
	}
	defer remote.Close()
	total := int64(0)
	if info, err := remote.Stat(); err == nil {
		total = info.Size()
	}
	counter := &countingWriter{w: writer, total: total, progress: progress, cancelled: cancelled}
	_, err = io.Copy(counter, remote)
	return err
}
