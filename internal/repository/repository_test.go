package repository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"frigo/internal/git"
	"frigo/internal/testrepo"
)

func TestDiscoverUsesFrigoHistoryNames(t *testing.T) {
	root := testrepo.Init(t)
	repo, err := Discover(context.Background(), git.Client{Path: "git"}, root)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := repo.RegistryPath, filepath.Join(repo.FrigoDir, "registry.json"); got != want {
		t.Fatalf("RegistryPath = %q, want %q", got, want)
	}
	if got, want := repo.HistoryDir, filepath.Join(repo.FrigoDir, "history.git"); got != want {
		t.Fatalf("HistoryDir = %q, want %q", got, want)
	}
}

func TestDiscoverNormalRepository(t *testing.T) {
	t.Parallel()

	root := testrepo.Init(t)
	testrepo.Write(t, root, "README.md", "test\n")
	testrepo.CommitAll(t, root, "initial", "README.md")
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	repo, err := Discover(context.Background(), git.Client{Path: "git"}, nested)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	wantGitDir := filepath.Join(root, ".git")
	if repo.Root != root {
		t.Fatalf("Root = %q, want %q", repo.Root, root)
	}
	if repo.GitDir != wantGitDir {
		t.Fatalf("GitDir = %q, want %q", repo.GitDir, wantGitDir)
	}
	if repo.CommonDir != wantGitDir {
		t.Fatalf("CommonDir = %q, want %q", repo.CommonDir, wantGitDir)
	}
	if repo.FrigoDir != filepath.Join(wantGitDir, "frigo") {
		t.Fatalf("FrigoDir = %q", repo.FrigoDir)
	}
	if repo.HistoryDir != filepath.Join(wantGitDir, "frigo", "history.git") {
		t.Fatalf("HistoryDir = %q", repo.HistoryDir)
	}
	if repo.RegistryPath != filepath.Join(wantGitDir, "frigo", "registry.json") {
		t.Fatalf("RegistryPath = %q", repo.RegistryPath)
	}
	if repo.ExcludePath != filepath.Join(wantGitDir, "info", "exclude") {
		t.Fatalf("ExcludePath = %q", repo.ExcludePath)
	}
	if repo.AttributesPath != filepath.Join(repo.FrigoDir, "attributes") {
		t.Fatalf("AttributesPath = %q, want under FrigoDir", repo.AttributesPath)
	}
	if repo.HooksDir != filepath.Join(repo.FrigoDir, "hooks") {
		t.Fatalf("HooksDir = %q, want under FrigoDir", repo.HooksDir)
	}
	if repo.LinkedWorktree {
		t.Fatal("LinkedWorktree = true, want false")
	}
}

func TestDiscoverLinkedWorktreeUsesWorktreeLocalState(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}

	root := testrepo.Init(t)
	testrepo.Write(t, root, "README.md", "test\n")
	testrepo.CommitAll(t, root, "initial", "README.md")
	worktree := filepath.Join(root, "linked")
	testrepo.Run(t, root, "worktree", "add", "-q", "-b", "linked-branch", worktree)

	repo, err := Discover(context.Background(), git.Client{Path: "git"}, worktree)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	if !repo.LinkedWorktree {
		t.Fatal("LinkedWorktree = false, want true")
	}
	if repo.CommonDir != filepath.Join(root, ".git") {
		t.Fatalf("CommonDir = %q, want %q", repo.CommonDir, filepath.Join(root, ".git"))
	}
	if repo.GitDir == repo.CommonDir {
		t.Fatalf("GitDir = CommonDir = %q for linked worktree", repo.GitDir)
	}
	if repo.FrigoDir != filepath.Join(repo.GitDir, "frigo") {
		t.Fatalf("FrigoDir = %q, want under worktree GitDir", repo.FrigoDir)
	}
	if repo.AttributesPath != filepath.Join(repo.FrigoDir, "attributes") {
		t.Fatalf("AttributesPath = %q, want under worktree-local FrigoDir", repo.AttributesPath)
	}
	if repo.HooksDir != filepath.Join(repo.FrigoDir, "hooks") {
		t.Fatalf("HooksDir = %q, want under worktree-local FrigoDir", repo.HooksDir)
	}
	if repo.ExcludePath != filepath.Join(root, ".git", "info", "exclude") {
		t.Fatalf("ExcludePath = %q", repo.ExcludePath)
	}
}

func TestDiscoverRejectsNonRepository(t *testing.T) {
	t.Parallel()

	_, err := Discover(context.Background(), git.Client{Path: "git"}, t.TempDir())
	if err == nil {
		t.Fatal("Discover() error = nil, want error")
	}
}
