package vhost

// The hosts-file editor. Every line this app writes carries the marker
// comment, and only those lines are ever touched: the user's own hosts
// entries are untouchable, whichever tool rewrote the file in between.

import (
	"os"
	"strings"
)

// HostsMarker tags lines this app owns.
const HostsMarker = "# kitodock"

// Hosts owns one hosts file (the real one at
// C:\Windows\System32\drivers\etc\hosts; tests point at a temp file).
type Hosts struct{ Path string }

// AddEntry ensures "127.0.0.1 <host> # kitodock" exists, deduplicated.
func (h Hosts) AddEntry(host string) error {
	lines, err := h.read()
	if err != nil {
		return err
	}
	if hasHost(lines, host) {
		return nil
	}
	lines = append(lines, "127.0.0.1 "+host+" "+HostsMarker)
	return h.write(lines)
}

// RemoveEntry drops this app's entry for host (other aliases on a shared
// line survive).
func (h Hosts) RemoveEntry(host string) error {
	lines, err := h.read()
	if err != nil {
		return err
	}
	kept := make([]string, 0, len(lines))
	changed := false
	for _, line := range lines {
		if !strings.Contains(line, HostsMarker) || !hasHostInLine(line, host) {
			kept = append(kept, line)
			continue
		}
		changed = true
		fields := strings.Fields(line)
		var rest []string
		for _, field := range fields {
			if field == host || field == HostsMarker || field == "127.0.0.1" {
				continue
			}
			rest = append(rest, field)
		}
		if len(rest) > 0 {
			kept = append(kept, "127.0.0.1 "+strings.Join(rest, " ")+" "+HostsMarker)
		}
	}
	if !changed {
		return nil
	}
	return h.write(kept)
}

// Snapshot returns the file's current lines for a later Restore.
func (h Hosts) Snapshot() ([]string, error) { return h.read() }

// Restore rewrites the file to a snapshot.
func (h Hosts) SnapshotRestore(lines []string) error { return h.write(lines) }

func (h Hosts) read() ([]string, error) {
	data, err := os.ReadFile(h.Path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n"), nil
}

func (h Hosts) write(lines []string) error {
	return os.WriteFile(h.Path, []byte(strings.Join(lines, "\n")), 0o644)
}

func hasHost(lines []string, host string) bool {
	for _, line := range lines {
		if hasHostInLine(line, host) {
			return true
		}
	}
	return false
}

func hasHostInLine(line, host string) bool {
	if !strings.Contains(line, HostsMarker) {
		return false
	}
	for _, field := range strings.Fields(line) {
		if strings.EqualFold(field, host) {
			return true
		}
	}
	return false
}
