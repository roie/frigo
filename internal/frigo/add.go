package frigo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/roie/frigo/internal/git"
	"github.com/roie/frigo/internal/ignore"
	"github.com/roie/frigo/internal/registry"
	"github.com/roie/frigo/internal/testsync"
)

var saveRegistry = registry.Save

func (w *Workspace) Add(ctx context.Context, rawPaths []string) (registry.AddResult, error) {
	var result registry.AddResult
	err := w.withLock(ctx, "add", func() error {
		var err error
		result, err = w.addLocked(ctx, rawPaths)
		return err
	})
	return result, err
}

func (w *Workspace) addLocked(ctx context.Context, rawPaths []string) (registry.AddResult, error) {
	paths, err := w.normalizePaths(rawPaths, true)
	if err != nil {
		return registry.AddResult{}, err
	}
	if err := w.ensureLayout(ctx, true); err != nil {
		return registry.AddResult{}, err
	}
	if err := w.rejectMainTracked(ctx, paths); err != nil {
		return registry.AddResult{}, err
	}

	owned, created, err := w.loadForAdd(ctx)
	if err != nil {
		return registry.AddResult{}, err
	}
	if err := testsync.Point(ctx, "add-loaded"); err != nil {
		return registry.AddResult{}, fmt.Errorf("synchronize add test: %w", err)
	}
	original := registry.Registry{Version: owned.Version, Paths: append([]string(nil), owned.Paths...)}
	result, err := owned.Add(paths...)
	if err != nil {
		return registry.AddResult{}, err
	}

	deactivateProtection := w.repo.LinkedWorktree && len(original.Paths) == 0 && len(owned.Paths) > 0
	protectionActive := false
	rollback := func(cause error) (registry.AddResult, error) {
		rollbackErr := w.rollbackAdd(original, created)
		var protectionErr error
		if rollbackErr == nil && deactivateProtection && protectionActive {
			protectionErr = w.releaseOwnedWorktreeLock(ctx)
		}
		return registry.AddResult{}, errors.Join(
			cause,
			wrapOptional("rollback add", rollbackErr),
			wrapOptional("rollback linked worktree protection", protectionErr),
		)
	}
	if created {
		if err := w.initialize(ctx); err != nil {
			return rollback(err)
		}
	}
	if w.repo.LinkedWorktree && len(owned.Paths) > 0 {
		id, exists, err := loadManagedPointer(w.repo.WorktreeIDPath)
		if err != nil {
			return rollback(fmt.Errorf("load linked frigo pointer before protection: %w", err))
		}
		if !exists {
			return rollback(fmt.Errorf("linked frigo pointer is missing before registry activation"))
		}
		if _, err := w.ensureWorktreeProtection(ctx, id); err != nil {
			return rollback(err)
		}
		protectionActive = true
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
	return result, nil
}

func (w *Workspace) loadForAdd(ctx context.Context) (registry.Registry, bool, error) {
	registryExists, err := pathExists(w.repo.RegistryPath)
	if err != nil {
		return registry.Registry{}, false, fmt.Errorf("inspect frigo registry: %w", err)
	}
	historyExists, err := pathExists(w.repo.HistoryDir)
	if err != nil {
		return registry.Registry{}, false, fmt.Errorf("inspect frigo history: %w", err)
	}
	if registryExists {
		if err := requireManagedRegularFile(w.repo.RegistryPath); err != nil {
			return registry.Registry{}, false, fmt.Errorf("inspect frigo registry: %w", err)
		}
	}
	if historyExists {
		if err := requireManagedDirectory(w.repo.HistoryDir); err != nil {
			return registry.Registry{}, false, fmt.Errorf("inspect frigo history: %w", err)
		}
	}
	switch {
	case registryExists && historyExists:
		owned, err := w.loadRegistry(ctx)
		return owned, false, err
	case registryExists:
		return registry.Registry{}, false, fmt.Errorf("frigo metadata is incomplete; refusing to create a new history")
	case historyExists && w.repo.LinkedWorktree:
		if err := w.validateInitializedHistory(ctx); err != nil {
			return registry.Registry{}, false, err
		}
		return registry.New(), true, nil
	case historyExists:
		return registry.Registry{}, false, fmt.Errorf("frigo metadata is incomplete; refusing to create a new history")
	default:
		frigoExists, err := pathExists(w.repo.FrigoDir)
		if err != nil {
			return registry.Registry{}, false, fmt.Errorf("inspect frigo metadata: %w", err)
		}
		if frigoExists {
			if !w.repo.LinkedWorktree {
				linkedStoresOnly, err := w.mainStoreContainsOnlyLinkedStores()
				if err != nil {
					return registry.Registry{}, false, err
				}
				if linkedStoresOnly {
					return registry.New(), true, nil
				}
			}
			return registry.Registry{}, false, fmt.Errorf("frigo metadata is incomplete; refusing to create a new history")
		}
		return registry.New(), true, nil
	}
}

func (w *Workspace) rollbackAdd(original registry.Registry, created bool) error {
	if created {
		if w.repo.LinkedWorktree {
			if err := os.Remove(w.repo.RegistryPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove new linked registry: %w", err)
			}
			return ignore.Sync(w.repo, registry.New())
		}
		linkedStoresExist, inspectErr := pathExists(w.repo.LinkedStoresDir)
		if inspectErr != nil {
			return fmt.Errorf("inspect linked stores during main rollback: %w", inspectErr)
		}
		if !linkedStoresExist {
			if err := os.RemoveAll(w.repo.FrigoDir); err != nil {
				return fmt.Errorf("remove new frigo metadata: %w", err)
			}
			return ignore.Sync(w.repo, registry.New())
		}
		var rollbackErr error
		for _, path := range []string{w.repo.RegistryPath, w.repo.AttributesPath} {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("remove %s: %w", path, err))
			}
		}
		for _, path := range []string{w.repo.HistoryDir, w.repo.HooksDir} {
			if err := os.RemoveAll(path); err != nil {
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("remove %s: %w", path, err))
			}
		}
		rollbackErr = errors.Join(rollbackErr, ignore.Sync(w.repo, registry.New()))
		return rollbackErr
	}
	if err := registry.Save(w.repo.RegistryPath, original); err != nil {
		return fmt.Errorf("restore frigo registry: %w", err)
	}
	return ignore.Sync(w.repo, original)
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
		return mainVisibleError(output)
	}
	for _, candidate := range paths {
		if err := w.rejectRootMainVisible(ctx, candidate); err != nil {
			return err
		}
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
			return mainVisibleError(candidate)
		}
	}
	return nil
}

func (w *Workspace) rejectRootMainVisible(ctx context.Context, candidate string) error {
	input := "./" + candidate + "\x00"
	_, err := w.git.OutputWithInputNoLiteralPathspecs(ctx, "", input, "-C", w.repo.Root, "check-ignore", "--no-index", "--quiet", "-z", "--stdin")
	if err == nil {
		return nil
	}
	if code, ok := git.ExitCode(err); ok && code == 1 {
		return mainVisibleError(candidate)
	}
	return fmt.Errorf("inspect main Git exclusions: %w", err)
}

func mainVisibleError(paths string) error {
	return fmt.Errorf("these frigo paths are not ignored by the main repository:\n%s\na higher-precedence .gitignore rule may be re-including them", paths)
}

func pathExists(filename string) (bool, error) {
	_, err := os.Lstat(filename)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
