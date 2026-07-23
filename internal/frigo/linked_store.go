package frigo

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/roie/frigo/internal/atomicfile"
	"github.com/roie/frigo/internal/metadata"
	"github.com/roie/frigo/internal/testsync"
)

const manifestName = "manifest.json"

func (w *Workspace) ensureLayout(ctx context.Context, allowCreate bool) error {
	if !w.repo.LinkedWorktree {
		w.repo = w.repo.WithFrigoDir(w.repo.CommonFrigoDir)
		if err := requireManagedDirectory(w.repo.FrigoDir); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("inspect main frigo metadata: %w", err)
		}
		return nil
	}

	legacy := filepath.Join(w.repo.GitDir, "frigo")
	if _, err := os.Lstat(legacy); err == nil {
		return fmt.Errorf("unsupported pre-v0.2 linked-worktree metadata at %s; use Frigo v0.1 to release it before using Frigo v0.2", legacy)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect pre-v0.2 linked-worktree metadata: %w", err)
	}

	id, exists, err := loadManagedPointer(w.repo.WorktreeIDPath)
	if err != nil {
		return fmt.Errorf("inspect linked frigo pointer: %w", err)
	}
	if exists {
		if err := w.selectLinkedStore(id); err != nil {
			return err
		}
		if allowCreate {
			if err := w.initializeHistory(ctx); err != nil {
				return err
			}
		}
		return nil
	}
	if !allowCreate {
		return fmt.Errorf("frigo metadata is not initialized for this linked worktree; use frigo add first")
	}
	return w.initializeLinkedStore(ctx)
}

func (w *Workspace) initializeLinkedStore(ctx context.Context) error {
	if !w.repo.LinkedWorktree {
		return fmt.Errorf("stable linked-store initialization requires a linked worktree")
	}
	if err := ensureManagedDirectory(w.repo.CommonFrigoDir); err != nil {
		return fmt.Errorf("prepare common frigo directory: %w", err)
	}
	if err := ensureManagedDirectory(w.repo.LinkedStoresDir); err != nil {
		return fmt.Errorf("prepare linked store directory: %w", err)
	}

	store, manifest, found, err := w.recoverLinkedStore()
	if err != nil {
		return err
	}
	if !found {
		id, err := metadata.GenerateID()
		if err != nil {
			return err
		}
		store = filepath.Join(w.repo.LinkedStoresDir, id)
		if err := os.Mkdir(store, 0o700); err != nil {
			return fmt.Errorf("create linked frigo store %s without replacement: %w", store, err)
		}
		if err := w.linkedStoreBoundary("linked-store-directory"); err != nil {
			return err
		}
		manifest = metadata.Manifest{
			Version:      metadata.CurrentVersion,
			ID:           id,
			WorktreePath: w.repo.Root,
		}
		if err := saveManifestExclusive(filepath.Join(store, manifestName), manifest); err != nil {
			return fmt.Errorf("save linked frigo manifest: %w", err)
		}
		if err := w.linkedStoreBoundary("linked-store-manifest"); err != nil {
			return err
		}
	}

	w.repo = w.repo.WithFrigoDir(store)
	if err := w.initializeHistory(ctx); err != nil {
		return err
	}
	if err := savePointerExclusive(w.repo.WorktreeIDPath, manifest.ID); err != nil {
		return fmt.Errorf("save linked frigo pointer: %w", err)
	}
	if err := w.linkedStoreBoundary("linked-store-pointer"); err != nil {
		return err
	}
	return nil
}

func (w *Workspace) recoverLinkedStore() (string, metadata.Manifest, bool, error) {
	entries, err := os.ReadDir(w.repo.LinkedStoresDir)
	if err != nil {
		return "", metadata.Manifest{}, false, fmt.Errorf("scan stable linked stores: %w", err)
	}

	type candidate struct {
		store    string
		manifest metadata.Manifest
	}
	var matches []candidate
	for _, entry := range entries {
		if !isMetadataID(entry.Name()) {
			continue
		}
		store := filepath.Join(w.repo.LinkedStoresDir, entry.Name())
		info, err := os.Lstat(store)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			continue
		}
		manifestPath := filepath.Join(store, manifestName)
		if err := requireManagedRegularFile(manifestPath); err != nil {
			continue
		}
		manifest, err := metadata.Load(manifestPath)
		if err != nil || manifest.ID != entry.Name() || manifest.WorktreePath != w.repo.Root {
			continue
		}
		matches = append(matches, candidate{store: store, manifest: manifest})
	}
	if len(matches) > 1 {
		return "", metadata.Manifest{}, false, fmt.Errorf("multiple stable linked frigo stores claim worktree %s; run frigo doctor after it becomes available", w.repo.Root)
	}
	if len(matches) == 0 {
		return "", metadata.Manifest{}, false, nil
	}
	return matches[0].store, matches[0].manifest, true, nil
}

