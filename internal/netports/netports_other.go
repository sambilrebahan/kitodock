//go:build !windows

package netports

import "errors"

type Listener struct {
	Port uint16
	PID  uint32
	Addr string
}

// Kitodock is Windows-first; other platforms get honest stubs instead of a
// half-ported snapshot of Win32 behavior.
var ErrUnsupported = errors.New("netports is Windows-only")

func Listeners() ([]Listener, error) { return nil, ErrUnsupported }

func ProcessName(uint32) string { return "" }

func KillPort(uint16, ...string) error { return ErrUnsupported }

func KillImage(string) error { return ErrUnsupported }
