//go:build windows

// Package netports answers "who is listening on that port?" and, carefully,
// "please make them stop". Dev tooling on Windows routinely re-parents child
// processes, so killing by the PID the socket table reports needs a
// tree-kill; and a port may belong to an app that has nothing to do with us,
// which is why every kill goes through an executable-name allowlist.
package netports

import (
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Listener is one listening TCP socket.
type Listener struct {
	Port uint16
	PID  uint32
	Addr string // "127.0.0.1", "[::1]", "0.0.0.0", "[::]"
}

var (
	iphlpapi           = syscall.NewLazyDLL("iphlpapi.dll")
	procGetExtendedTCP = iphlpapi.NewProc("GetExtendedTcpTable")
)

// TCP_TABLE_CLASS values: the OWNER_PID variants carry the owning process;
// BASIC_* (0–2) use a shorter row without a PID and would garble parsing.
const (
	tcpTableOwnerPIDListener = 3
	tcpStateListen           = 2
	afInet                   = 2
	afInet6                  = 23
)

// row4 mirrors MIB_TCPROW_OWNER_PID.
type row4 struct {
	State      uint32
	LocalAddr  uint32
	LocalPort  uint32
	RemoteAddr uint32
	RemotePort uint32
	OwningPID  uint32
}

// row6 mirrors MIB_TCP6ROW_OWNER_PID.
type row6 struct {
	LocalAddr     [16]byte
	LocalScopeID  uint32
	LocalPort     uint32
	State         uint32
	RemoteAddr    [16]byte
	RemoteScopeID uint32
	RemotePort    uint32
	OwningPID     uint32
}

// Listeners returns every TCP socket in the LISTEN state, IPv4 and IPv6.
// The table is read straight from iphlpapi — no netstat shell-out, no
// parsing English output.
func Listeners() ([]Listener, error) {
	var out []Listener
	if found, err := queryTable(afInet, &out); err != nil || !found {
		return nil, err
	}
	if found, err := queryTable(afInet6, &out); err != nil || !found {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].PID < out[j].PID
	})
	return out, nil
}

// queryTable fetches one address family's table; the bool reports whether
// the call succeeded (a missing IPv6 stack is tolerated by skipping).
func queryTable(family uint32, into *[]Listener) (bool, error) {
	rowSize := uintptr(unsafe.Sizeof(row4{}))
	if family == afInet6 {
		rowSize = uintptr(unsafe.Sizeof(row6{}))
	}
	size := uint32(0)
	// GetExtendedTcpTable(pTable, &size, bOrder, ulAf, tableClass): the
	// sizing call leaves the table pointer NULL and expects the size back.
	procGetExtendedTCP.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(family), uintptr(tcpTableOwnerPIDListener))
	if size == 0 {
		return false, nil
	}
	buffer := make([]byte, size)
	// The size argument is an in/out pointer on both calls: in with the
	// buffer capacity, out with the bytes actually needed.
	rc, _, _ := procGetExtendedTCP.Call(
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(unsafe.Pointer(&size)),
		0,
		uintptr(family),
		uintptr(tcpTableOwnerPIDListener),
	)
	if rc == uintptr(windows.ERROR_INSUFFICIENT_BUFFER) {
		return false, nil // table grew between the two calls; caller retries
	}
	if rc != 0 {
		return false, fmt.Errorf("GetExtendedTcpTable(af=%d): win32 error %d", family, rc)
	}
	count := *(*uint32)(unsafe.Pointer(&buffer[0]))
	rows := buffer[unsafe.Sizeof(uint32(0)):]
	if family == afInet {
		for i := uint32(0); i < count; i++ {
			row := (*row4)(unsafe.Pointer(&rows[uintptr(i)*rowSize]))
			if row.State != tcpStateListen {
				continue
			}
			*into = append(*into, Listener{
				Port: networkPort(row.LocalPort),
				PID:  row.OwningPID,
				Addr: ipv4(row.LocalAddr).String(),
			})
		}
	} else {
		for i := uint32(0); i < count; i++ {
			row := (*row6)(unsafe.Pointer(&rows[uintptr(i)*rowSize]))
			if row.State != tcpStateListen {
				continue
			}
			*into = append(*into, Listener{
				Port: networkPort(row.LocalPort),
				PID:  row.OwningPID,
				Addr: "[" + ipv6(row.LocalAddr).String() + "]",
			})
		}
	}
	return true, nil
}

// networkPort un-swaps the port's network byte order in the table rows.
func networkPort(raw uint32) uint16 {
	return uint16(raw&0xff)<<8 | uint16(raw>>8)
}

func ipv4(raw uint32) net.IP {
	return net.IP{byte(raw), byte(raw >> 8), byte(raw >> 16), byte(raw >> 24)}
}

func ipv6(raw [16]byte) net.IP {
	return net.IP(raw[:])
}

// ProcessName is the owning executable's base name ("node.exe"), or "" when
// the process already exited.
func ProcessName(pid uint32) string {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return ""
	}
	for {
		if entry.ProcessID == pid {
			return strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			return ""
		}
	}
}

// KillPort terminates everything listening on port, restricted to the named
// executables. An empty allowlist refuses to kill anything — the safe
// default for a button in the UI.
func KillPort(port uint16, allowExes ...string) error {
	if len(allowExes) == 0 {
		return fmt.Errorf("port %d: refusing to kill without an executable allowlist", port)
	}
	allowed := map[string]bool{}
	for _, name := range allowExes {
		allowed[strings.ToLower(name)] = true
	}

	listeners, err := Listeners()
	if err != nil {
		return err
	}
	killed := 0
	for _, l := range listeners {
		if l.Port != port {
			continue
		}
		name := ProcessName(l.PID)
		if !allowed[name] {
			continue
		}
		if err := killTree(l.PID); err != nil {
			return fmt.Errorf("port %d (pid %d, %s): %w", port, l.PID, name, err)
		}
		killed++
	}
	if killed == 0 {
		return fmt.Errorf("port %d: no listener belongs to %v", port, allowExes)
	}
	return nil
}

// killTree ends a process and everything it spawned, via taskkill's
// tree-kill; a "not found" verdict is success — the goal is the port, and
// the holder is already gone.
func killTree(pid uint32) error {
	cmd := exec.Command("taskkill", "/F", "/T", "/PID", fmt.Sprint(pid))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil && !strings.Contains(strings.ToLower(string(out)), "not found") {
		return fmt.Errorf("taskkill: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// KillImage ends every process running the named executable. Used only for
// binaries this app owns, to clean up strays a crashed run left behind.
func KillImage(exeBase string) error {
	cmd := exec.Command("taskkill", "/F", "/T", "/IM", exeBase)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil && !strings.Contains(strings.ToLower(string(out)), "not found") {
		return fmt.Errorf("taskkill /IM %s: %v: %s", exeBase, err, strings.TrimSpace(string(out)))
	}
	return nil
}
