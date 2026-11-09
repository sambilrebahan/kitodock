package ssh

// Host registry: persistence for saved SSH targets. Passwords are stored
// DPAPI-sealed by the caller (the controller seals before Upsert), so this
// file only ever sees ciphertext.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// HostStore persists the host list as a JSON document.
type HostStore struct {
	path  string
	hosts []Host
}

// LoadHostStore reads hosts.json, returning an empty store when absent.
func LoadHostStore(path string) (*HostStore, error) {
	s := &HostStore{path: path, hosts: []Host{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read hosts: %w", err)
	}
	if err := json.Unmarshal(data, &s.hosts); err != nil {
		return nil, fmt.Errorf("parse hosts: %w", err)
	}
	return s, nil
}

// List returns hosts sorted by name.
func (s *HostStore) List() []Host {
	out := make([]Host, len(s.hosts))
	copy(out, s.hosts)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Find returns the host with the given name.
func (s *HostStore) Find(name string) (Host, bool) {
	for _, h := range s.hosts {
		if h.Name == name {
			return h, true
		}
	}
	return Host{}, false
}

// Upsert inserts or replaces a host by name.
func (s *HostStore) Upsert(h Host) {
	for i, existing := range s.hosts {
		if existing.Name == h.Name {
			s.hosts[i] = h
			return
		}
	}
	s.hosts = append(s.hosts, h)
}

// Remove deletes a host by name, reporting whether it existed.
func (s *HostStore) Remove(name string) bool {
	for i, existing := range s.hosts {
		if existing.Name == name {
			s.hosts = append(s.hosts[:i], s.hosts[i+1:]...)
			return true
		}
	}
	return false
}

// Save writes the store back to disk atomically via a temp+rename.
func (s *HostStore) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.hosts, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
