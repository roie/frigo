package frigo

import (
	"context"
	"fmt"
)

func (w *Workspace) Log(ctx context.Context) (string, error) {
	if _, err := w.loadRegistry(ctx); err != nil {
		return "", err
	}
	hasHead, err := w.hasHead(ctx)
	if err != nil {
		return "", err
	}
	if !hasHead {
		return "no saved history", nil
	}
	output, err := w.privateOutput(ctx, w.git.WithEnv("GIT_ATTR_NOSYSTEM=1"), "log", "--oneline", "--decorate")
	if err != nil {
		return "", fmt.Errorf("read frigo log: %w", err)
	}
	return output, nil
}
