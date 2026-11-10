package gitworkspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runGit is a test helper executing git in dir.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// initRepo creates a repository with one commit on branch main.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@kitodock.dev")
	runGit(t, dir, "config", "user.name", "Kitodock Test")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644)
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial")
	return dir
}

func TestCurrentBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := initRepo(t)
	branch, err := CurrentBranch(repo)
	if err != nil || branch != "main" {
		t.Fatalf("CurrentBranch = %q err=%v", branch, err)
	}
	// A plain directory outside any repository errors.
	if _, err := CurrentBranch(t.TempDir()); err == nil {
		t.Fatal("non-repo accepted")
	}
}

func TestWorktreesListsMainAndLinked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := initRepo(t)
	wt := filepath.Join(t.TempDir(), "preview")
	runGit(t, repo, "worktree", "add", "-b", "preview", wt)

	trees, err := Worktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(trees) != 2 {
		t.Fatalf("worktrees = %+v", trees)
	}
	if !trees[0].Main || trees[0].Branch != "main" || trees[0].Head == "" {
		t.Fatalf("main entry = %+v", trees[0])
	}
	if trees[1].Path != wt || trees[1].Branch != "preview" || trees[1].Main {
		t.Fatalf("linked entry = %+v", trees[1])
	}

	// Worktrees resolves from inside a subdirectory too.
	sub := filepath.Join(repo, "sub")
	os.Mkdir(sub, 0o755)
	trees, err = Worktrees(sub)
	if err != nil || len(trees) != 2 {
		t.Fatalf("Worktrees(sub) = %+v err=%v", trees, err)
	}
}

func TestResolveMapsPathToWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := initRepo(t)
	wt := filepath.Join(t.TempDir(), "preview")
	runGit(t, repo, "worktree", "add", "-b", "preview", wt)

	main, err := Resolve(repo)
	if err != nil || !main.Main || main.Branch != "main" {
		t.Fatalf("Resolve(main) = %+v err=%v", main, err)
	}
	linked, err := Resolve(wt)
	if err != nil || linked.Main || linked.Branch != "preview" {
		t.Fatalf("Resolve(linked) = %+v err=%v", linked, err)
	}
	// Outside any repository.
	if _, err := Resolve(t.TempDir()); err == nil {
		t.Fatal("outside repo resolved")
	}
}

func TestIsRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if !IsRepository(initRepo(t)) {
		t.Fatal("repo not detected")
	}
	if IsRepository(t.TempDir()) {
		t.Fatal("plain dir detected as repo")
	}
	_ = strings.TrimSpace
}
