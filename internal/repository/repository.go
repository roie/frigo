package repository

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"frigo/internal/git"
)

// Repository describes the main Git worktree and frigo's local metadata paths.
type Repository struct {
	Root           string
	GitDir         string
	CommonDir      string
	FrigoDir        string
	HistoryDir     string
	RegistryPath   string
	ExcludePath    string
	AttributesPath string
	HooksDir       string
	LinkedWorktree bool
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
	frigoDir := filepath.Join(gitDir, "frigo")

	return Repository{
		Root:           root,
		GitDir:         gitDir,
		CommonDir:      commonDir,
		FrigoDir:        frigoDir,
		HistoryDir:     filepath.Join(frigoDir, "history.git"),
		RegistryPath:   filepath.Join(frigoDir, "registry.json"),
		ExcludePath:    filepath.Join(commonDir, "info", "exclude"),
		AttributesPath: filepath.Join(frigoDir, "attributes"),
		HooksDir:       filepath.Join(frigoDir, "hooks"),
		LinkedWorktree: gitDir != commonDir,
	}, nil
}

func resolveGitPath(root, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(root, value))
}
