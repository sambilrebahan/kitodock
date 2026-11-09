package ssh

// Connection: dial, authenticate, request PTY, open shell; SFTP layering.

import (
	"fmt"
	"io"
	"net"
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
}

// Session is an active SSH connection with an optional PTY.
type Session struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
	host    Host
}

// Connect dials and authenticates. keyData is the private key PEM (nil for
// password auth). hostKeyCb verifies the server key — pass a KnownHosts
// TOFU callback in production; nil falls back to insecure skip (tests only).
func Connect(host Host, keyData []byte, unseal func(string) (string, error), hostKeyCb ssh.HostKeyCallback) (*Session, error) {
	if hostKeyCb == nil {
		hostKeyCb = ssh.InsecureIgnoreHostKey()
	}
	config := &ssh.ClientConfig{
		User:            host.User,
		HostKeyCallback: hostKeyCb,
		Timeout:         10 * time.Second,
	}

	switch host.AuthMode {
	case "key":
		if keyData != nil {
			signer, err := ssh.ParsePrivateKey(keyData)
			if err != nil {
				return nil, fmt.Errorf("parse key: %w", err)
			}
			config.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
		}
	case "password":
		password, err := unseal(host.Password)
		if err != nil {
			return nil, fmt.Errorf("unseal password: %w", err)
		}
		config.Auth = []ssh.AuthMethod{ssh.Password(password)}
	default:
		return nil, fmt.Errorf("unsupported auth mode %q", host.AuthMode)
	}

	addr := net.JoinHostPort(host.Address, fmt.Sprintf("%d", host.Port))
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	return &Session{client: client, host: host}, nil
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

// Close ends the session and connection.
func (s *Session) Close() {
	if s.session != nil {
		s.session.Close()
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
