package frigo

import (
	"context"
	"fmt"

	"github.com/roie/frigo/internal/git"
)

func (w *Workspace) List(ctx context.Context, rawPaths []string) ([]string, error) {
	var result []string
	err := w.withLock(ctx, "list", func() error {
		var err error
		result, err = w.listLocked(ctx, rawPaths)
		return err
	})
	return result, err
}

func (w *Workspace) listLocked(ctx context.Context, rawPaths []string) ([]string, error) {
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

// StatusResult is one lock-consistent main and private status snapshot.
type StatusResult struct {
	Main  string
	Frigo string
}

// StatusSnapshot reads both status halves while holding the common operation lock.
func (w *Workspace) StatusSnapshot(ctx context.Context) (StatusResult, error) {
	var result StatusResult
	err := w.withLock(ctx, "status", func() error {
		var err error
		result.Main, err = w.git.Output(ctx, w.repo.Root, "status", "--short", "--untracked-files=all", "--")
		if err != nil {
			return fmt.Errorf("read main status: %w", err)
		}
		result.Frigo, err = w.statusLocked(ctx, nil)
		return err
	})
	return result, err
}

func (w *Workspace) Status(ctx context.Context, rawPaths []string) (string, error) {
	var result string
	err := w.withLock(ctx, "status", func() error {
		var err error
		result, err = w.statusLocked(ctx, rawPaths)
		return err
	})
	return result, err
}

func (w *Workspace) statusLocked(ctx context.Context, rawPaths []string) (string, error) {
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
	base, err := w.resolveHistoryBase(ctx)
	if err != nil {
		return "", err
	}
	baseOID, err := w.comparisonOID(ctx, base)
	if err != nil {
		return "", err
	}
	var output string
	if err := w.withTemporaryIndexAt(ctx, base, intentPaths, func(client git.Client) error {
		args := append([]string{"status", "--short", "--untracked-files=all", "--"}, paths...)
		result, err := w.statusAtOID(ctx, client, baseOID, args...)
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
