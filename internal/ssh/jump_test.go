package ssh

import (
	"strings"
	"testing"
)

// TestProxyJumpTwoLayers dials a target through a real bastion: both hops
// are in-process SSH servers, and the bastion forwards over direct-tcpip.
func TestProxyJumpTwoLayers(t *testing.T) {
	bastion := startTestServer(t)
	target := startTestServer(t)

	khBastion, err := LoadKnownHosts(t.TempDir() + "/kh_b.json")
	if err != nil {
		t.Fatal(err)
	}
	khTarget, err := LoadKnownHosts(t.TempDir() + "/kh_t.json")
	if err != nil {
		t.Fatal(err)
	}

	var stages []Stage
	targetHost := testHost(target.addr())
	targetHost.Name = "target"
	targetHost.JumpHosts = []string{"bastion"}

	sess, err := DialTarget(DialRequest{
		Host:      targetHost,
		Unseal:    plainUnseal,
		HostKeyCb: khTarget.Callback(),
		ResolveJump: func(name string) (*DialRequest, error) {
			if name != "bastion" {
				return nil, errUnknownJump
			}
			bastionHost := testHost(bastion.addr())
			bastionHost.Name = "bastion"
			return &DialRequest{
				Host: bastionHost, Unseal: plainUnseal, HostKeyCb: khBastion.Callback(),
			}, nil
		},
		OnStage: func(s Stage) { stages = append(stages, s) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	out, err := sess.Run("uptime", false)
	if err != nil || !strings.Contains(out, "ran: uptime") {
		t.Fatalf("Run through jump chain = %q err=%v", out, err)
	}

	// Both hops were recorded by their own TOFU stores.
	if len(khBastion.Fingerprints()) != 1 || len(khTarget.Fingerprints()) != 1 {
		t.Fatalf("TOFU records: bastion=%v target=%v",
			khBastion.Fingerprints(), khTarget.Fingerprints())
	}

	// The stage timeline covers the whole journey in order. Note auth
	// precedes hostkey per hop: the auth marker is emitted right before
	// the handshake starts (the library offers no auth hook), while the
	// hostkey marker fires from inside the handshake itself.
	var order []string
	for _, s := range stages {
		order = append(order, s.Name)
	}
	want := []string{StageResolve, StageDial, StageAuth, StageHostKey, StageJump, StageAuth, StageHostKey, StageReady}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("stage order = %v, want %v", order, want)
	}

	// The target's key algorithm is visible for trust review.
	if _, ok := khTarget.Algorithms()[normalizeHostKeyAddr(target.addr())]; !ok {
		t.Fatalf("Algorithms missing %s: %v", target.addr(), khTarget.Algorithms())
	}
}

// sentinel error for the unknown-jump case.
var errUnknownJump = &jumpError{"no such jump host"}

type jumpError struct{ msg string }

func (e *jumpError) Error() string { return e.msg }

// TestDialTargetRejectsUnknownJump ensures an unresolvable jump fails
// before any connection is attempted.
func TestDialTargetRejectsUnknownJump(t *testing.T) {
	target := startTestServer(t)
	host := testHost(target.addr())
	host.JumpHosts = []string{"ghost"}
	_, err := DialTarget(DialRequest{Host: host, Unseal: plainUnseal})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("unknown jump err = %v", err)
	}
}

// TestExpandProxyCommand checks %h/%p substitution.
func TestExpandProxyCommand(t *testing.T) {
	got := expandProxyCommand("ssh -W %h:%p corp-gw", "10.1.2.3", 2222)
	if got != "ssh -W 10.1.2.3:2222 corp-gw" {
		t.Fatalf("expand = %q", got)
	}
	// No tokens: untouched.
	if expandProxyCommand("nc corp 22", "h", 1) != "nc corp 22" {
		t.Fatal("template without tokens was modified")
	}
}
