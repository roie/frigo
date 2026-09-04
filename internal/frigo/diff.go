package frigo

import (
	"context"
	"fmt"

	"github.com/roie/frigo/internal/git"
)

func (w *Workspace) Diff(ctx context.Context, rawPaths []string) (string, error) {
	var result string
	err := w.withLock(ctx, "diff", func() error {
		var err error
		result, err = w.diffLocked(ctx, rawPaths)
		return err
	})
	return result, err
}

// DiffPatch returns exact unified patch bytes for current Frigo changes.
func (w *Workspace) DiffPatch(ctx context.Context, rawPaths []string) ([]byte, error) {
	var result []byte
	err := w.withLock(ctx, "diff", func() error {
		var err error
		result, err = w.diffPatchLocked(ctx, rawPaths)
		return err
	})
	return result, err
}

func (w *Workspace) diffLocked(ctx context.Context, rawPaths []string) (string, error) {
	var output string
	err := w.withDiffComparison(ctx, rawPaths, func(client git.Client, _ historyBase, paths []string) error {
		args := append([]string{"diff", "--no-ext-diff", "--ita-visible-in-index", "--"}, paths...)
		result, err := w.privateOutput(ctx, client, args...)
		if err != nil {
			return fmt.Errorf("read frigo diff: %w", err)
		}
		output = result
		return nil
	})
	return output, err
}

func (w *Workspace) diffPatchLocked(ctx context.Context, rawPaths []string) ([]byte, error) {
	var output []byte
	err := w.withDiffComparison(ctx, rawPaths, func(client git.Client, base historyBase, paths []string) error {
		oid, err := w.comparisonOID(ctx, base)
		if err != nil {
			return err
		}
		args := w.machinePatchArgs()
		args = append(args, "--ita-visible-in-index", oid, "--")
		args = append(args, paths...)
		result, err := w.privateOutputBytes(ctx, client, args...)
		if err != nil {
			return fmt.Errorf("read frigo diff: %w", err)
		}
		output = result
		return nil
	})
	return output, err
}

func (w *Workspace) machinePatchArgs() []string {
	return []string{
		"-c", "core.quotePath=true",
		"-c", "diff.suppressBlankEmpty=false",
		"-c", "diff.compactionHeuristic=false",
		"-c", "diff.relative=false",
		"diff",
		// Metadata validation guarantees the public attributes file is empty.
		// An explicit empty order file cancels the user's diff.orderFile.
		"-O", w.repo.AttributesPath,
		"--full-index",
		"--ignore-submodules=none",
		"--submodule=short",
		"--patch",
		"--no-ext-diff",
		"--no-textconv",
		"--no-color",
		"--no-renames",
		"--src-prefix=a/",
		"--dst-prefix=b/",
		"--unified=3",
		"--inter-hunk-context=0",
		"--diff-algorithm=myers",
		"--no-indent-heuristic",
	}
}

func (w *Workspace) withDiffComparison(ctx context.Context, rawPaths []string, read func(client git.Client, base historyBase, paths []string) error) error {
	owned, err := w.loadSeparatedRegistry(ctx)
	if err != nil {
		return err
	}
	paths, err := w.resolveScopedPaths(rawPaths, owned)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	intentPaths, err := w.intentPaths(paths)
	if err != nil {
		return err
	}
	base, err := w.resolveHistoryBase(ctx)
	if err != nil {
		return err
	}
	return w.withTemporaryIndexAt(ctx, base, intentPaths, func(client git.Client) error {
		return read(client, base, paths)
	})
}
