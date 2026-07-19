package frigo

import (
	"context"
	"fmt"

	"frigo/internal/git"
)

func (w *Workspace) List(ctx context.Context, rawPaths []string) ([]string, error) {
	owned, err := w.loadRegistry(ctx)
	if err != nil {
		return nil, err
	}
	if len(rawPaths) == 0 {
		return append([]string(nil), owned.Paths...), nil
	}
	paths, err := w.normalizePaths(rawPaths, false)
	if err != nil {
		return nil, err
	}
	for _, candidate := range paths {
		if !owned.OwnsExact(candidate) {
			return nil, fmt.Errorf("%s is not an exact owned frigo root", candidate)
		}
	}
	return paths, nil
}

func (w *Workspace) Status(ctx context.Context, rawPaths []string) (string, error) {
	owned, err := w.loadSeparatedRegistry(ctx)
	if err != nil {
		return "", err
	}
	paths, err := w.resolveScopedPaths(rawPaths, owned)
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", nil
	}
	intentPaths, err := w.intentPaths(paths)
	if err != nil {
		return "", err
	}
	var output string
	if err := w.withTemporaryIndex(ctx, intentPaths, func(client git.Client) error {
		args := append([]string{"status", "--short", "--untracked-files=all", "--"}, paths...)
		result, err := w.privateOutput(ctx, client, args...)
		if err != nil {
			return fmt.Errorf("read frigo status: %w", err)
		}
		output = result
		return nil
	}); err != nil {
		return "", err
	}
	return output, nil
}