func (w *Workspace) selectLinkedStore(id string) error {
	if err := requireManagedDirectory(w.repo.CommonFrigoDir); err != nil {
		return fmt.Errorf("inspect common frigo directory: %w", err)
	}
	if err := requireManagedDirectory(w.repo.LinkedStoresDir); err != nil {
		return fmt.Errorf("inspect linked store directory: %w", err)
	}
	store := filepath.Join(w.repo.LinkedStoresDir, id)
	if err := requireManagedDirectory(store); err != nil {
		return fmt.Errorf("inspect linked frigo store %s: %w", store, err)
	}
	manifestPath := filepath.Join(store, manifestName)
	if err := requireManagedRegularFile(manifestPath); err != nil {
		return fmt.Errorf("inspect linked frigo manifest: %w", err)
	}
	manifest, err := metadata.Load(manifestPath)
	if err != nil {
		return fmt.Errorf("load linked frigo manifest: %w", err)
	}
	if manifest.ID != id {
		return fmt.Errorf("linked frigo pointer ID %s disagrees with manifest ID %s", id, manifest.ID)
	}
	if manifest.WorktreePath != w.repo.Root {
		return fmt.Errorf("linked frigo manifest worktree path %q disagrees with current worktree %q", manifest.WorktreePath, w.repo.Root)
	}
	w.repo = w.repo.WithFrigoDir(store)
	return nil
}

func (w *Workspace) mainStoreContainsOnlyLinkedStores() (bool, error) {
	entries, err := os.ReadDir(w.repo.CommonFrigoDir)
	if err != nil {
		return false, fmt.Errorf("inspect main frigo metadata: %w", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(w.repo.LinkedStoresDir) {
		return false, nil
	}
	if err := requireManagedDirectory(w.repo.LinkedStoresDir); err != nil {
		return false, fmt.Errorf("inspect stable linked stores before main initialization: %w", err)
	}
	return true, nil
}

func (w *Workspace) initialize(ctx context.Context) error {
	if err := ensureManagedDirectory(w.repo.FrigoDir); err != nil {
		return fmt.Errorf("create frigo metadata directory: %w", err)
	}
	return w.initializeHistory(ctx)
}

func (w *Workspace) initializeHistory(ctx context.Context) error {
	if err := requireManagedDirectory(w.repo.FrigoDir); err != nil {
		return fmt.Errorf("inspect frigo store: %w", err)
	}
	if err := rejectSymlinksUnder(w.repo.HistoryDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect partial frigo history: %w", err)
	}
	if _, err := w.git.WithEnv("GIT_TEMPLATE_DIR=").Output(ctx, "", "init", "--bare", "--quiet", w.repo.HistoryDir); err != nil {
		return fmt.Errorf("initialize frigo history: %w", err)
	}
	if err := w.linkedStoreBoundary("linked-store-history"); err != nil {
		return err
	}

	created, err := createManagedDirectory(w.repo.HooksDir)
	if err != nil {
		return fmt.Errorf("create empty frigo hooks directory: %w", err)
	}
	if err := requireEmptyManagedDirectory(w.repo.HooksDir); err != nil {
		return fmt.Errorf("validate empty frigo hooks directory: %w", err)
	}
	if created {
		if err := w.linkedStoreBoundary("linked-store-hooks"); err != nil {
			return err
		}
	}
	written, err := ensureManagedFile(w.repo.AttributesPath, nil, 0o600, false)
	if err != nil {
		return fmt.Errorf("create empty frigo attributes file: %w", err)
	}
	if written {
		if err := w.linkedStoreBoundary("linked-store-attributes"); err != nil {
			return err
		}
	}
	if err := ensureManagedDirectory(filepath.Dir(w.repo.PrivateAttributesPath)); err != nil {
		return fmt.Errorf("create frigo private attributes directory: %w", err)
	}
	written, err = ensureManagedFile(w.repo.PrivateAttributesPath, []byte(privateAttributes), 0o600, true)
	if err != nil {
		return fmt.Errorf("initialize frigo private attributes: %w", err)
	}
	if written {
		if err := w.linkedStoreBoundary("linked-store-private-attributes"); err != nil {
			return err
		}
	}

	for _, config := range [][2]string{
		{"core.hooksPath", w.repo.HooksDir},
		{"core.attributesFile", w.repo.AttributesPath},
		{"core.autocrlf", "false"},
		{"commit.gpgSign", "false"},
	} {
		if _, err := w.privateOutput(ctx, w.git, "config", config[0], config[1]); err != nil {
			return fmt.Errorf("configure frigo history: %w", err)
		}
		if err := w.linkedStoreBoundary("linked-store-config-" + config[0]); err != nil {
			return err
		}
	}
	if err := w.validateInitializedHistory(ctx); err != nil {
		return err
	}
	return w.linkedStoreBoundary("linked-store-history-validation")
}

func (w *Workspace) validateInitializedHistory(ctx context.Context) error {
	if err := rejectSymlinksUnder(w.repo.HistoryDir); err != nil {
		return fmt.Errorf("validate frigo history entries: %w", err)
	}
	bare, err := w.git.Output(ctx, "", "--git-dir="+w.repo.HistoryDir, "rev-parse", "--is-bare-repository")
	if err != nil || !strings.EqualFold(bare, "true") {
		if err == nil {
			err = fmt.Errorf("repository reports bare=%q", bare)
		}
		return fmt.Errorf("frigo history is not a valid bare Git repository: %w", err)
	}
	if err := requireEmptyManagedDirectory(w.repo.HooksDir); err != nil {
		return fmt.Errorf("validate frigo hooks: %w", err)
	}
	if err := requireManagedFileContents(w.repo.AttributesPath, nil); err != nil {
		return fmt.Errorf("validate frigo attributes: %w", err)
	}
	if err := requireManagedFileContents(w.repo.PrivateAttributesPath, []byte(privateAttributes)); err != nil {
		return fmt.Errorf("validate frigo private attributes: %w", err)
	}
	for _, config := range [][2]string{
		{"core.hooksPath", w.repo.HooksDir},
		{"core.attributesFile", w.repo.AttributesPath},
		{"core.autocrlf", "false"},
		{"commit.gpgSign", "false"},
	} {
		value, err := w.git.Output(ctx, "", "--git-dir="+w.repo.HistoryDir, "config", "--get", config[0])
		if err != nil {
			return fmt.Errorf("validate frigo history config %s: %w", config[0], err)
		}
		if value != config[1] {
			return fmt.Errorf("frigo history config %s = %q, want %q", config[0], value, config[1])
		}
	}
	return nil
}

func (w *Workspace) linkedStoreBoundary(name string) error {
	if w.linkedStoreHook != nil {
		if err := w.linkedStoreHook(name); err != nil {
			return err
		}
	}
	return testsync.Fail(name)
}

func loadManagedPointer(filename string) (string, bool, error) {
	info, err := os.Lstat(filename)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("managed pointer %s is not a regular file", filename)
	}
	id, err := metadata.LoadPointer(filename)
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

func requireManagedDirectory(filename string) error {
	info, err := os.Lstat(filename)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("managed path %s is not a real directory", filename)
	}
	return nil
}

func requireEmptyManagedDirectory(root string) error {
	if err := requireManagedDirectory(root); err != nil {
		return err
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed directory %s contains symlink %s", root, path)
		}
		return fmt.Errorf("managed directory %s is not empty: %s", root, path)
	})
}

