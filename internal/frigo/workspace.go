package frigo

import (
	"context"

	"github.com/roie/frigo/internal/git"
	"github.com/roie/frigo/internal/repository"
)

type Workspace struct {
	repo    repository.Repository
	git     git.Client
	baseDir string
}

func NewWorkspace(repo repository.Repository, client git.Client, baseDir string) *Workspace {
	return &Workspace{repo: repo, git: client, baseDir: baseDir}
}

func (w *Workspace) privateOutput(ctx context.Context, client git.Client, args ...string) (string, error) {
	prefix := []string{
		"--git-dir=" + w.repo.HistoryDir,
		"--work-tree=" + w.repo.Root,
		"-c", "core.hooksPath=" + w.repo.HooksDir,
		"-c", "core.attributesFile=" + w.repo.AttributesPath,
		"-c", "core.autocrlf=false",
		"-c", "commit.gpgSign=false",
	}
	return client.Output(ctx, "", append(prefix, args...)...)
}
