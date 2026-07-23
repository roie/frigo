package frigo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/roie/frigo/internal/git"
	"github.com/roie/frigo/internal/ignore"
	"github.com/roie/frigo/internal/registry"
	"github.com/roie/frigo/internal/testsync"
)

func (w *Workspace) Release(ctx context.Context, rawPaths []string, force bool) (registry.ReleaseResult, error) {
	var result registry.ReleaseResult
	err := w.withLock(ctx, "release", func() error {
		var err error
		result, err = w.releaseLocked(ctx, rawPaths, force)
		return err
	})
	return result, err
}

func (w *Workspace) releaseLocked(ctx context.Context, rawPaths []string, force bool) (registry.ReleaseResult, error) {
	paths, err := w.normalizePaths(rawPaths, false)
	if err != nil {
		return registry.ReleaseResult{}, err
	}

	owned, err := w.loadRegistry(ctx)
	if err != nil {
		return registry.ReleaseResult{}, err
	}
	for _, candidate := range paths {
		if !owned.OwnsExact(candidate) {
			return registry.ReleaseResult{}, fmt.Errorf("%s is not an exact owned frigo root", candidate)
		}
	}

	if !force {
		base, err := w.resolveHistoryBase(ctx)
		if err != nil {
			return registry.ReleaseResult{}, err
		}
		dirty, err := w.releaseDirtyPaths(ctx, base, paths)
		if err != nil {
			return registry.ReleaseResult{}, err
		}
		if len(dirty) > 0 {
			return registry.ReleaseResult{}, fmt.Errorf("uncommitted frigo changes under %s; use --force to release anyway", strings.Join(dirty, ", "))
		}
	}
	if err := testsync.Point(ctx, "release-loaded"); err != nil {
		return registry.ReleaseResult{}, fmt.Errorf("synchronize release test: %w", err)
	}

	original := registry.Registry{Version: owned.Version, Paths: append([]string(nil), owned.Paths...)}
	result, err := owned.Release(paths...)
	if err != nil {
		return registry.ReleaseResult{}, err
	}
	rollback := func(cause error) (registry.ReleaseResult, error) {
		restoreErr := saveRegistry(w.repo.RegistryPath, original)
		var excludeErr error
		if restoreErr == nil {
			excludeErr = ignore.Sync(w.repo, original)
		}
		syncErr := testsync.Point(ctx, "release-rollback-complete")
		return registry.ReleaseResult{}, errors.Join(
			cause,
			wrapOptional("restore frigo registry", restoreErr),
			wrapOptional("restore frigo exclusions", excludeErr),
			wrapOptional("synchronize release rollback test", syncErr),
		)
	}
	if err := saveRegistry(w.repo.RegistryPath, owned); err != nil {
		return registry.ReleaseResult{}, fmt.Errorf("save frigo registry: %w", err)
	}
	if err := ignore.Sync(w.repo, owned); err != nil {
		return rollback(err)
	}
	if w.repo.LinkedWorktree && len(owned.Paths) == 0 {
		if err := w.releaseOwnedWorktreeLock(ctx); err != nil {
			if worktreeLockAllowsActiveRegistryRestore(err) {
				return rollback(err)
			}
			return registry.ReleaseResult{}, err
		}
	}
	return result, nil
}

func (w *Workspace) releaseDirtyPaths(ctx context.Context, base historyBase, paths []string) ([]string, error) {
	intentPaths, err := w.intentPaths(paths)
	if err != nil {
		return nil, err
	}

	dirty := make([]string, 0, len(paths))
	if err := w.withTemporaryIndexAt(ctx, base, intentPaths, func(client git.Client) error {
		for _, candidate := range paths {
			args := []string{"status", "--porcelain", "--untracked-files=all", "--", candidate}
			output, err := w.privateOutput(ctx, client, args...)
			if err != nil {
				return fmt.Errorf("inspect frigo changes under %s: %w", candidate, err)
			}
			if output != "" {
				dirty = append(dirty, candidate)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return dirty, nil
}
