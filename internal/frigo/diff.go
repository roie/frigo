package frigo

import (
	"context"
	"fmt"

	"frigo/internal/git"
)

func (w *Workspace) Diff(ctx context.Context, rawPaths []string) (string, error) {
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
		args := append([]string{"diff", "--no-ext-diff", "--ita-visible-in-index", "--"}, paths...)
		result, err := w.privateOutput(ctx, client, args...)
		if err != nil {
			return fmt.Errorf("read frigo diff: %w", err)
		}
		output = result
		return nil
	}); err != nil {
		return "", err
	}
	return output, nil
}
