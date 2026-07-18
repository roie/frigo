package frigo

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"frigo/internal/registry"
)

func (w *Workspace) loadRegistry() (registry.Registry, error) {
	owned, err := registry.Load(w.repo.RegistryPath)
	if err != nil {
		return registry.Registry{}, fmt.Errorf("load frigo registry: %w", err)
	}
	return owned, nil
}

func (w *Workspace) resolveScopedPaths(rawPaths []string, owned registry.Registry) ([]string, error) {
	if len(rawPaths) == 0 {
		return append([]string(nil), owned.Paths...), nil
	}
	seen := make(map[string]struct{}, len(rawPaths))
	paths := make([]string, 0, len(rawPaths))
	for _, raw := range rawPaths {
		candidate, err := w.resolveScopedPath(raw)
		if err != nil {
			return nil, err
		}
		if !owned.Owns(candidate) {
			return nil, fmt.Errorf("%s is not owned by frigo", raw)
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		paths = append(paths, candidate)
	}
	sort.Strings(paths)
	return paths, nil
}

func (w *Workspace) resolveScopedPath(raw string) (string, error) {
	if raw == "" || strings.ContainsAny(raw, "\r\n") {
		return "", fmt.Errorf("invalid path %q", raw)
	}
	absolute := raw
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(w.baseDir, absolute)
	}
	absolute, err := filepath.Abs(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", raw, err)
	}
	absolute = filepath.Clean(absolute)
	relative, err := filepath.Rel(w.repo.Root, absolute)
	if err != nil {
		return "", fmt.Errorf("resolve path %q relative to worktree: %w", raw, err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the Git worktree", raw)
	}
	if relative == "." {
		return "", fmt.Errorf("the worktree root cannot be managed as one frigo path")
	}
	relative = filepath.ToSlash(relative)
	if relative == ".git" || strings.HasPrefix(relative, ".git/") {
		return "", fmt.Errorf("Git metadata cannot be managed by frigo")
	}
	return relative, nil
}

func (w *Workspace) intentPaths(paths []string) ([]string, error) {
	intent := make([]string, 0, len(paths))
	for _, candidate := range paths {
		hasContent, err := hasTrackableContent(filepath.Join(w.repo.Root, filepath.FromSlash(candidate)))
		if err != nil {
			return nil, fmt.Errorf("inspect %s: %w", candidate, err)
		}
		if hasContent {
			intent = append(intent, candidate)
		}
	}
	return intent, nil
}

func hasTrackableContent(filename string) (bool, error) {
	info, err := os.Lstat(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !info.IsDir() {
		return true, nil
	}
	found := false
	err = filepath.WalkDir(filename, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == filename {
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		found = true
		return fs.SkipAll
	})
	if err != nil && err != fs.SkipAll {
		return false, err
	}
	return found, nil
}
