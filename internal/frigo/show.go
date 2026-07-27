package frigo

import (
	"context"
	"fmt"
	"strings"

	"github.com/roie/frigo/internal/git"
)

// Show returns one commit and its patch from Frigo's isolated history.
func (w *Workspace) Show(ctx context.Context, revision string, rawPaths []string) (string, error) {
	var result string
	err := w.withLock(ctx, "show", func() error {
		var err error
		result, err = w.showLocked(ctx, revision, rawPaths)
		return err
	})
	return result, err
}

func (w *Workspace) showLocked(ctx context.Context, revision string, rawPaths []string) (string, error) {
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

	oid := base.OID
	if revision != "" {
		oid, err = w.resolveShowRevision(ctx, revision)
		if err != nil {
			return "", err
		}
	}
	paths, err := w.normalizeHistoricalPaths(rawPaths)
	if err != nil {
		return "", err
	}

	args := []string{"show", "--no-ext-diff", "--no-textconv", "--no-color", oid}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	output, err := w.privateOutput(ctx, w.git.WithEnv("GIT_ATTR_NOSYSTEM=1"), args...)
	if err != nil {
		return "", fmt.Errorf("show frigo commit %s: %w", oid, err)
	}
	return output, nil
}

func (w *Workspace) resolveShowRevision(ctx context.Context, revision string) (string, error) {
	if revision == "" || strings.ContainsAny(revision, "\r\n") || revision[0] == '-' {
		return "", fmt.Errorf("invalid frigo revision %q", revision)
	}
	oid, err := w.privateOutput(
		ctx,
		w.git.WithEnv("GIT_ATTR_NOSYSTEM=1"),
		"rev-parse", "--verify", "--quiet", revision+"^{commit}",
	)
	if err != nil {
		if _, ok := git.ExitCode(err); ok {
			return "", fmt.Errorf("invalid frigo revision %q", revision)
		}
		return "", fmt.Errorf("resolve frigo revision %q: %w", revision, err)
	}
	return oid, nil
}
