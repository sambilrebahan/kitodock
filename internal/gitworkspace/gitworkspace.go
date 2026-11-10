// Package gitworkspace answers "where are this repository's checkouts and
// which one is on which branch" — the substrate preview-worktree routing
// and agent adoption build on. It shells out to git with a clean
// environment, mirroring internal/sourcecontrol's discipline, but stays
// read-only: no checkout, no fetch, no mutation of any kind.
package gitworkspace

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Worktree is one checkout of a repository.
type Worktree struct {
	// Path is the checkout's absolute path.
	Path string `json:"path"`
	// Head is the commit sha the checkout points at.
	Head string `json:"head"`
	// Branch is the checked-out branch name ("" when detached).
	Branch string `json:"branch,omitempty"`
	// Bare marks a bare repository entry.
	Bare bool `json:"bare,omitempty"`
	// Detached marks a detached-HEAD checkout.
	Detached bool `json:"detached,omitempty"`
	// Main marks the primary checkout (`git worktree list`'s first entry).
	Main bool `json:"main,omitempty"`
}

// gitOutput runs one git command in dir with a deterministic environment.
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Heredoc-free, locale-pinned, no inherited GIT_* surprises.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errOut.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return out.String(), nil
}

// IsRepository reports whether dir sits inside a git work tree.
func IsRepository(dir string) bool {
	_, err := gitOutput(dir, "rev-parse", "--git-dir")
	return err == nil
}

// Worktrees lists a repository's checkouts. dir may be any path inside
// the repository; the primary checkout is first and flagged Main.
func Worktrees(dir string) ([]Worktree, error) {
	out, err := gitOutput(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var trees []Worktree
	var current *Worktree
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "worktree "):
			// git prints forward slashes even on Windows.
			path := filepath.FromSlash(strings.TrimPrefix(line, "worktree "))
			trees = append(trees, Worktree{Path: path})
			current = &trees[len(trees)-1]
		case current == nil:
			continue // preamble junk
		case strings.HasPrefix(line, "HEAD "):
			current.Head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			// Porcelain emits the full ref; callers want the short name.
			current.Branch = strings.TrimPrefix(
				strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "bare":
			current.Bare = true
		case line == "detached":
			current.Detached = true
		}
	}
	if len(trees) > 0 {
		trees[0].Main = true
	}
	return trees, nil
}

// CurrentBranch returns the branch checked out in dir ("" when detached,
// error when dir is not a repository).
func CurrentBranch(dir string) (string, error) {
	out, err := gitOutput(dir, "branch", "--show-current")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Resolve maps a (possibly relative) path to the worktree it belongs to,
// walking up to the filesystem root when needed.
func Resolve(path string) (*Worktree, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// The worktree containing path is found by walking up until git
	// recognizes a repository, then matching against its worktree list.
	dir := abs
	for {
		if IsRepository(dir) {
			trees, err := Worktrees(dir)
			if err != nil {
				return nil, err
			}
			for i := range trees {
				if samePath(trees[i].Path, dir) {
					return &trees[i], nil
				}
			}
			return nil, fmt.Errorf("%s is inside a repository but is not a worktree root", path)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, fmt.Errorf("%s is not inside a git repository", path)
		}
		dir = parent
	}
}

// samePath compares paths case-insensitively on Windows semantics.
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if strings.EqualFold(a, b) {
		return true
	}
	return false
}
