package frigo

import (
	"context"
	"fmt"
	"strings"

	"frigo/internal/git"
	"frigo/internal/ignore"
	"frigo/internal/registry"
)

func (w *Workspace) Release(ctx context.Context, rawPaths []string, force bool) (registry.ReleaseResult, error) {
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
		dirty, err := w.releaseDirtyPaths(ctx, paths)
		if err != nil {
			return registry.ReleaseResult{}, err
		}
		if len(dirty) > 0 {
			return registry.ReleaseResult{}, fmt.Errorf("uncommitted frigo changes under %s; use --force to release anyway", strings.Join(dirty, ", "))
		}
	}

	original := registry.Registry{Version: owned.Version, Paths: append([]string(nil), owned.Paths...)}
	result, err := owned.Release(paths...)
	if err != nil {
		return registry.ReleaseResult{}, err
	}
	if err := saveRegistry(w.repo.RegistryPath, owned); err != nil {
		return registry.ReleaseResult{}, fmt.Errorf("save frigo registry: %w", err)
	}
	if err := ignore.Sync(w.repo, owned); err != nil {
		if rollbackErr := saveRegistry(w.repo.RegistryPath, original); rollbackErr != nil {
			return registry.ReleaseResult{}, fmt.Errorf("%v; rollback failed: %w", err, rollbackErr)
		}
		return registry.ReleaseResult{}, err
	}
	return result, nil
}

func (w *Workspace) releaseDirtyPaths(ctx context.Context, paths []string) ([]string, error) {
	intentPaths, err := w.intentPaths(paths)
	if err != nil {
		return nil, err
	}

	dirty := make([]string, 0, len(paths))
	if err := w.withTemporaryIndex(ctx, intentPaths, func(client git.Client) error {
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
