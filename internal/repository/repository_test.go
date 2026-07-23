package repository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/roie/frigo/internal/git"
	"github.com/roie/frigo/internal/testrepo"
)

func TestDiscoverUsesFrigoHistoryNames(t *testing.T) {
	root := testrepo.Init(t)
	repo, err := Discover(context.Background(), git.Client{Path: "git"}, root)
	if err != nil {
		t.Fatal(err)
	}

	wantGitDir := filepath.Join(root, ".git")
	if repo.CommonFrigoDir != filepath.Join(wantGitDir, "frigo") {
		t.Fatalf("CommonFrigoDir = %q", repo.CommonFrigoDir)
	}
	if repo.LinkedStoresDir != filepath.Join(wantGitDir, "worktrees") {
		t.Fatalf("LinkedStoresDir = %q", repo.LinkedStoresDir)
	}
	if repo.WorktreeIDPath != filepath.Join(wantGitDir, "frigo-id") {
		t.Fatalf("WorktreeIDPath = %q", repo.WorktreeIDPath)
	}
	if got, want := repo.RegistryPath, filepath.Join(repo.FrigoDir, "registry.json"); got != want {
		t.Fatalf("RegistryPath = %q, want %q", got, want)
	}
	if got, want := repo.HistoryDir, filepath.Join(repo.FrigoDir, "history.git"); got != want {
		t.Fatalf("HistoryDir = %q, want %q", got, want)
	}
	if repo.PrivateAttributesPath != filepath.Join(repo.HistoryDir, "info", "attributes") {
		t.Fatalf("PrivateAttributesPath = %q", repo.PrivateAttributesPath)
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
	if repo.CommonFrigoDir != filepath.Join(wantGitDir, "frigo") {
		t.Fatalf("CommonFrigoDir = %q", repo.CommonFrigoDir)
	}
	if repo.LinkedStoresDir != filepath.Join(wantGitDir, "worktrees") {
		t.Fatalf("LinkedStoresDir = %q", repo.LinkedStoresDir)
	}
	if repo.WorktreeIDPath != filepath.Join(wantGitDir, "frigo-id") {
		t.Fatalf("WorktreeIDPath = %q", repo.WorktreeIDPath)
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
	if repo.OperationLockPath != filepath.Join(wantGitDir, "frigo.lock") {
		t.Fatalf("OperationLockPath = %q", repo.OperationLockPath)
	}
	if repo.AttributesPath != filepath.Join(repo.FrigoDir, "attributes") {
		t.Fatalf("AttributesPath = %q, want under FrigoDir", repo.AttributesPath)
	}
	if repo.PrivateAttributesPath != filepath.Join(repo.HistoryDir, "info", "attributes") {
		t.Fatalf("PrivateAttributesPath = %q, want under HistoryDir", repo.PrivateAttributesPath)
	}
	if repo.HooksDir != filepath.Join(repo.FrigoDir, "hooks") {
		t.Fatalf("HooksDir = %q, want under FrigoDir", repo.HooksDir)
	}
	if repo.LinkedWorktree {
		t.Fatal("LinkedWorktree = true, want false")
	}
}

func TestWithFrigoDirRebasesSelectedStoreOnly(t *testing.T) {
	root := testrepo.Init(t)
	repo, err := Discover(context.Background(), git.Client{Path: "git"}, root)
	if err != nil {
		t.Fatal(err)
	}

	selected := filepath.Join(repo.CommonFrigoDir, "worktree-1234")
	rebased := repo.WithFrigoDir(selected)

	if repo.FrigoDir != filepath.Join(repo.GitDir, "frigo") {
		t.Fatalf("original FrigoDir = %q", repo.FrigoDir)
	}
	if rebased.Root != repo.Root {
		t.Fatalf("Root = %q, want %q", rebased.Root, repo.Root)
	}
	if rebased.GitDir != repo.GitDir {
		t.Fatalf("GitDir = %q, want %q", rebased.GitDir, repo.GitDir)
	}
	if rebased.CommonDir != repo.CommonDir {
		t.Fatalf("CommonDir = %q, want %q", rebased.CommonDir, repo.CommonDir)
	}
	if rebased.CommonFrigoDir != repo.CommonFrigoDir {
		t.Fatalf("CommonFrigoDir = %q, want %q", rebased.CommonFrigoDir, repo.CommonFrigoDir)
	}
	if rebased.LinkedStoresDir != repo.LinkedStoresDir {
		t.Fatalf("LinkedStoresDir = %q, want %q", rebased.LinkedStoresDir, repo.LinkedStoresDir)
	}
	if rebased.WorktreeIDPath != repo.WorktreeIDPath {
		t.Fatalf("WorktreeIDPath = %q, want %q", rebased.WorktreeIDPath, repo.WorktreeIDPath)
	}
	if rebased.ExcludePath != repo.ExcludePath {
		t.Fatalf("ExcludePath = %q, want %q", rebased.ExcludePath, repo.ExcludePath)
	}
	if rebased.OperationLockPath != repo.OperationLockPath {
		t.Fatalf("OperationLockPath = %q, want %q", rebased.OperationLockPath, repo.OperationLockPath)
	}
	if rebased.FrigoDir != selected {
		t.Fatalf("FrigoDir = %q, want %q", rebased.FrigoDir, selected)
	}
	if rebased.HistoryDir != filepath.Join(selected, "history.git") {
		t.Fatalf("HistoryDir = %q", rebased.HistoryDir)
	}
	if rebased.RegistryPath != filepath.Join(selected, "registry.json") {
		t.Fatalf("RegistryPath = %q", rebased.RegistryPath)
	}
	if rebased.AttributesPath != filepath.Join(selected, "attributes") {
		t.Fatalf("AttributesPath = %q", rebased.AttributesPath)
	}
	if rebased.PrivateAttributesPath != filepath.Join(selected, "history.git", "info", "attributes") {
		t.Fatalf("PrivateAttributesPath = %q", rebased.PrivateAttributesPath)
	}
	if rebased.HooksDir != filepath.Join(selected, "hooks") {
		t.Fatalf("HooksDir = %q", rebased.HooksDir)
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
	if repo.CommonFrigoDir != filepath.Join(root, ".git", "frigo") {
		t.Fatalf("CommonFrigoDir = %q", repo.CommonFrigoDir)
	}
	if repo.LinkedStoresDir != filepath.Join(root, ".git", "worktrees") {
		t.Fatalf("LinkedStoresDir = %q", repo.LinkedStoresDir)
	}
	if repo.WorktreeIDPath != filepath.Join(repo.GitDir, "frigo-id") {
		t.Fatalf("WorktreeIDPath = %q, want under worktree-local GitDir", repo.WorktreeIDPath)
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
	if repo.PrivateAttributesPath != filepath.Join(repo.HistoryDir, "info", "attributes") {
		t.Fatalf("PrivateAttributesPath = %q, want under HistoryDir", repo.PrivateAttributesPath)
	}
	if repo.HooksDir != filepath.Join(repo.FrigoDir, "hooks") {
		t.Fatalf("HooksDir = %q, want under worktree-local FrigoDir", repo.HooksDir)
	}
	if repo.ExcludePath != filepath.Join(root, ".git", "info", "exclude") {
		t.Fatalf("ExcludePath = %q", repo.ExcludePath)
	}
	if repo.OperationLockPath != filepath.Join(root, ".git", "frigo.lock") {
		t.Fatalf("OperationLockPath = %q, want shared common lock", repo.OperationLockPath)
	}
}

func TestDiscoverRejectsNonRepository(t *testing.T) {
	t.Parallel()

	_, err := Discover(context.Background(), git.Client{Path: "git"}, t.TempDir())
	if err == nil {
		t.Fatal("Discover() error = nil, want error")
	}
}
