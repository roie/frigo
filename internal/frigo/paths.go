package frigo

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/roie/frigo/internal/registry"
)

func (w *Workspace) loadRegistry(ctx context.Context) (registry.Registry, error) {
	if err := w.ensureLayout(ctx, false); err != nil {
		return registry.Registry{}, err
	}
	owned, err := w.loadRegistryFile()
	if err != nil {
		return registry.Registry{}, err
	}
	if err := w.validateHistory(ctx); err != nil {
		return registry.Registry{}, err
	}
	return owned, nil
}

func (w *Workspace) loadSeparatedRegistry(ctx context.Context) (registry.Registry, error) {
	if err := w.ensureLayout(ctx, false); err != nil {
		return registry.Registry{}, err
	}
	owned, err := w.loadRegistryFile()
	if err != nil {
		return registry.Registry{}, err
	}
	if err := w.validateMainSeparation(ctx, owned.Paths); err != nil {
		return registry.Registry{}, err
	}
	if err := w.validateHistory(ctx); err != nil {
		return registry.Registry{}, err
	}
	return owned, nil
}

func (w *Workspace) loadRegistryFile() (registry.Registry, error) {
	registryExists, err := pathExists(w.repo.RegistryPath)
	if err != nil {
		return registry.Registry{}, fmt.Errorf("inspect frigo registry: %w", err)
	}
	historyExists, err := pathExists(w.repo.HistoryDir)
	if err != nil {
		return registry.Registry{}, fmt.Errorf("inspect frigo history: %w", err)
	}
	if registryExists {
		if err := requireManagedRegularFile(w.repo.RegistryPath); err != nil {
			return registry.Registry{}, fmt.Errorf("inspect frigo registry: %w", err)
		}
	}
	if historyExists {
		if err := requireManagedDirectory(w.repo.HistoryDir); err != nil {
			return registry.Registry{}, fmt.Errorf("inspect frigo history: %w", err)
		}
	}
	if registryExists != historyExists {
		return registry.Registry{}, fmt.Errorf("frigo metadata is incomplete: registry and history must exist together")
	}
	if !registryExists {
		return registry.Registry{}, fmt.Errorf("frigo metadata is not initialized; use frigo add first")
	}

	owned, err := registry.Load(w.repo.RegistryPath)
	if err != nil {
		return registry.Registry{}, fmt.Errorf("load frigo registry: %w", err)
	}
	return owned, nil
}

func (w *Workspace) validateHistory(ctx context.Context) error {
	bare, err := w.git.Output(ctx, "", "--git-dir="+w.repo.HistoryDir, "rev-parse", "--is-bare-repository")
	if err != nil {
		return fmt.Errorf("frigo history is not a valid bare Git repository: %w", err)
	}
	if !strings.EqualFold(bare, "true") {
		return fmt.Errorf("frigo history is not bare")
	}
	if err := w.ensurePrivateAttributes(); err != nil {
		return fmt.Errorf("ensure frigo private attributes: %w", err)
	}
	return nil
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
	return w.normalizePath(raw, false)
}

func (w *Workspace) normalizePaths(rawPaths []string, requireExist bool) ([]string, error) {
	if len(rawPaths) == 0 {
		return nil, fmt.Errorf("at least one path is required")
	}
	seen := make(map[string]struct{}, len(rawPaths))
	paths := make([]string, 0, len(rawPaths))
	for _, raw := range rawPaths {
		candidate, err := w.normalizePath(raw, requireExist)
		if err != nil {
			return nil, err
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

func (w *Workspace) normalizePath(raw string, requireExist bool) (string, error) {
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
	if rebased, ok := rebasePath(absolute, w.inputBaseDir, w.baseDir); ok {
		absolute = rebased
	}
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
	if !utf8.ValidString(relative) {
		return "", fmt.Errorf("%q is not a valid UTF-8 path", raw)
	}
	if relative == ".git" || strings.HasPrefix(relative, ".git/") {
		return "", fmt.Errorf("Git metadata cannot be managed by frigo")
	}
	if err := w.rejectGitMetadataAlias(absolute); err != nil {
		return "", err
	}
	if err := w.rejectSymlinkComponents(relative); err != nil {
		return "", err
	}
	if requireExist {
		if _, err := os.Lstat(absolute); err != nil {
			if os.IsNotExist(err) {
				return "", fmt.Errorf("%s does not exist", raw)
			}
			return "", fmt.Errorf("inspect %s: %w", raw, err)
		}
	}
	return relative, nil
}

func rebasePath(absolute, inputBaseDir, resolvedBaseDir string) (string, bool) {
	if inputBaseDir == resolvedBaseDir {
		return absolute, false
	}
	relative, err := filepath.Rel(inputBaseDir, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return absolute, false
	}
	return filepath.Join(resolvedBaseDir, relative), true
}

func (w *Workspace) rejectSymlinkComponents(relative string) error {
	candidate := w.repo.Root
	componentRelative := ""
	for _, component := range strings.Split(filepath.FromSlash(relative), string(filepath.Separator)) {
		candidate = filepath.Join(candidate, component)
		componentRelative = filepath.Join(componentRelative, component)
		info, err := os.Lstat(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("inspect %s: %w", filepath.ToSlash(componentRelative), err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s resolves through a symlink; add the target explicitly", filepath.ToSlash(componentRelative))
		}
	}
	return nil
}

func (w *Workspace) rejectGitMetadataAlias(absolute string) error {
	metadataInfos, err := w.gitMetadataInfos()
	if err != nil {
		return err
	}
	for candidate := absolute; ; candidate = filepath.Dir(candidate) {
		info, statErr := os.Stat(candidate)
		switch {
		case statErr == nil:
			for _, metadataInfo := range metadataInfos {
				if os.SameFile(info, metadataInfo) {
					return fmt.Errorf("Git metadata cannot be managed by frigo")
				}
			}
		case statErr != nil && !os.IsNotExist(statErr):
			return fmt.Errorf("inspect %s: %w", candidate, statErr)
		}
		if candidate == w.repo.Root {
			break
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			break
		}
	}
	return nil
}

func (w *Workspace) gitMetadataInfos() ([]os.FileInfo, error) {
	paths := []string{w.repo.GitDir, w.repo.CommonDir, filepath.Join(w.repo.Root, ".git")}
	infos := make([]os.FileInfo, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect Git metadata: %w", err)
		}
		duplicate := false
		for _, existing := range infos {
			if os.SameFile(info, existing) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			infos = append(infos, info)
		}
	}
	return infos, nil
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
