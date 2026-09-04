package frigo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"unicode/utf8"

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

// StatusPorcelain returns sorted Git porcelain v1 records terminated by NUL.
func (w *Workspace) StatusPorcelain(ctx context.Context, rawPaths []string) ([]byte, error) {
	var result []byte
	err := w.withLock(ctx, "status", func() error {
		var err error
		result, err = w.statusPorcelainLocked(ctx, rawPaths)
		return err
	})
	return result, err
}

func (w *Workspace) statusLocked(ctx context.Context, rawPaths []string) (string, error) {
	var output string
	err := w.withStatusComparison(ctx, rawPaths, func(client git.Client, baseOID string, paths []string) error {
		args := append([]string{"status", "--short", "--untracked-files=all", "--"}, paths...)
		result, err := w.statusAtOID(ctx, client, baseOID, args...)
		if err != nil {
			return fmt.Errorf("read frigo status: %w", err)
		}
		output = result
		return nil
	})
	return output, err
}

func (w *Workspace) statusPorcelainLocked(ctx context.Context, rawPaths []string) ([]byte, error) {
	var output []byte
	err := w.withStatusComparison(ctx, rawPaths, func(client git.Client, baseOID string, paths []string) error {
		args := append([]string{"status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames", "--"}, paths...)
		result, err := w.statusAtOIDBytes(ctx, client, baseOID, args...)
		if err != nil {
			return fmt.Errorf("read frigo status: %w", err)
		}
		output, err = normalizeStatusPorcelain(result)
		return err
	})
	return output, err
}

func (w *Workspace) withStatusComparison(ctx context.Context, rawPaths []string, read func(client git.Client, baseOID string, paths []string) error) error {
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
	baseOID, err := w.comparisonOID(ctx, base)
	if err != nil {
		return err
	}
	return w.withTemporaryIndexAt(ctx, base, intentPaths, func(client git.Client) error {
		return read(client, baseOID, paths)
	})
}

type porcelainStatusRecord struct {
	path  []byte
	bytes []byte
}

func normalizeStatusPorcelain(output []byte) ([]byte, error) {
	if len(output) == 0 {
		return nil, nil
	}
	if output[len(output)-1] != 0 {
		return nil, errors.New("invalid frigo status: missing NUL terminator")
	}
	rawRecords := bytes.Split(output[:len(output)-1], []byte{0})
	records := make([]porcelainStatusRecord, 0, len(rawRecords))
	for _, raw := range rawRecords {
		if len(raw) < 4 || raw[2] != ' ' {
			return nil, errors.New("invalid frigo status record")
		}
		path := raw[3:]
		if !utf8.Valid(path) || bytes.ContainsAny(path, "\r\n") {
			return nil, fmt.Errorf("invalid frigo status path %q", path)
		}
		record := bytes.Clone(raw)
		if record[0] != ' ' && record[1] == ' ' {
			// Frigo's temporary intent-to-add index is not staging. Move any
			// index-only artifact into the worktree column exposed to callers.
			record[1] = record[0]
			record[0] = ' '
		}
		records = append(records, porcelainStatusRecord{
			path:  bytes.Clone(path),
			bytes: record,
		})
	}
	sort.Slice(records, func(i, j int) bool {
		return bytes.Compare(records[i].path, records[j].path) < 0
	})
	result := make([]byte, 0, len(output))
	for _, record := range records {
		result = append(result, record.bytes...)
		result = append(result, 0)
	}
	return result, nil
}