func ensureManagedDirectory(filename string) error {
	err := requireManagedDirectory(filename)
	if err == nil {
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	if err := os.Mkdir(filename, 0o700); err != nil {
		if !os.IsExist(err) {
			return err
		}
		return requireManagedDirectory(filename)
	}
	return nil
}

func createManagedDirectory(filename string) (bool, error) {
	err := requireManagedDirectory(filename)
	if err == nil {
		return false, nil
	}
	if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.Mkdir(filename, 0o700); err != nil {
		if !os.IsExist(err) {
			return false, err
		}
		return false, requireManagedDirectory(filename)
	}
	return true, nil
}

func requireManagedRegularFile(filename string) error {
	info, err := os.Lstat(filename)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("managed path %s is not a regular file", filename)
	}
	return nil
}

func requireManagedFileContents(filename string, expected []byte) error {
	if err := requireManagedRegularFile(filename); err != nil {
		return err
	}
	contents, err := os.ReadFile(filename)
	if err != nil {
		return err
	}
	if !bytes.Equal(contents, expected) {
		return fmt.Errorf("managed file %s has unexpected contents", filename)
	}
	return nil
}

func ensureManagedFile(filename string, data []byte, mode os.FileMode, replace bool) (bool, error) {
	info, err := os.Lstat(filename)
	if os.IsNotExist(err) {
		if err := saveBytesExclusive(filename, data, mode); err != nil {
			return false, err
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("managed path %s is not a regular file", filename)
	}
	existing, err := os.ReadFile(filename)
	if err != nil {
		return false, err
	}
	if !replace && !bytes.Equal(existing, data) {
		return false, fmt.Errorf("managed file %s has unexpected contents", filename)
	}
	if !replace || bytes.Equal(existing, data) {
		return false, nil
	}
	if err := atomicfile.Write(filename, data, mode); err != nil {
		return false, err
	}
	return true, nil
}

func saveManifestExclusive(filename string, manifest metadata.Manifest) error {
	return saveExclusive(filename, func(temp string) error { return metadata.Save(temp, manifest) })
}

func savePointerExclusive(filename, id string) error {
	return saveExclusive(filename, func(temp string) error { return metadata.SavePointer(temp, id) })
}

func saveBytesExclusive(filename string, data []byte, mode os.FileMode) error {
	return saveExclusive(filename, func(temp string) error { return atomicfile.Write(temp, data, mode) })
}

func saveExclusive(filename string, writeTemp func(string) error) error {
	parent := filepath.Dir(filename)
	if err := requireManagedDirectory(parent); err != nil {
		return err
	}
	temp, err := os.CreateTemp(parent, ".frigo-exclusive-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempName)
		return err
	}
	defer os.Remove(tempName)
	if err := writeTemp(tempName); err != nil {
		return err
	}
	if err := os.Link(tempName, filename); err != nil {
		return fmt.Errorf("create %s without replacement: %w", filename, err)
	}
	return nil
}

func rejectSymlinksUnder(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed history contains symlink %s", path)
		}
		return nil
	})
}

func isMetadataID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}
