package ssh

// Jump routing: reach a target through a chain of bastion hosts
// (ProxyJump) or a local helper process that provides the raw connection
// (ProxyCommand, with %h/%p expansion). The chain is dialed hop by hop —
// each hop is a full SSH handshake over the previous hop's direct-tcpip
// channel — so host-key verification (TOFU) applies to every hop equally.

import (
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// maxHops caps the total chain length (jumps + target); longer chains are
// almost certainly a configuration mistake.
const maxHops = 5

// Stage names reported by a stage-tracked dial.
const (
	StageResolve = "resolve" // jump chain assembled
	StageDial    = "dial"    // underlying TCP / proxy-command connection
	StageJump    = "jump"    // direct-tcpip through a bastion
	StageHostKey = "hostkey" // server key verified / recorded (TOFU)
	StageAuth    = "auth"    // credentials about to be presented
	StageReady   = "ready"   // session usable
)

// Stage is one progress marker while establishing a connection.
type Stage struct {
	Name string `json:"name"`
	// Detail names the hop or address the stage applies to.
	Detail string `json:"detail,omitempty"`
	// Milliseconds elapsed since the dial began.
	Milliseconds int64 `json:"ms"`
}

// DialRequest bundles everything needed to reach one host: credentials, the
// host-key policy, and how to resolve jump hosts by name.
type DialRequest struct {
	Host      Host
	KeyData   []byte
	Unseal    func(string) (string, error)
	HostKeyCb ssh.HostKeyCallback
	// ResolveJump turns one entry of Host.JumpHosts into its dial request
	// (address + own credentials). Nil means jumps cannot be resolved.
	ResolveJump func(name string) (*DialRequest, error)
	// OnStage, when set, receives progress markers in dial order.
	OnStage func(Stage)
}

// DialTarget connects to req.Host — through its jump chain and proxy
// command when configured — and returns the live session.
func DialTarget(req DialRequest) (*Session, error) {
	start := time.Now()
	stage := func(name, detail string) {
		if req.OnStage != nil {
			req.OnStage(Stage{
				Name: name, Detail: detail,
				Milliseconds: time.Since(start).Milliseconds(),
			})
		}
	}

	chain, err := assembleChain(req)
	if err != nil {
		return nil, err
	}
	stage(StageResolve, fmt.Sprintf("%d hop(s)", len(chain)))

	// Underlying connection to the first hop: proxy command or plain TCP.
	var conn net.Conn
	first := chain[0]
	if first.Host.ProxyCommand != "" {
		conn, err = startProxyCommand(first.Host)
		if err != nil {
			return nil, fmt.Errorf("proxy command: %w", err)
		}
		stage(StageDial, "proxy: "+first.Host.ProxyCommand)
	} else {
		conn, err = net.DialTimeout("tcp", addrOf(first.Host), 10*time.Second)
		if err != nil {
			return nil, fmt.Errorf("dial %s: %w", addrOf(first.Host), err)
		}
		stage(StageDial, addrOf(first.Host))
	}

	// Handshake hop by hop; later hops ride direct-tcpip channels.
	hops := make([]*ssh.Client, 0, len(chain))
	fail := func(err error) (*Session, error) {
		if conn != nil {
			conn.Close()
		}
		for _, c := range hops {
			c.Close()
		}
		return nil, err
	}

	for i, hop := range chain {
		if i > 0 {
			prev := hops[i-1]
			conn, err = prev.Dial("tcp", addrOf(hop.Host))
			if err != nil {
				return fail(fmt.Errorf("jump %s through %s: %w",
					addrOf(hop.Host), addrOf(chain[i-1].Host), err))
			}
			stage(StageJump, addrOf(hop.Host)+" via "+addrOf(chain[i-1].Host))
		}

		config, err := clientConfigFor(hop)
		if err != nil {
			return fail(err)
		}
		verify := hop.HostKeyCb
		if verify == nil {
			verify = ssh.InsecureIgnoreHostKey()
		}
		config.HostKeyCallback = tracedHostKey(verify, hop, stage)

		stage(StageAuth, hop.Host.User+"@"+addrOf(hop.Host))
		sshConn, chans, reqs, err := ssh.NewClientConn(conn, addrOf(hop.Host), config)
		if err != nil {
			return fail(fmt.Errorf("handshake %s: %w", addrOf(hop.Host), err))
		}
		hops = append(hops, ssh.NewClient(sshConn, chans, reqs))
	}

	stage(StageReady, addrOf(req.Host))
	sess := &Session{client: hops[len(hops)-1], host: req.Host}
	if len(hops) > 1 {
		sess.chain = hops[:len(hops)-1]
	}
	return sess, nil
}

// assembleChain expands the target's JumpHosts into the ordered hop list
// [jump1 … jumpN, target], mirroring `ssh -J a,b target` semantics: the
// last jump is the direct parent of the target.
func assembleChain(req DialRequest) ([]DialRequest, error) {
	chain := make([]DialRequest, 0, len(req.Host.JumpHosts)+1)
	for _, name := range req.Host.JumpHosts {
		if req.ResolveJump == nil {
			return nil, fmt.Errorf("host %q uses jump %q but jumps cannot be resolved here",
				req.Host.Name, name)
		}
		jump, err := req.ResolveJump(name)
		if err != nil {
			return nil, fmt.Errorf("resolve jump %q: %w", name, err)
		}
		// A jump's own jump list is ignored: the chain is declared flat on
		// the target, exactly like OpenSSH -J.
		jump.Host.JumpHosts = nil
		chain = append(chain, *jump)
	}
	chain = append(chain, req)
	if len(chain) > maxHops {
		return nil, fmt.Errorf("jump chain of %d hops exceeds the limit of %d", len(chain), maxHops)
	}
	return chain, nil
}

// clientConfigFor builds the auth section shared by every dial path.
func clientConfigFor(req DialRequest) (*ssh.ClientConfig, error) {
	config := &ssh.ClientConfig{
		User:            req.Host.User,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // replaced by the caller
		Timeout:         10 * time.Second,
	}
	switch req.Host.AuthMode {
	case "key":
		if len(req.KeyData) == 0 {
			return nil, fmt.Errorf("host %q authenticates by key but none was supplied", req.Host.Name)
		}
		signer, err := ssh.ParsePrivateKey(req.KeyData)
		if err != nil {
			return nil, fmt.Errorf("parse key: %w", err)
		}
		config.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	case "password":
		if req.Unseal == nil {
			return nil, fmt.Errorf("host %q authenticates by password but no unsealer was supplied", req.Host.Name)
		}
		password, err := req.Unseal(req.Host.Password)
		if err != nil {
			return nil, fmt.Errorf("unseal password: %w", err)
		}
		config.Auth = []ssh.AuthMethod{ssh.Password(password)}
	default:
		return nil, fmt.Errorf("unsupported auth mode %q", req.Host.AuthMode)
	}
	return config, nil
}

// tracedHostKey wraps a verifier so the hostkey stage is reported per hop.
func tracedHostKey(inner ssh.HostKeyCallback, hop DialRequest, stage func(name, detail string)) ssh.HostKeyCallback {
	return func(hostArg string, remote net.Addr, key ssh.PublicKey) error {
		stage(StageHostKey, hostArg)
		return inner(hostArg, remote, key)
	}
}

// addrOf renders host:port for dialing.
func addrOf(h Host) string {
	port := h.Port
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(h.Address, fmt.Sprintf("%d", port))
}

// --- Proxy command ---------------------------------------------------------

// expandProxyCommand substitutes %h (target host) and %p (target port) in a
// proxy command template. Tokens are whitespace-split; quoting is not
// supported — keep commands single-token-per-argument.
func expandProxyCommand(template, host string, port int) string {
	out := strings.ReplaceAll(template, "%h", host)
	return strings.ReplaceAll(out, "%p", fmt.Sprintf("%d", port))
}

// startProxyCommand launches the helper process for the first hop and
// returns its stdin/stdout pair as a connection.
func startProxyCommand(h Host) (net.Conn, error) {
	fields := strings.Fields(expandProxyCommand(h.ProxyCommand, h.Address, h.Port))
	if len(fields) == 0 {
		return nil, fmt.Errorf("empty proxy command")
	}
	cmd := exec.Command(fields[0], fields[1:]...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", fields[0], err)
	}
	return &proxyCommandConn{cmd: cmd, stdin: stdin, stdout: stdout}, nil
}

// proxyCommandConn adapts a child process's pipes to net.Conn so the SSH
// handshake can run over them. Deadline methods are no-ops: pipes have no
// deadlines.
type proxyCommandConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

func (c *proxyCommandConn) Read(p []byte) (int, error)  { return c.stdout.Read(p) }
func (c *proxyCommandConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }

func (c *proxyCommandConn) Close() error {
	c.stdin.Close()
	c.stdout.Close()
	if c.cmd.Process != nil {
		return c.cmd.Process.Kill()
	}
	return nil
}

func (c *proxyCommandConn) LocalAddr() net.Addr              { return pipeAddr{} }
func (c *proxyCommandConn) RemoteAddr() net.Addr             { return pipeAddr{} }
func (c *proxyCommandConn) SetDeadline(time.Time) error      { return nil }
func (c *proxyCommandConn) SetReadDeadline(time.Time) error  { return nil }
func (c *proxyCommandConn) SetWriteDeadline(time.Time) error { return nil }

// pipeAddr is the placeholder address reported by proxy command connections.
type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "proxy-command" }
