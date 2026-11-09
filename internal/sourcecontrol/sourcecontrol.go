// Package sourcecontrol is a validated facade over the git CLI for the
// in-app source control panel: status, stage, unstage, commit, push. It
// wraps every mutation in an operation with a stable identity, serializes
// per repository, and scrubs environment variables that could change git's
// behavior.
package sourcecontrol

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// FileState is the staged/unstaged state of one file.
type FileState struct {
	Path      string `json:"path"`
	Staged    bool   `json:"staged"`
	Unstaged  bool   `json:"unstaged"`
	Untracked bool   `json:"untracked"`
}

// Status is a repository snapshot.
type Status struct {
	Branch   string      `json:"branch"`
	Upstream string      `json:"upstream,omitempty"`
	Ahead    int         `json:"ahead"`
	Behind   int         `json:"behind"`
	Files    []FileState `json:"files"`
}

// Operation is a running or finished git operation.
type Operation struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`   // "stage" | "unstage" | "commit" | "push"
	Status  string    `json:"status"` // "running" | "done" | "error"
	Output  []string  `json:"output"`
	Error   string    `json:"error,omitempty"`
	Started time.Time `json:"started"`
}

// Service is the git facade for one workspace root.
type Service struct {
	mu       sync.Mutex
	root     string
	ops      map[string]*Operation
	opOrder  []string
	activeMu sync.Mutex // serializes git mutations
	onUpdate func()
}

// ErrRepositoryChanged is returned when the .git directory changed between
// the start and end of a mutation — the repository was swapped, rebased, or
// reset by something else mid-operation.
var ErrRepositoryChanged = errors.New("repository changed during operation")

// repoFingerprint computes a stable identity of the repository: the
// resolved HEAD commit hash. A swapped repo changes HEAD entirely; a
// rebased/reset repo changes HEAD to a different commit. Index changes
// (stage/unstage) do NOT change HEAD, so those operations pass cleanly.
func (s *Service) repoFingerprint() string {
	head, err := os.ReadFile(filepath.Join(s.root, ".git", "HEAD"))
	if err != nil {
		return "no-head"
	}
	content := strings.TrimSpace(string(head))
	if strings.HasPrefix(content, "ref: ") {
		refPath := filepath.Join(s.root, ".git", strings.TrimPrefix(content, "ref: "))
		commit, err := os.ReadFile(refPath)
		if err != nil {
			return content
		}
		return strings.TrimSpace(string(commit))
	}
	return content
}

// mutate wraps a git mutation with TOCTOU detection for operations that
// should not change HEAD (stage, unstage, push). Commit legitimately
// changes HEAD, so it skips the fingerprint check.
func (s *Service) mutate(ctx context.Context, kind string, fn func(ctx context.Context) (string, error)) error {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()

	checkFingerprint := kind != "commit"
	var before string
	if checkFingerprint {
		before = s.repoFingerprint()
	}
	op := s.startOp(kind)
	out, err := fn(ctx)
	if checkFingerprint && err == nil {
		if after := s.repoFingerprint(); before != after {
			err = ErrRepositoryChanged
		}
	}
	s.finishOp(op, err, out)
	return err
}

func New(root string) *Service {
	return &Service{root: root, ops: map[string]*Operation{}}
}

// Notify installs the change callback for the UI.
func (s *Service) Notify(onUpdate func()) { s.onUpdate = onUpdate }

func (s *Service) emit() {
	if s.onUpdate != nil {
		go s.onUpdate()
	}
}

// git runs a git command in the workspace root with a clean environment.
func (s *Service) git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = s.root
	cmd.Env = cleanGitEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// cleanGitEnv returns a git-safe environment: no GIT_* overrides, no
// credential helpers that could prompt, English output for parsing.
func cleanGitEnv() []string {
	var env []string
	for _, pair := range os.Environ() {
		if strings.HasPrefix(pair, "GIT_") {
			continue
		}
		env = append(env, pair)
	}
	env = append(env,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"LC_ALL=C", // deterministic output for parsing
	)
	return env
}

// Status reads the repository status using porcelain v2.
func (s *Service) Status(ctx context.Context) (Status, error) {
	var status Status

	// Branch and upstream info.
	out, err := s.git(ctx, "status", "--porcelain=v2", "--branch", "--untracked-files=all")
	if err != nil {
		return status, fmt.Errorf("git status: %w: %s", err, strings.TrimSpace(out))
	}

	fileMap := map[string]*FileState{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			status.Branch = strings.TrimPrefix(line, "# branch.head ")
		case strings.HasPrefix(line, "# branch.upstream "):
			status.Upstream = strings.TrimPrefix(line, "# branch.upstream ")
		case strings.HasPrefix(line, "# branch.ab "):
			// Format: "# branch.ab +A -B"
			ab := strings.TrimPrefix(line, "# branch.ab ")
			for _, part := range strings.Fields(ab) {
				if strings.HasPrefix(part, "+") {
					fmt.Sscanf(part, "+%d", &status.Ahead)
				} else if strings.HasPrefix(part, "-") {
					fmt.Sscanf(part, "-%d", &status.Behind)
				}
			}
		case strings.HasPrefix(line, "1 ") || strings.HasPrefix(line, "2 ") || strings.HasPrefix(line, "u "):
			// Changed tracked file: fields after the header contain XY path.
			// Porcelain v2 "1" lines: 1 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <path>
			fields := strings.Fields(line)
			if len(fields) < 8 {
				continue
			}
			xy := fields[1]
			path := strings.Join(fields[8:], " ")
			entry, ok := fileMap[path]
			if !ok {
				entry = &FileState{Path: path}
				fileMap[path] = entry
			}
			if len(xy) >= 1 && xy[0] != '.' {
				entry.Staged = true
			}
			if len(xy) >= 2 && xy[1] != '.' {
				entry.Unstaged = true
			}
		case strings.HasPrefix(line, "? "):
			// Untracked file: "? <path>"
			path := strings.TrimPrefix(line, "? ")
			fileMap[path] = &FileState{Path: path, Untracked: true}
		}
	}

	for _, entry := range fileMap {
		status.Files = append(status.Files, *entry)
	}
	// Sort by path.
	for i := 0; i < len(status.Files); i++ {
		for j := i + 1; j < len(status.Files); j++ {
			if status.Files[j].Path < status.Files[i].Path {
				status.Files[i], status.Files[j] = status.Files[j], status.Files[i]
			}
		}
	}
	return status, nil
}

// startOp begins a tracked operation.
func (s *Service) startOp(kind string) *Operation {
	s.mu.Lock()
	defer s.mu.Unlock()
	op := &Operation{
		ID:   fmt.Sprintf("%s-%d", kind, time.Now().UnixNano()),
		Kind: kind, Status: "running", Started: time.Now(),
	}
	s.ops[op.ID] = op
	s.opOrder = append(s.opOrder, op.ID)
	// Keep last 20 operations.
	if len(s.opOrder) > 20 {
		old := s.opOrder[0]
		delete(s.ops, old)
		s.opOrder = s.opOrder[1:]
	}
	s.emit()
	return op
}

func (s *Service) finishOp(op *Operation, err error, output string) {
	s.mu.Lock()
	op.Output = strings.Split(strings.TrimRight(output, "\n"), "\n")
	if err != nil {
		op.Status = "error"
		op.Error = err.Error()
	} else {
		op.Status = "done"
	}
	s.mu.Unlock()
	s.emit()
}

// Stage adds files to the index (literal pathspecs via stdin to avoid
// shell interpretation).
func (s *Service) Stage(ctx context.Context, paths []string) error {
	return s.mutate(ctx, "stage", func(ctx context.Context) (string, error) {
		return s.gitPathspec(ctx, "add", []string{"--"}, paths)
	})
}

// Unstage removes files from the index (restore --staged).
func (s *Service) Unstage(ctx context.Context, paths []string) error {
	return s.mutate(ctx, "unstage", func(ctx context.Context) (string, error) {
		args := append([]string{"restore", "--staged", "--"}, paths...)
		out, err := s.git(ctx, args...)
		if err != nil {
			args = append([]string{"reset", "HEAD", "--"}, paths...)
			out, err = s.git(ctx, args...)
		}
		return out, err
	})
}

// Commit commits the entire index.
func (s *Service) Commit(ctx context.Context, message string) error {
	return s.mutate(ctx, "commit", func(ctx context.Context) (string, error) {
		return s.git(ctx, "commit", "-m", message)
	})
}

// Push pushes the current branch to its upstream.
func (s *Service) Push(ctx context.Context) error {
	return s.mutate(ctx, "push", func(ctx context.Context) (string, error) {
		return s.git(ctx, "push")
	})
}

// gitPathspec runs a git command with paths passed via --pathspec-from-file
// to avoid shell quoting issues. The `--` separator is NOT used because
// --pathspec-from-file is an option, not a path.
func (s *Service) gitPathspec(ctx context.Context, command string, _ []string, paths []string) (string, error) {
	tmp, err := os.CreateTemp("", "git-paths-*.txt")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	for _, path := range paths {
		tmp.WriteString(path + "\n")
	}
	tmp.Close()

	args := []string{command, "--pathspec-from-file=" + tmp.Name()}
	return s.git(ctx, args...)
}

// GetOperation returns one operation's state.
func (s *Service) GetOperation(id string) (Operation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.ops[id]
	if !ok {
		return Operation{}, false
	}
	return *op, true
}

// Busy reports whether a mutation is in flight.
func (s *Service) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, op := range s.ops {
		if op.Status == "running" {
			return true
		}
	}
	return false
}

// RecentOperations returns the last few operations.
func (s *Service) RecentOperations() []Operation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Operation, 0, len(s.opOrder))
	for _, id := range s.opOrder {
		if op, ok := s.ops[id]; ok {
			out = append(out, *op)
		}
	}
	return out
}

// Close ends the service (stop any long-running ops via context).
func (s *Service) Close() {}

// Diff returns the working-tree diff for one file (unified format).
// Empty string means no changes.
func (s *Service) Diff(ctx context.Context, path string) (string, error) {
	out, err := s.git(ctx, "diff", "--", path)
	if err != nil {
		return "", fmt.Errorf("git diff %s: %w: %s", path, err, strings.TrimSpace(out))
	}
	return out, nil
}

// DiffStaged returns the staged (index vs HEAD) diff for one file.
func (s *Service) DiffStaged(ctx context.Context, path string) (string, error) {
	out, err := s.git(ctx, "diff", "--cached", "--", path)
	if err != nil {
		return "", fmt.Errorf("git diff --cached %s: %w: %s", path, err, strings.TrimSpace(out))
	}
	return out, nil
}

// ShowFile returns the HEAD version of a file (for diff comparison).
func (s *Service) ShowFile(ctx context.Context, path string) (string, error) {
	out, err := s.git(ctx, "show", "HEAD:"+path)
	if err != nil {
		// New file (not in HEAD yet).
		return "", nil
	}
	return out, nil
}
