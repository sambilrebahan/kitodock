// Package deployments stores deployment records and run history locally.
// Records are sealed with DPAPI (through an injected Protector) so the
// on-disk index is opaque without the running Windows account.
package deployments

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Protector seals and unseals payload bytes for the current OS user.
type Protector interface {
	Protect(plain string) (string, error)
	Unseal(sealed string) (string, error)
}

// Kind identifies what a run did.
type Kind string

const (
	KindDetect  Kind = "detect"
	KindApply   Kind = "apply"
	KindDeploy  Kind = "deploy"
	KindDestroy Kind = "destroy"
)

// Deployment is one stored deployment record.
type Deployment struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	// Config is the DPAPI-sealed ApplyConfig JSON.
	Config string       `json:"config"`
	Runs   []RunSummary `json:"runs,omitempty"`
}

// RunSummary is one completed (or interrupted) run.
type RunSummary struct {
	ID        string    `json:"id"`
	Kind      Kind      `json:"kind"`
	Status    string    `json:"status"` // "success" | "error" | "interrupted"
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt"`
	Error     string    `json:"error,omitempty"`
}

// RunLog is the full output of one run (sealed separately).
type RunLog struct {
	ID           string `json:"id"`
	DeploymentID string `json:"deploymentId"`
	// Log is the DPAPI-sealed output.
	Log string `json:"log"`
}

// MaxRuns caps the retained run summaries per deployment.
const MaxRuns = 100

// MaxLogBytes caps a single run's output before sealing.
const MaxLogBytes = 4 << 20 // 4 MiB

// Store owns the deployment index and run logs.
type Store struct {
	mu          sync.Mutex
	path        string
	protector   Protector
	deployments map[string]*Deployment
}

type diskIndex struct {
	Version int          `json:"version"`
	Records []diskRecord `json:"records"`
}

type diskRecord struct {
	ID         string     `json:"id"`
	Deployment Deployment `json:"deployment"`
}

// OpenStore loads (or creates) the store at path.
func OpenStore(path string, protector Protector) (*Store, error) {
	s := &Store{
		path:        path,
		protector:   protector,
		deployments: map[string]*Deployment{},
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var doc diskIndex
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse deployment index: %w", err)
	}
	for _, rec := range doc.Records {
		d := rec.Deployment
		s.deployments[d.ID] = &d
	}
	return s, nil
}

// List returns all deployments, newest first.
func (s *Store) List() []Deployment {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Deployment, 0, len(s.deployments))
	for _, d := range s.deployments {
		out = append(out, *d)
	}
	// Sort by UpdatedAt descending.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].UpdatedAt.After(out[i].UpdatedAt) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// Get returns one deployment by ID.
func (s *Store) Get(id string) (Deployment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.deployments[id]
	if !ok {
		return Deployment{}, false
	}
	return *d, true
}

// Save creates or updates a deployment record.
func (s *Store) Save(d Deployment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d.UpdatedAt = time.Now()
	s.deployments[d.ID] = &d
	return s.persist()
}

// Delete removes a deployment and its run logs.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.deployments, id)
	return s.persist()
}

// StartRun records the beginning of a run.
func (s *Store) StartRun(deploymentID, runID string, kind Kind) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.deployments[deploymentID]
	if !ok {
		return fmt.Errorf("deployment %s not found", deploymentID)
	}
	// Any previous run still marked as "running" was interrupted by a crash.
	for i := range d.Runs {
		if d.Runs[i].Status == "running" {
			d.Runs[i].Status = "interrupted"
			d.Runs[i].EndedAt = time.Now()
		}
	}
	d.Runs = append([]RunSummary{{
		ID: runID, Kind: kind, Status: "running", StartedAt: time.Now(),
	}}, d.Runs...)
	if len(d.Runs) > MaxRuns {
		d.Runs = d.Runs[:MaxRuns]
	}
	return s.persist()
}

// FinishRun records the outcome of a run.
func (s *Store) FinishRun(deploymentID, runID string, status, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.deployments[deploymentID]
	if !ok {
		return fmt.Errorf("deployment %s not found", deploymentID)
	}
	for i := range d.Runs {
		if d.Runs[i].ID == runID {
			d.Runs[i].Status = status
			d.Runs[i].EndedAt = time.Now()
			d.Runs[i].Error = errMsg
			break
		}
	}
	return s.persist()
}

// SaveRunLog seals and stores the output of a run.
func (s *Store) SaveRunLog(deploymentID, runID, output string) error {
	if len(output) > MaxLogBytes {
		output = output[:MaxLogBytes]
	}
	sealed, err := s.protector.Protect(output)
	if err != nil {
		return fmt.Errorf("seal run log: %w", err)
	}
	log := RunLog{ID: runID, DeploymentID: deploymentID, Log: sealed}
	data, err := json.MarshalIndent(log, "", "  ")
	if err != nil {
		return err
	}
	logPath := filepath.Join(filepath.Dir(s.path), "runs", runID+".json")
	os.MkdirAll(filepath.Dir(logPath), 0o755)
	return os.WriteFile(logPath, append(data, '\n'), 0o600)
}

// ReadRunLog unseals and returns a run's output.
func (s *Store) ReadRunLog(runID string) (string, error) {
	logPath := filepath.Join(filepath.Dir(s.path), "runs", runID+".json")
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var log RunLog
	if err := json.Unmarshal(data, &log); err != nil {
		return "", err
	}
	return s.protector.Unseal(log.Log)
}

// persist writes the index to disk atomically.
func (s *Store) persist() error {
	doc := diskIndex{Version: 1}
	for _, d := range s.deployments {
		doc.Records = append(doc.Records, diskRecord{ID: d.ID, Deployment: *d})
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// ErrNotFound is returned when a deployment or run doesn't exist.
var ErrNotFound = errors.New("not found")

// GenerateID returns a simple unique ID for a new deployment or run.
func GenerateID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
