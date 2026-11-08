package sourcecontrol

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitRepo(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git not available or failed: %v: %s", err, out)
		}
	}
	run("init")
	run("config", "user.name", "T")
	run("config", "user.email", "t@t")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0o644)
	run("add", ".")
	run("commit", "-m", "init")

	return New(dir)
}

func TestStatusReadsBranchAndFiles(t *testing.T) {
	s := gitRepo(t)
	status, err := s.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Branch == "" {
		t.Fatal("no branch")
	}
	if len(status.Files) != 0 {
		t.Fatalf("clean repo should have no files: %+v", status.Files)
	}
}

func TestStatusDetectsChanges(t *testing.T) {
	s := gitRepo(t)

	// Modify a tracked file.
	os.WriteFile(filepath.Join(s.root, "README.md"), []byte("# changed\n"), 0o644)
	// Create an untracked file.
	os.WriteFile(filepath.Join(s.root, "new.txt"), []byte("x"), 0o644)

	status, err := s.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Files) != 2 {
		t.Fatalf("files = %d, want 2: %+v", len(status.Files), status.Files)
	}
	for _, f := range status.Files {
		if f.Path == "README.md" {
			if !f.Unstaged {
				t.Error("README.md should be unstaged")
			}
		}
		if f.Path == "new.txt" {
			if !f.Untracked {
				t.Error("new.txt should be untracked")
			}
		}
	}
}

func TestStageUnstageCommit(t *testing.T) {
	s := gitRepo(t)
	ctx := context.Background()

	// Create a change.
	os.WriteFile(filepath.Join(s.root, "feature.txt"), []byte("new feature\n"), 0o644)

	// Stage it.
	if err := s.Stage(ctx, []string{"feature.txt"}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	status, _ := s.Status(ctx)
	for _, f := range status.Files {
		if f.Path == "feature.txt" && !f.Staged {
			t.Error("feature.txt not staged after Stage")
		}
	}

	// Commit.
	if err := s.Commit(ctx, "add feature"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	status, _ = s.Status(ctx)
	if len(status.Files) != 0 {
		t.Fatalf("after commit files = %+v", status.Files)
	}
}

func TestUnstage(t *testing.T) {
	s := gitRepo(t)
	ctx := context.Background()

	os.WriteFile(filepath.Join(s.root, "x.txt"), []byte("x\n"), 0o644)
	s.Stage(ctx, []string{"x.txt"})

	if err := s.Unstage(ctx, []string{"x.txt"}); err != nil {
		t.Fatalf("unstage: %v", err)
	}
	status, _ := s.Status(ctx)
	for _, f := range status.Files {
		if f.Path == "x.txt" && f.Staged {
			t.Error("x.txt still staged after Unstage")
		}
	}
}

func TestOperationsTracked(t *testing.T) {
	s := gitRepo(t)
	ctx := context.Background()

	os.WriteFile(filepath.Join(s.root, "op.txt"), []byte("op\n"), 0o644)
	s.Stage(ctx, []string{"op.txt"})

	ops := s.RecentOperations()
	if len(ops) == 0 {
		t.Fatal("no operations recorded")
	}
	if ops[0].Kind != "stage" {
		t.Fatalf("last op = %s, want stage", ops[0].Kind)
	}
	if ops[0].Status != "done" {
		t.Fatalf("op status = %s, want done", ops[0].Status)
	}
}

func TestCleanGitEnv(t *testing.T) {
	env := cleanGitEnv()
	for _, pair := range env {
		if strings.HasPrefix(pair, "GIT_AUTHOR") || strings.HasPrefix(pair, "GIT_COMMITTER") {
			if !strings.Contains(pair, "=T") && !strings.Contains(pair, "=t@") {
				t.Errorf("GIT_ var not stripped: %s", pair)
			}
		}
		if strings.HasPrefix(pair, "GIT_WORK_TREE") || strings.HasPrefix(pair, "GIT_INDEX_FILE") {
			t.Errorf("dangerous GIT_ var present: %s", pair)
		}
	}
	found := false
	for _, pair := range env {
		if pair == "GIT_TERMINAL_PROMPT=0" {
			found = true
		}
	}
	if !found {
		t.Error("GIT_TERMINAL_PROMPT=0 not set")
	}
}
