package frigo

import (
	"context"
	"fmt"

	"github.com/roie/frigo/internal/git"
)

func (w *Workspace) Restore(ctx context.Context, rawPaths []string) ([]string, error) {
	owned, err := w.loadSeparatedRegistry(ctx)
	if err != nil {
		return nil, err
	}
	paths, err := w.resolveScopedPaths(rawPaths, owned)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return []string{}, nil
	}

	hasHead, err := w.hasHead(ctx)
	if err != nil {
		return nil, err
	}
	if !hasHead {
		return nil, fmt.Errorf("no saved history")
	}

	for _, candidate := range paths {
		output, err := w.privateOutput(ctx, w.git.WithEnv("GIT_ATTR_NOSYSTEM=1"), "ls-tree", "-r", "--name-only", "HEAD", "--", candidate)
		if err != nil {
			return nil, fmt.Errorf("inspect saved path %s: %w", candidate, err)
		}
		if output == "" {
			return nil, fmt.Errorf("%s has no saved version", candidate)
		}
	}

	if err := w.withTemporaryIndex(ctx, nil, func(client git.Client) error {
		args := append([]string{"restore", "--source=HEAD", "--worktree", "--"}, paths...)
		if _, err := w.privateOutput(ctx, client, args...); err != nil {
			return fmt.Errorf("restore frigo paths: %w", err)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return paths, nil
}
