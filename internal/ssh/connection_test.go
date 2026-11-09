package ssh

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// testServer is a minimal in-process SSH server: password auth, an exec
// handler that echoes the command, a shell that echoes input, and an SFTP
// subsystem rooted at a temp directory. It lets the client code be tested
// against a real handshake without any network dependency.
type testServer struct {
	t        *testing.T
	listener net.Listener
	config   *ssh.ServerConfig
	sftpRoot string
}

const (
	testUser     = "tester"
	testPassword = "s3cret"
)

func startTestServer(t *testing.T) *testServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if conn.User() == testUser && string(password) == testPassword {
				return nil, nil
			}
			return nil, fmt.Errorf("auth rejected for %s", conn.User())
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &testServer{t: t, listener: ln, config: cfg, sftpRoot: t.TempDir()}
	go s.acceptLoop()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *testServer) addr() string { return s.listener.Addr().String() }

func (s *testServer) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *testServer) handleConn(conn net.Conn) {
	serverConn, chans, reqs, err := ssh.NewServerConn(conn, s.config)
	if err != nil {
		return
	}
	defer serverConn.Close()
	go ssh.DiscardRequests(reqs)
	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			newChan.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		channel, requests, err := newChan.Accept()
		if err != nil {
			continue
		}
		go s.handleSession(channel, requests)
	}
}

func (s *testServer) handleSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	for req := range requests {
		switch req.Type {
		case "exec":
			var payload struct{ Command string }
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			fmt.Fprintf(channel, "ran: %s", payload.Command)
			channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			channel.Close()
		case "pty-req":
			req.Reply(true, nil)
		case "shell":
			req.Reply(true, nil)
			go io.Copy(channel, channel) // echo shell
		case "subsystem":
			var payload struct{ Name string }
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil || payload.Name != "sftp" {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			srv, err := sftp.NewServer(channel, sftp.WithServerWorkingDirectory(s.sftpRoot))
			if err != nil {
				continue
			}
			srv.Serve()
			srv.Close()
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

func testHost(addr string) Host {
	_, port, _ := net.SplitHostPort(addr)
	return Host{
		Name: "test", Address: "127.0.0.1", Port: mustAtoi(port),
		User: testUser, AuthMode: "password", Password: "plain:" + testPassword,
	}
}

func mustAtoi(s string) int {
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}

func plainUnseal(sealed string) (string, error) {
	return strings.TrimPrefix(sealed, "plain:"), nil
}

func TestConnectRejectsBadCredentials(t *testing.T) {
	srv := startTestServer(t)
	host := testHost(srv.addr())
	host.Password = "plain:wrong"

	if _, err := Connect(host, nil, plainUnseal, nil); err == nil {
		t.Fatal("Connect accepted a wrong password")
	}
}

func TestConnectRunAndTOFU(t *testing.T) {
	srv := startTestServer(t)

	kh, err := LoadKnownHosts(filepath.Join(t.TempDir(), "kh.json"))
	if err != nil {
		t.Fatal(err)
	}
	cb := kh.Callback()

	// First connection records the host key (TOFU).
	sess, err := Connect(testHost(srv.addr()), nil, plainUnseal, cb)
	if err != nil {
		t.Fatal(err)
	}
	out, err := sess.Run("uname -a", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ran: uname -a") {
		t.Fatalf("Run output = %q", out)
	}

	// Second connection with the same key must pass verification.
	sess2, err := Connect(testHost(srv.addr()), nil, plainUnseal, cb)
	if err != nil {
		t.Fatalf("second connect: %v", err)
	}
	sess2.Close()

	// A different server key on the same address must be refused.
	kh2, _ := LoadKnownHosts(filepath.Join(t.TempDir(), "kh2.json"))
	cb2 := kh2.Callback()
	if _, err := Connect(testHost(srv.addr()), nil, plainUnseal, cb2); err != nil {
		t.Fatal(err)
	}
	bad := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		return fmt.Errorf("nope")
	}
	if _, err := Connect(testHost(srv.addr()), nil, plainUnseal, bad); err == nil {
		t.Fatal("Connect ignored a rejecting host-key callback")
	}
}

func TestRequestPtyEchoShell(t *testing.T) {
	srv := startTestServer(t)
	sess, err := Connect(testHost(srv.addr()), nil, plainUnseal, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	if err := sess.RequestPty("xterm-256color", 80, 24); err != nil {
		t.Fatal(err)
	}
	if err := sess.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 64)
	type readResult struct {
		n   int
		err error
	}
	done := make(chan readResult, 1)
	go func() {
		n, err := sess.Read(buf)
		done <- readResult{n, err}
	}()
	select {
	case r := <-done:
		if r.err != nil || !bytes.Contains(buf[:r.n], []byte("ping")) {
			t.Fatalf("echo read = %q err=%v", buf[:r.n], r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for echo")
	}
}

func TestSFTPRoundTrip(t *testing.T) {
	srv := startTestServer(t)
	sess, err := Connect(testHost(srv.addr()), nil, plainUnseal, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	sftpConn, err := sess.OpenSFTP()
	if err != nil {
		t.Fatal(err)
	}
	defer sftpConn.Close()

	cwd, err := sftpConn.client.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	// Write → read round-trip.
	if err := sftpConn.WriteFile(filepath.ToSlash(filepath.Join(cwd, "hello.txt")), []byte("kitodock sftp")); err != nil {
		t.Fatal(err)
	}
	data, err := sftpConn.ReadFile(filepath.ToSlash(filepath.Join(cwd, "hello.txt")))
	if err != nil || string(data) != "kitodock sftp" {
		t.Fatalf("ReadFile = %q err=%v", data, err)
	}

	// Upload from a reader, download to a buffer.
	if err := sftpConn.Upload(strings.NewReader("streamed"), filepath.ToSlash(filepath.Join(cwd, "up.txt"))); err != nil {
		t.Fatal(err)
	}
	var dl bytes.Buffer
	if err := sftpConn.Download(filepath.ToSlash(filepath.Join(cwd, "up.txt")), &dl); err != nil {
		t.Fatal(err)
	}
	if dl.String() != "streamed" {
		t.Fatalf("Download = %q", dl.String())
	}

	// Mkdir + List + Rename + Remove.
	if err := sftpConn.Mkdir(filepath.ToSlash(filepath.Join(cwd, "docs"))); err != nil {
		t.Fatal(err)
	}
	if err := sftpConn.Rename(filepath.ToSlash(filepath.Join(cwd, "hello.txt")), filepath.ToSlash(filepath.Join(cwd, "hi.txt"))); err != nil {
		t.Fatal(err)
	}
	entries, err := sftpConn.List(cwd)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = e.IsDir
	}
	check := func(name string, wantDir bool) {
		isDir, ok := names[name]
		if !ok || isDir != wantDir {
			t.Fatalf("entry %s: present=%v isDir=%v, want dir=%v (all=%v)", name, ok, isDir, wantDir, names)
		}
	}
	check("hi.txt", false)
	check("docs", true)
	check("up.txt", false)
	if _, ok := names["hello.txt"]; ok {
		t.Fatal("hello.txt still listed after rename")
	}
	if err := sftpConn.Remove(filepath.ToSlash(filepath.Join(cwd, "hi.txt"))); err != nil {
		t.Fatal(err)
	}
	if _, err := sftpConn.ReadFile(filepath.ToSlash(filepath.Join(cwd, "hi.txt"))); err == nil {
		t.Fatal("removed file still readable")
	}
}
