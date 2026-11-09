package deployments

// Portable bundles: export deployment definitions (and their host
// addresses) as plain JSON for moving a setup between machines, and import
// them idempotently. Two rules are absolute: secrets never travel in a
// bundle (PATs and database passwords are stripped on export), and
// importing only writes records — no script or connection is ever executed.

import (
	"encoding/json"
	"fmt"
	"time"
)

// BundleVersion is the current bundle document layout.
const BundleVersion = 1

// Bundle is the portable document exchanged via export/import.
type Bundle struct {
	Version    int       `json:"version"`
	ExportedAt time.Time `json:"exportedAt"`
	// Hosts carries address/auth metadata only — never passwords, which
	// are sealed per Windows account and would be useless elsewhere.
	Hosts       []BundleHost       `json:"hosts,omitempty"`
	Deployments []BundleDeployment `json:"deployments,omitempty"`
}

// BundleHost is one host reference inside a bundle.
type BundleHost struct {
	Name     string   `json:"name"`
	Address  string   `json:"address"`
	Port     int      `json:"port"`
	User     string   `json:"user"`
	AuthMode string   `json:"authMode"`
	KeyName  string   `json:"keyName,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

// BundleDeployment is one deployment definition with its secrets stripped.
type BundleDeployment struct {
	Name       string            `json:"name"`
	HostName   string            `json:"hostName"`
	PresetID   string            `json:"presetId,omitempty"`
	PresetVars map[string]string `json:"presetVars,omitempty"`
	// Config is the plain (sanitized) ApplyConfig JSON.
	Config json.RawMessage `json:"config"`
}

// SanitizeConfig removes credential fields from a plain ApplyConfig JSON
// document, returning a copy; the input is left untouched.
func SanitizeConfig(config json.RawMessage) (json.RawMessage, error) {
	var doc map[string]any
	if err := json.Unmarshal(config, &doc); err != nil {
		return nil, fmt.Errorf("parse config for sanitizing: %w", err)
	}
	if doc["gitPat"] != nil {
		doc["gitPat"] = ""
	}
	if db, ok := doc["database"].(map[string]any); ok {
		if db["password"] != nil {
			db["password"] = ""
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ExportBundle assembles the deployment half of a bundle for the given IDs
// (or every deployment when ids is nil), sanitizing secrets. Hosts are
// attached by the caller, which owns the host registry.
func (s *Store) ExportBundle(ids []string) (Bundle, error) {
	bundle := Bundle{Version: BundleVersion, ExportedAt: time.Now()}
	for _, d := range s.List() {
		if ids != nil && !containsName(ids, d.ID) && !containsName(ids, d.Name) {
			continue
		}
		plain, err := s.protector.Unseal(d.Config)
		if err != nil {
			return bundle, fmt.Errorf("unseal %s: %w", d.Name, err)
		}
		var doc struct {
			HostName   string            `json:"hostName"`
			PresetID   string            `json:"presetId"`
			PresetVars map[string]string `json:"presetVars"`
			Config     json.RawMessage   `json:"config"`
		}
		if err := json.Unmarshal([]byte(plain), &doc); err != nil {
			return bundle, fmt.Errorf("parse %s: %w", d.Name, err)
		}
		clean, err := SanitizeConfig(doc.Config)
		if err != nil {
			return bundle, err
		}
		bundle.Deployments = append(bundle.Deployments, BundleDeployment{
			Name: d.Name, HostName: doc.HostName, PresetID: doc.PresetID,
			PresetVars: doc.PresetVars, Config: clean,
		})
	}
	return bundle, nil
}

// ImportResult reports what an import did — counters plus the host
// conflicts the caller resolved by mapping.
type ImportResult struct {
	HostsAdded         int `json:"hostsAdded"`
	HostsSkipped       int `json:"hostsSkipped"`
	DeploymentsAdded   int `json:"deploymentsAdded"`
	DeploymentsSkipped int `json:"deploymentsSkipped"`
	// HostConflicts maps a bundle host name to the existing host it was
	// resolved to (same name, different address).
	HostConflicts map[string]string `json:"hostConflicts,omitempty"`
}

// ImportDeployments stores every bundle deployment not already present by
// name, re-sealing its doc for this account. The second import of the same
// bundle is a no-op. It never connects anywhere.
func (s *Store) ImportDeployments(entries []BundleDeployment) (added, skipped int, err error) {
	for _, entry := range entries {
		if entry.Name == "" {
			return added, skipped, fmt.Errorf("bundle deployment without a name")
		}
		if s.nameExists(entry.Name) {
			skipped++
			continue
		}
		doc := map[string]any{
			"hostName":   entry.HostName,
			"presetId":   entry.PresetID,
			"presetVars": entry.PresetVars,
			"config":     entry.Config,
		}
		plain, merr := json.Marshal(doc)
		if merr != nil {
			return added, skipped, merr
		}
		sealed, serr := s.protector.Protect(string(plain))
		if serr != nil {
			return added, skipped, fmt.Errorf("seal %s: %w", entry.Name, serr)
		}
		if serr := s.Save(Deployment{ID: GenerateID(), Name: entry.Name, Config: sealed}); serr != nil {
			return added, skipped, serr
		}
		added++
	}
	return added, skipped, nil
}

// nameExists reports whether a deployment with that name is stored.
func (s *Store) nameExists(name string) bool {
	for _, d := range s.List() {
		if d.Name == name {
			return true
		}
	}
	return false
}

func containsName(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
