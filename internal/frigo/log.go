package frigo

import (
	"context"
	"fmt"
)

func (w *Workspace) Log(ctx context.Context) (string, error) {
	var result string
	err := w.withLock(ctx, "log", func() error {
		var err error
		result, err = w.logLocked(ctx)
		return err
	})
	return result, err
}

func (w *Workspace) logLocked(ctx context.Context) (string, error) {
	if _, err := w.loadRegistry(ctx); err != nil {
		return "", err
	}
	base, err := w.resolveHistoryBase(ctx)
	if err != nil {
		return "", err
	}
	if !base.Exists {
		return "no saved history", nil
	}
	output, err := w.privateOutput(ctx, w.git.WithEnv("GIT_ATTR_NOSYSTEM=1"), "log", "--oneline", "--decorate", base.OID)
	if err != nil {
		return "", fmt.Errorf("read frigo log: %w", err)
	}
	return output, nil
}
