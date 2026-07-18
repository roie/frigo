package frigo

import (
	"context"
	"fmt"
	"strings"

	"frigo/internal/git"
)

func (w *Workspace) List(ctx context.Context, rawPaths []string) ([]string, error) {
	owned, err := w.loadRegistry()
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
	intentPaths, err := w.intentPaths(paths)
	if err != nil {
		return nil, err
	}
	var output string
	if err := w.withTemporaryIndex(ctx, intentPaths, func(client git.Client) error {
		args := append([]string{"ls-files", "--cached", "--others", "--exclude-standard", "--"}, paths...)
		result, err := w.privateOutput(ctx, client, args...)
		if err != nil {
			return fmt.Errorf("list frigo files: %w", err)
		}
		output = result
		return nil
	}); err != nil {
		return nil, err
	}
	if output == "" {
		return []string{}, nil
	}
	return strings.Split(output, "\n"), nil
}

func (w *Workspace) Status(ctx context.Context, rawPaths []string) (string, error) {
	owned, err := w.loadRegistry()
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
