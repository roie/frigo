package frigo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/roie/frigo/internal/atomicfile"
	"github.com/roie/frigo/internal/git"
	"github.com/roie/frigo/internal/lockfile"
	"github.com/roie/frigo/internal/repository"
)

type Workspace struct {
	repo     repository.Repository
	git      git.Client
	baseDir  string
	lockWait time.Duration
}

func NewWorkspace(repo repository.Repository, client git.Client, baseDir string) *Workspace {
	return &Workspace{repo: repo, git: client, baseDir: baseDir, lockWait: operationLockWait}
}

const operationLockWait = 10 * time.Second

func (w *Workspace) withLock(ctx context.Context, operation string, fn func() error) (err error) {
	lock, err := lockfile.Acquire(ctx, w.repo.OperationLockPath, operation, w.lockWait)
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("release operation lock: %w", releaseErr))
		}
	}()
	return fn()
}

func (w *Workspace) privateOutput(ctx context.Context, client git.Client, args ...string) (string, error) {
	client = client.WithEnv("GIT_ATTR_NOSYSTEM=1")
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

const privateAttributes = "* -text !eol !filter -ident !working-tree-encoding !diff\n"

func (w *Workspace) ensurePrivateAttributes() error {
	return atomicfile.Write(w.repo.PrivateAttributesPath, []byte(privateAttributes), 0o600)
}
