package repository

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/roie/frigo/internal/git"
)

// Repository describes the main Git worktree and frigo's local metadata paths.
type Repository struct {
	Root                  string
	GitDir                string
	CommonDir             string
	CommonFrigoDir        string
	LinkedStoresDir       string
	WorktreeIDPath        string
	FrigoDir              string
	HistoryDir            string
	RegistryPath          string
	ExcludePath           string
	OperationLockPath     string
	AttributesPath        string
	PrivateAttributesPath string
	HooksDir              string
	LinkedWorktree        bool
}

// Discover finds the containing non-bare Git worktree from start.
func Discover(ctx context.Context, client git.Client, start string) (Repository, error) {
	root, err := client.Output(ctx, "", "-C", start, "rev-parse", "--show-toplevel")
	if err != nil {
		return Repository{}, fmt.Errorf("not inside a Git worktree: %w", err)
	}

	bare, err := client.Output(ctx, "", "-C", root, "rev-parse", "--is-bare-repository")
	if err != nil {
		return Repository{}, fmt.Errorf("inspect Git repository: %w", err)
	}
	if strings.EqualFold(bare, "true") {
		return Repository{}, fmt.Errorf("bare Git repositories are not supported")
	}

	gitDirRaw, err := client.Output(ctx, "", "-C", root, "rev-parse", "--git-dir")
	if err != nil {
		return Repository{}, fmt.Errorf("locate Git metadata: %w", err)
	}
	commonDirRaw, err := client.Output(ctx, "", "-C", root, "rev-parse", "--git-common-dir")
	if err != nil {
		return Repository{}, fmt.Errorf("locate common Git metadata: %w", err)
	}

	root, err = filepath.Abs(root)
	if err != nil {
		return Repository{}, fmt.Errorf("resolve worktree root: %w", err)
	}
	root = filepath.Clean(root)
	gitDir := resolveGitPath(root, gitDirRaw)
	commonDir := resolveGitPath(root, commonDirRaw)

	repo := Repository{
		Root:              root,
		GitDir:            gitDir,
		CommonDir:         commonDir,
		CommonFrigoDir:    filepath.Join(commonDir, "frigo"),
		LinkedStoresDir:   filepath.Join(commonDir, "worktrees"),
		WorktreeIDPath:    filepath.Join(gitDir, "frigo-id"),
		ExcludePath:       filepath.Join(commonDir, "info", "exclude"),
		OperationLockPath: filepath.Join(commonDir, "frigo.lock"),
		LinkedWorktree:    gitDir != commonDir,
	}

	repo = repo.WithFrigoDir(filepath.Join(gitDir, "frigo"))
	return repo, nil
}

// WithFrigoDir returns a copy with all selected-store paths derived from dir.
func (r Repository) WithFrigoDir(dir string) Repository {
	selected := filepath.Clean(dir)
	r.FrigoDir = selected
	r.HistoryDir = filepath.Join(selected, "history.git")
	r.RegistryPath = filepath.Join(selected, "registry.json")
	r.AttributesPath = filepath.Join(selected, "attributes")
	r.PrivateAttributesPath = filepath.Join(selected, "history.git", "info", "attributes")
	r.HooksDir = filepath.Join(selected, "hooks")
	return r
}

func resolveGitPath(root, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(root, value))
}
