package frigo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"frigo/internal/ignore"
	"frigo/internal/registry"
)

var saveRegistry = registry.Save

func (w *Workspace) Add(ctx context.Context, rawPaths []string) (registry.AddResult, error) {
	paths, err := w.normalizePaths(rawPaths, true)
	if err != nil {
		return registry.AddResult{}, err
	}
	if err := w.rejectMainTracked(ctx, paths); err != nil {
		return registry.AddResult{}, err
	}

	owned, created, err := w.loadForAdd()
	if err != nil {
		return registry.AddResult{}, err
	}
	original := registry.Registry{Version: owned.Version, Paths: append([]string(nil), owned.Paths...)}
	result, err := owned.Add(paths...)
	if err != nil {
		return registry.AddResult{}, err
	}

	rollback := func(cause error) (registry.AddResult, error) {
		if rollbackErr := w.rollbackAdd(original, created); rollbackErr != nil {
			return registry.AddResult{}, fmt.Errorf("%v; rollback failed: %w", cause, rollbackErr)
		}
		return registry.AddResult{}, cause
	}
	if err := saveRegistry(w.repo.RegistryPath, owned); err != nil {
		return rollback(fmt.Errorf("save frigo registry: %w", err))
	}
	if err := ignore.Sync(w.repo, owned); err != nil {
		return rollback(err)
	}
	if err := w.validateMainSeparation(ctx, owned.Paths); err != nil {
		return rollback(err)
	}
	if created {
		if err := w.initialize(ctx); err != nil {
			return rollback(err)
		}
	}
	return result, nil
}

func (w *Workspace) loadForAdd() (registry.Registry, bool, error) {
	registryExists, err := pathExists(w.repo.RegistryPath)
	if err != nil {
		return registry.Registry{}, false, fmt.Errorf("inspect frigo registry: %w", err)
	}
	historyExists, err := pathExists(w.repo.HistoryDir)
	if err != nil {
		return registry.Registry{}, false, fmt.Errorf("inspect frigo history: %w", err)
	}
	switch {
	case registryExists && historyExists:
		owned, err := registry.Load(w.repo.RegistryPath)
		if err != nil {
			return registry.Registry{}, false, fmt.Errorf("load frigo registry: %w", err)
		}
		return owned, false, nil
	case registryExists != historyExists:
		return registry.Registry{}, false, fmt.Errorf("frigo metadata is incomplete; refusing to create a new history")
	default:
		frigoExists, err := pathExists(w.repo.FrigoDir)
		if err != nil {
			return registry.Registry{}, false, fmt.Errorf("inspect frigo metadata: %w", err)
		}
		if frigoExists {
			return registry.Registry{}, false, fmt.Errorf("frigo metadata is incomplete; refusing to create a new history")
		}
		return registry.New(), true, nil
	}
}

func (w *Workspace) rollbackAdd(original registry.Registry, created bool) error {
	if created {
		if err := os.RemoveAll(w.repo.FrigoDir); err != nil {
			return fmt.Errorf("remove new frigo metadata: %w", err)
		}
		return ignore.Sync(w.repo, registry.New())
	}
	if err := registry.Save(w.repo.RegistryPath, original); err != nil {
		return fmt.Errorf("restore frigo registry: %w", err)
	}
	return ignore.Sync(w.repo, original)
}

func (w *Workspace) initialize(ctx context.Context) error {
	if err := os.MkdirAll(w.repo.FrigoDir, 0o700); err != nil {
		return fmt.Errorf("create frigo metadata directory: %w", err)
	}
	if err := os.MkdirAll(w.repo.HooksDir, 0o700); err != nil {
		return fmt.Errorf("create empty frigo hooks directory: %w", err)
	}
	if err := os.WriteFile(w.repo.AttributesPath, nil, 0o600); err != nil {
		return fmt.Errorf("create empty frigo attributes file: %w", err)
	}
	if _, err := w.git.Output(ctx, "", "init", "--bare", "--quiet", w.repo.HistoryDir); err != nil {
		return fmt.Errorf("initialize frigo history: %w", err)
	}
	for _, config := range [][2]string{
		{"core.hooksPath", w.repo.HooksDir},
		{"core.attributesFile", w.repo.AttributesPath},
		{"core.autocrlf", "false"},
		{"commit.gpgSign", "false"},
	} {
		if _, err := w.privateOutput(ctx, w.git.WithEnv("GIT_ATTR_NOSYSTEM=1"), "config", config[0], config[1]); err != nil {
			return fmt.Errorf("configure frigo history: %w", err)
		}
	}
	return nil
}

func (w *Workspace) validateMainSeparation(ctx context.Context, paths []string) error {
	if err := w.rejectMainTracked(ctx, paths); err != nil {
		return err
	}
	return w.rejectMainVisible(ctx, paths)
}

func (w *Workspace) rejectMainTracked(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"-C", w.repo.Root, "ls-files", "--"}, paths...)
	output, err := w.git.Output(ctx, "", args...)
	if err != nil {
		return fmt.Errorf("inspect main Git index: %w", err)
	}
	if output != "" {
		return fmt.Errorf("cannot manage paths tracked by the main repository:\n%s", output)
	}
	return nil
}

func (w *Workspace) rejectMainVisible(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"-C", w.repo.Root, "ls-files", "--others", "--exclude-standard", "--"}, paths...)
	output, err := w.git.Output(ctx, "", args...)
	if err != nil {
		return fmt.Errorf("inspect main Git exclusions: %w", err)
	}
	if output != "" {
		return fmt.Errorf("these frigo paths are not ignored by the main repository:\n%s\na higher-precedence .gitignore rule may be re-including them", output)
	}
	for _, candidate := range paths {
		filename := filepath.Join(w.repo.Root, filepath.FromSlash(candidate))
		info, statErr := os.Lstat(filename)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				continue
			}
			return fmt.Errorf("inspect %s: %w", candidate, statErr)
		}
		if !info.IsDir() {
			continue
		}
		hasContent, err := hasTrackableContent(filename)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", candidate, err)
		}
		if hasContent {
			continue
		}
		ignored, err := w.git.Output(ctx, "", "-C", w.repo.Root, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "--", candidate)
		if err != nil {
			return fmt.Errorf("inspect main Git exclusions: %w", err)
		}
		if ignored == "" {
			return fmt.Errorf("these frigo paths are not ignored by the main repository:\n%s\na higher-precedence .gitignore rule may be re-including them", candidate)
		}
	}
	return nil
}

func pathExists(filename string) (bool, error) {
	_, err := os.Stat(filename)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
