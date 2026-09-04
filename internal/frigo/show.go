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

// ShowNameStatus returns sorted status/path pairs for one historical commit.
func (w *Workspace) ShowNameStatus(ctx context.Context, revision string, rawPaths []string) ([]byte, error) {
	var result []byte
	err := w.withLock(ctx, "show", func() error {
		var err error
		result, err = w.showNameStatusLocked(ctx, revision, rawPaths)
		return err
	})
	return result, err
}

// ShowPatch returns exact unified patch bytes for one historical commit.
func (w *Workspace) ShowPatch(ctx context.Context, revision string, rawPaths []string) ([]byte, error) {
	var result []byte
	err := w.withLock(ctx, "show", func() error {
		var err error
		result, err = w.showPatchLocked(ctx, revision, rawPaths)
		return err
	})
	return result, err
}

// ShowBlob returns exact bytes for one path at one historical commit.
func (w *Workspace) ShowBlob(ctx context.Context, revision, rawPath string) ([]byte, error) {
	var result []byte
	err := w.withLock(ctx, "show", func() error {
		var err error
		result, err = w.showBlobLocked(ctx, revision, rawPath)
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
		oid, err = w.resolveCommit(ctx, revision)
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

func (w *Workspace) showNameStatusLocked(ctx context.Context, revision string, rawPaths []string) ([]byte, error) {
	client, comparison, oid, paths, err := w.prepareHistoricalComparison(ctx, revision, rawPaths)
	if err != nil {
		return nil, err
	}
	args := []string{
		"-c", "diff.relative=false",
		"diff", "--name-status", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", "--no-color",
		"-O", w.repo.AttributesPath, "--ignore-submodules=none", comparison, oid, "--",
	}
	args = append(args, paths...)
	output, err := w.privateOutputBytes(ctx, client, args...)
	if err != nil {
		return nil, fmt.Errorf("read changed paths for frigo commit %s: %w", oid, err)
	}
	return normalizeHistoricalChanges(output)
}

func (w *Workspace) showPatchLocked(ctx context.Context, revision string, rawPaths []string) ([]byte, error) {
	client, comparison, oid, paths, err := w.prepareHistoricalComparison(ctx, revision, rawPaths)
	if err != nil {
		return nil, err
	}
	args := w.machinePatchArgs()
	args = append(args, comparison, oid, "--")
	args = append(args, paths...)
	output, err := w.privateOutputBytes(ctx, client, args...)
	if err != nil {
		return nil, fmt.Errorf("read patch for frigo commit %s: %w", oid, err)
	}
	return output, nil
}

func (w *Workspace) showBlobLocked(ctx context.Context, revision, rawPath string) ([]byte, error) {
	if _, err := w.loadRegistry(ctx); err != nil {
		return nil, err
	}
	oid, err := w.resolveCommit(ctx, revision)
	if err != nil {
		return nil, err
	}
	paths, err := w.normalizeHistoricalPaths([]string{rawPath})
	if err != nil {
		return nil, err
	}
	if len(paths) != 1 {
		return nil, errors.New("historical blob selection requires exactly one path")
	}
	client := w.git.WithEnv(
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_PAGER=cat",
	)
	output, err := w.privateOutputBytes(ctx, client, "cat-file", "blob", oid+":"+paths[0])
	if err != nil {
		return nil, fmt.Errorf("read historical path %q at frigo commit %s: %w", paths[0], oid, err)
	}
	return output, nil
}

func (w *Workspace) prepareHistoricalComparison(ctx context.Context, revision string, rawPaths []string) (git.Client, string, string, []string, error) {
	if _, err := w.loadRegistry(ctx); err != nil {
		return git.Client{}, "", "", nil, err
	}
	oid, err := w.resolveCommit(ctx, revision)
	if err != nil {
		return git.Client{}, "", "", nil, err
	}
	paths, err := w.normalizeHistoricalPaths(rawPaths)
	if err != nil {
		return git.Client{}, "", "", nil, err
	}
	comparison, err := w.historicalComparisonOID(ctx, oid)
	if err != nil {
		return git.Client{}, "", "", nil, err
	}
	client := w.git.WithEnv(
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_PAGER=cat",
	)
	return client, comparison, oid, paths, nil
}

type historicalChange struct {
	status byte
	path   []byte
}

func normalizeHistoricalChanges(output []byte) ([]byte, error) {
	if len(output) == 0 {
		return nil, nil
	}
	if output[len(output)-1] != 0 {
		return nil, errors.New("invalid changed-path record termination")
	}
	fields := bytes.Split(output[:len(output)-1], []byte{0})
	if len(fields)%2 != 0 {
		return nil, fmt.Errorf("invalid changed-path field count %d", len(fields))
	}

	changes := make([]historicalChange, 0, len(fields)/2)
	for index := 0; index < len(fields); index += 2 {
		if len(fields[index]) != 1 {
			return nil, fmt.Errorf("invalid changed-path status %q", fields[index])
		}
		path := fields[index+1]
		if err := validateHistoricalOutputPath(path); err != nil {
			return nil, err
		}
		changes = append(changes, historicalChange{status: fields[index][0], path: bytes.Clone(path)})
	}
	sort.Slice(changes, func(left, right int) bool {
		return bytes.Compare(changes[left].path, changes[right].path) < 0
	})

	result := make([]byte, 0, len(output))
	for index, change := range changes {
		if index > 0 && bytes.Equal(changes[index-1].path, change.path) {
			return nil, fmt.Errorf("duplicate changed path %q", change.path)
		}
		result = append(result, change.status, 0)
		result = append(result, change.path...)
		result = append(result, 0)
	}
	return result, nil
}

func validateHistoricalOutputPath(path []byte) error {
	if len(path) == 0 || !utf8.Valid(path) || path[0] == '/' || bytes.ContainsAny(path, "\r\n") {
		return fmt.Errorf("invalid historical path %q", path)
	}
	components := bytes.Split(path, []byte{'/'})
	for index, component := range components {
		if len(component) == 0 || bytes.Equal(component, []byte(".")) || bytes.Equal(component, []byte("..")) {
			return fmt.Errorf("invalid historical path %q", path)
		}
		if index == 0 && bytes.Equal(component, []byte(".git")) {
			return fmt.Errorf("invalid historical path %q", path)
		}
	}
	return nil
}
