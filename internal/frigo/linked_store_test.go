package frigo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitpkg "github.com/roie/frigo/internal/git"
	"github.com/roie/frigo/internal/metadata"
	"github.com/roie/frigo/internal/registry"
	"github.com/roie/frigo/internal/repository"
	"github.com/roie/frigo/internal/testrepo"
)

func TestLinkedStoreUsesStableCommonHistory(t *testing.T) {
	ws, mainRoot, linkedRoot := newLinkedWorkspace(t)
	testrepo.Write(t, linkedRoot, "PLAN.md", "private\n")

	if _, err := ws.Add(context.Background(), []string{"PLAN.md"}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	id, err := metadata.LoadPointer(ws.repo.WorktreeIDPath)
	if err != nil {
		t.Fatalf("LoadPointer() error = %v", err)
	}
	store := filepath.Join(mainRoot, ".git", "frigo", "worktrees", id)
	if got := ws.repo.FrigoDir; got != store {
		t.Fatalf("FrigoDir = %q, want stable store %q", got, store)
	}
	manifest, err := metadata.Load(filepath.Join(store, "manifest.json"))
	if err != nil {
		t.Fatalf("Load(manifest) error = %v", err)
	}
	if manifest.ID != id {
		t.Fatalf("manifest ID = %q, want pointer ID %q", manifest.ID, id)
	}
	if manifest.WorktreePath != linkedRoot {
		t.Fatalf("manifest worktree path = %q, want %q", manifest.WorktreePath, linkedRoot)
	}
	if got := testrepo.Output(t, linkedRoot, "--git-dir="+filepath.Join(store, "history.git"), "rev-parse", "--is-bare-repository"); got != "true" {
		t.Fatalf("history bare state = %q, want true", got)
	}
	if _, err := os.Lstat(filepath.Join(ws.repo.GitDir, "frigo")); !os.IsNotExist(err) {
		t.Fatalf("legacy linked metadata exists: %v", err)
	}
}

func TestLinkedStoreInitializationReusesAssociation(t *testing.T) {
	ws, _, _ := newLinkedWorkspace(t)
	ctx := context.Background()
	if err := ws.withLock(ctx, "test initialize", func() error {
		return ws.ensureLayout(ctx, true)
	}); err != nil {
		t.Fatalf("first ensureLayout() error = %v", err)
	}
	firstID, err := metadata.LoadPointer(ws.repo.WorktreeIDPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := ws.withLock(ctx, "test initialize again", func() error {
		return ws.ensureLayout(ctx, true)
	}); err != nil {
		t.Fatalf("second ensureLayout() error = %v", err)
	}
	secondID, err := metadata.LoadPointer(ws.repo.WorktreeIDPath)
	if err != nil {
		t.Fatal(err)
	}
	if firstID != secondID {
		t.Fatalf("pointer changed from %q to %q", firstID, secondID)
	}
	entries, err := os.ReadDir(ws.repo.LinkedStoresDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != firstID {
		t.Fatalf("linked stores = %v, want only %q", entryNames(entries), firstID)
	}
}

func TestLinkedStoreAllowsMainInitializationAfterLinkedStableStore(t *testing.T) {
	linkedWS, mainRoot, _ := initializedLinkedWorkspace(t)
	id := linkedWorkspaceID(t, linkedWS)
	testrepo.Write(t, mainRoot, "main.local", "main\n")
	mainRepo, err := repository.Discover(context.Background(), gitpkg.Client{Path: "git"}, mainRoot)
	if err != nil {
		t.Fatal(err)
	}
	mainWS := NewWorkspace(mainRepo, gitpkg.Client{Path: "git"}, mainRoot)

	if _, err := mainWS.Add(context.Background(), []string{"main.local"}); err != nil {
		t.Fatalf("main Add() after linked initialization error = %v", err)
	}
	if _, err := registry.Load(mainWS.repo.RegistryPath); err != nil {
		t.Fatalf("main registry missing: %v", err)
	}
	assertValidLinkedStore(t, linkedWS)
	if got, err := metadata.LoadPointer(linkedWS.repo.WorktreeIDPath); err != nil || got != id {
		t.Fatalf("linked pointer = %q, %v; want unchanged %q", got, err, id)
	}
}

func TestLinkedStoreMainRollbackPreservesPreexistingLinkedHistory(t *testing.T) {
	linkedWS, mainRoot, _ := initializedLinkedWorkspace(t)
	id := linkedWorkspaceID(t, linkedWS)
	testrepo.Write(t, mainRoot, "main.local", "main\n")
	mainRepo, err := repository.Discover(context.Background(), gitpkg.Client{Path: "git"}, mainRoot)
	if err != nil {
		t.Fatal(err)
	}
	mainWS := NewWorkspace(mainRepo, gitpkg.Client{Path: "git"}, mainRoot)
	injected := errors.New("main registry save failed")
	oldSave := saveRegistry
	saveRegistry = func(string, registry.Registry) error { return injected }
	t.Cleanup(func() { saveRegistry = oldSave })

	if _, err := mainWS.Add(context.Background(), []string{"main.local"}); !errors.Is(err, injected) {
		t.Fatalf("main Add() error = %v, want injected save failure", err)
	}
	assertValidLinkedStore(t, linkedWS)
	if got, err := metadata.LoadPointer(linkedWS.repo.WorktreeIDPath); err != nil || got != id {
		t.Fatalf("linked pointer = %q, %v; want unchanged %q", got, err, id)
	}
	if _, err := os.Lstat(mainWS.repo.HistoryDir); !os.IsNotExist(err) {
		t.Fatalf("partial main history remains after rollback: %v", err)
	}
}

func TestLinkedStoreMainWorktreeKeepsDirectLayout(t *testing.T) {
	ws, root := newBareWorkspace(t)
	testrepo.Write(t, root, "PLAN.md", "private\n")

	if _, err := ws.Add(context.Background(), []string{"PLAN.md"}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if got, want := ws.repo.FrigoDir, filepath.Join(root, ".git", "frigo"); got != want {
		t.Fatalf("FrigoDir = %q, want %q", got, want)
	}
	if _, err := os.Lstat(ws.repo.WorktreeIDPath); !os.IsNotExist(err) {
		t.Fatalf("main worktree pointer exists: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(ws.repo.FrigoDir, "manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("main worktree manifest exists: %v", err)
	}
}

func TestUnsupportedLinkedMetadataIsRejectedWithoutMutation(t *testing.T) {
	ws, _, linkedRoot := newLinkedWorkspace(t)
	testrepo.Write(t, linkedRoot, "PLAN.md", "private\n")
	legacy := filepath.Join(ws.repo.GitDir, "frigo")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(legacy, "keep.txt")
	if err := os.WriteFile(marker, []byte("untouched\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := ws.Add(context.Background(), []string{"PLAN.md"})
	if err == nil || !strings.Contains(err.Error(), "pre-v0.2") {
		t.Fatalf("Add() error = %v, want actionable pre-v0.2 rejection", err)
	}
	contents, readErr := os.ReadFile(marker)
	if readErr != nil || string(contents) != "untouched\n" {
		t.Fatalf("legacy marker = %q, %v; want untouched", contents, readErr)
	}
	if _, statErr := os.Lstat(ws.repo.WorktreeIDPath); !os.IsNotExist(statErr) {
		t.Fatalf("pointer created for unsupported metadata: %v", statErr)
	}
	if _, statErr := os.Lstat(filepath.Join(ws.repo.CommonFrigoDir, "worktrees")); !os.IsNotExist(statErr) {
		t.Fatalf("stable store root created for unsupported metadata: %v", statErr)
	}
}

func TestLinkedStoreRejectsSymlinkAndForeignManagedEntries(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *Workspace, string) string
	}{
		{
			name: "pointer symlink",
			setup: func(t *testing.T, ws *Workspace, target string) string {
				t.Helper()
				if err := os.Symlink(target, ws.repo.WorktreeIDPath); err != nil {
					t.Fatal(err)
				}
				return ws.repo.WorktreeIDPath
			},
		},
		{
			name: "pointer foreign file",
			setup: func(t *testing.T, ws *Workspace, _ string) string {
				t.Helper()
				if err := os.WriteFile(ws.repo.WorktreeIDPath, []byte("foreign\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return ws.repo.WorktreeIDPath
			},
		},
		{
			name: "common store symlink",
			setup: func(t *testing.T, ws *Workspace, target string) string {
				t.Helper()
				if err := os.Symlink(target, ws.repo.CommonFrigoDir); err != nil {
					t.Fatal(err)
				}
				return ws.repo.CommonFrigoDir
			},
		},
		{
			name: "stores symlink",
			setup: func(t *testing.T, ws *Workspace, target string) string {
				t.Helper()
				if err := os.MkdirAll(ws.repo.CommonFrigoDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(ws.repo.CommonFrigoDir, "worktrees")); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(ws.repo.CommonFrigoDir, "worktrees")
			},
		},
		{
			name: "stores foreign file",
			setup: func(t *testing.T, ws *Workspace, _ string) string {
				t.Helper()
				if err := os.MkdirAll(ws.repo.CommonFrigoDir, 0o700); err != nil {
					t.Fatal(err)
				}
				filename := filepath.Join(ws.repo.CommonFrigoDir, "worktrees")
				if err := os.WriteFile(filename, []byte("foreign\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return filename
			},
		},
		{
			name: "selected store foreign file",
			setup: func(t *testing.T, ws *Workspace, _ string) string {
				t.Helper()
				id := strings.Repeat("9", 32)
				if err := metadata.SavePointer(ws.repo.WorktreeIDPath, id); err != nil {
					t.Fatal(err)
				}
				stores := filepath.Join(ws.repo.CommonFrigoDir, "worktrees")
				if err := os.MkdirAll(stores, 0o700); err != nil {
					t.Fatal(err)
				}
				store := filepath.Join(stores, id)
				if err := os.WriteFile(store, []byte("foreign\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return store
			},
		},
		{
			name: "selected store symlink",
			setup: func(t *testing.T, ws *Workspace, target string) string {
				t.Helper()
				id := strings.Repeat("a", 32)
				if err := metadata.SavePointer(ws.repo.WorktreeIDPath, id); err != nil {
					t.Fatal(err)
				}
				stores := filepath.Join(ws.repo.CommonFrigoDir, "worktrees")
				if err := os.MkdirAll(stores, 0o700); err != nil {
					t.Fatal(err)
				}
				store := filepath.Join(stores, id)
				if err := os.Symlink(target, store); err != nil {
					t.Fatal(err)
				}
				return store
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws, _, linkedRoot := newLinkedWorkspace(t)
			testrepo.Write(t, linkedRoot, "PLAN.md", "private\n")
			targetDir := t.TempDir()
			target := filepath.Join(targetDir, "target")
			if err := os.WriteFile(target, []byte("untouched\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			managed := tt.setup(t, ws, target)

			_, err := ws.Add(context.Background(), []string{"PLAN.md"})
			if err == nil {
				t.Fatal("Add() error = nil, want managed-entry rejection")
			}
			contents, readErr := os.ReadFile(target)
			if readErr != nil || string(contents) != "untouched\n" {
				t.Fatalf("foreign target = %q, %v; want untouched", contents, readErr)
			}
			if _, statErr := os.Lstat(managed); statErr != nil {
				t.Fatalf("managed foreign entry was removed: %v", statErr)
			}
		})
	}
}

func TestLinkedStoreDoesNotReplaceManifestCreatedAtDirectoryBoundary(t *testing.T) {
	ws, _, _ := newLinkedWorkspace(t)
	var manifestPath string
	ws.linkedStoreHook = func(name string) error {
		if name != "linked-store-directory" {
			return nil
		}
		entries, err := os.ReadDir(ws.repo.LinkedStoresDir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if isMetadataID(entry.Name()) {
				manifestPath = filepath.Join(ws.repo.LinkedStoresDir, entry.Name(), manifestName)
				return os.WriteFile(manifestPath, []byte("foreign manifest\n"), 0o600)
			}
		}
		return errors.New("created store not found")
	}

	ctx := context.Background()
	err := ws.withLock(ctx, "test manifest collision", func() error { return ws.ensureLayout(ctx, true) })
	if err == nil {
		t.Fatal("ensureLayout() error = nil, want no-replace manifest collision")
	}
	contents, readErr := os.ReadFile(manifestPath)
	if readErr != nil || string(contents) != "foreign manifest\n" {
		t.Fatalf("manifest collision entry = %q, %v; want untouched", contents, readErr)
	}
	if _, statErr := os.Lstat(ws.repo.WorktreeIDPath); !os.IsNotExist(statErr) {
		t.Fatalf("pointer created after manifest collision: %v", statErr)
	}
}

func TestLinkedStoreRecoveryRejectsAmbiguousMatchingManifests(t *testing.T) {
	ws, _, linkedRoot := newLinkedWorkspace(t)
	ids := []string{strings.Repeat("6", 32), strings.Repeat("7", 32)}
	for _, id := range ids {
		store := filepath.Join(ws.repo.LinkedStoresDir, id)
		if err := metadata.Save(filepath.Join(store, manifestName), metadata.Manifest{
			Version: metadata.CurrentVersion, ID: id, WorktreePath: linkedRoot,
		}); err != nil {
			t.Fatal(err)
		}
	}

	ctx := context.Background()
	err := ws.withLock(ctx, "test ambiguous recovery", func() error { return ws.ensureLayout(ctx, true) })
	if err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("ensureLayout() error = %v, want ambiguous recovery rejection", err)
	}
	if _, statErr := os.Lstat(ws.repo.WorktreeIDPath); !os.IsNotExist(statErr) {
		t.Fatalf("pointer created for ambiguous stores: %v", statErr)
	}
	for _, id := range ids {
		manifest, loadErr := metadata.Load(filepath.Join(ws.repo.LinkedStoresDir, id, manifestName))
		if loadErr != nil || manifest.ID != id {
			t.Fatalf("manifest %q changed: %#v, %v", id, manifest, loadErr)
		}
	}
}

func TestLinkedStoreRejectsEstablishedRegistrySymlinkWithoutReplacement(t *testing.T) {
	ws, _, linkedRoot := initializedLinkedWorkspace(t)
	testrepo.Write(t, linkedRoot, "PLAN.md", "private\n")
	target := filepath.Join(t.TempDir(), "registry.json")
	if err := registry.Save(target, registry.New()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, ws.repo.RegistryPath); err != nil {
		t.Fatal(err)
	}

	if _, err := ws.Add(context.Background(), []string{"PLAN.md"}); err == nil {
		t.Fatal("Add() error = nil, want registry symlink rejection")
	}
	info, err := os.Lstat(ws.repo.RegistryPath)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("registry symlink replaced: info=%v err=%v", info, err)
	}
	after, err := os.ReadFile(target)
	if err != nil || string(after) != string(before) {
		t.Fatalf("registry target changed: before=%q after=%q err=%v", before, after, err)
	}
}

func TestLinkedStoreRejectsEstablishedHistorySymlinkWithoutTargetMutation(t *testing.T) {
	ws, _, _ := initializedLinkedWorkspace(t)
	if err := registry.Save(ws.repo.RegistryPath, registry.New()); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "foreign.git")
	testrepo.Run(t, ws.repo.Root, "init", "--bare", "--quiet", target)
	if err := os.RemoveAll(ws.repo.HistoryDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, ws.repo.HistoryDir); err != nil {
		t.Fatal(err)
	}

	if _, err := ws.List(context.Background(), nil); err == nil {
		t.Fatal("List() error = nil, want history symlink rejection")
	}
	info, err := os.Lstat(ws.repo.HistoryDir)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("history symlink replaced: info=%v err=%v", info, err)
	}
	if _, err := os.Lstat(filepath.Join(target, "info", "attributes")); !os.IsNotExist(err) {
		t.Fatalf("foreign history target was mutated: %v", err)
	}
}

func TestLinkedStoreRequiresExactPointerManifestAgreement(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(metadata.Manifest) metadata.Manifest
	}{
		{name: "id mismatch", mutate: func(m metadata.Manifest) metadata.Manifest {
			m.ID = strings.Repeat("f", 32)
			return m
		}},
		{name: "worktree path mismatch", mutate: func(m metadata.Manifest) metadata.Manifest {
			m.WorktreePath += "-other"
			return m
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws, _, linkedRoot := newLinkedWorkspace(t)
			ctx := context.Background()
			if err := ws.withLock(ctx, "test initialize", func() error { return ws.ensureLayout(ctx, true) }); err != nil {
				t.Fatal(err)
			}
			id, err := metadata.LoadPointer(ws.repo.WorktreeIDPath)
			if err != nil {
				t.Fatal(err)
			}
			manifestPath := filepath.Join(ws.repo.LinkedStoresDir, id, "manifest.json")
			manifest, err := metadata.Load(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := metadata.Save(manifestPath, tt.mutate(manifest)); err != nil {
				t.Fatal(err)
			}

			repo, err := repository.Discover(ctx, gitpkg.Client{Path: "git"}, linkedRoot)
			if err != nil {
				t.Fatal(err)
			}
			fresh := NewWorkspace(repo, gitpkg.Client{Path: "git"}, linkedRoot)
			if err := fresh.withLock(ctx, "test reopen", func() error { return fresh.ensureLayout(ctx, false) }); err == nil {
				t.Fatal("ensureLayout() error = nil, want exact-association rejection")
			}
		})
	}
}

func TestLinkedStoreRecoversEveryDurableBoundary(t *testing.T) {
	boundaries := []string{
		"linked-store-directory",
		"linked-store-manifest",
		"linked-store-history",
		"linked-store-hooks",
		"linked-store-attributes",
		"linked-store-private-attributes",
		"linked-store-config-core.hooksPath",
		"linked-store-config-core.attributesFile",
		"linked-store-config-core.autocrlf",
		"linked-store-config-commit.gpgSign",
		"linked-store-history-validation",
		"linked-store-pointer",
	}
	for _, boundary := range boundaries {
		t.Run(boundary, func(t *testing.T) {
			ws, _, _ := newLinkedWorkspace(t)
			if err := os.MkdirAll(ws.repo.LinkedStoresDir, 0o700); err != nil {
				t.Fatal(err)
			}
			foreign := filepath.Join(ws.repo.LinkedStoresDir, "foreign-entry")
			if err := os.WriteFile(foreign, []byte("untouched\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			interrupted := errors.New("interrupt durable boundary")
			fired := false
			ws.linkedStoreHook = func(got string) error {
				if got == boundary && !fired {
					fired = true
					return interrupted
				}
				return nil
			}
			ctx := context.Background()
			err := ws.withLock(ctx, "test interrupted initialize", func() error {
				return ws.ensureLayout(ctx, true)
			})
			if !errors.Is(err, interrupted) {
				t.Fatalf("interrupted ensureLayout() error = %v, want injected failure", err)
			}
			if !fired {
				t.Fatalf("boundary %q was not reached", boundary)
			}
			if got, readErr := os.ReadFile(foreign); readErr != nil || string(got) != "untouched\n" {
				t.Fatalf("foreign entry after interruption = %q, %v", got, readErr)
			}

			ws.linkedStoreHook = nil
			if err := ws.withLock(ctx, "test recover initialize", func() error {
				return ws.ensureLayout(ctx, true)
			}); err != nil {
				t.Fatalf("recovery ensureLayout() error = %v", err)
			}
			assertValidLinkedStore(t, ws)
			if got, readErr := os.ReadFile(foreign); readErr != nil || string(got) != "untouched\n" {
				t.Fatalf("foreign entry after recovery = %q, %v", got, readErr)
			}
		})
	}
}

func TestLinkedStoreRepairsPartialHistory(t *testing.T) {
	ws, _, linkedRoot := newLinkedWorkspace(t)
	id := strings.Repeat("b", 32)
	store := filepath.Join(ws.repo.LinkedStoresDir, id)
	if err := os.MkdirAll(filepath.Join(store, "history.git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := metadata.Save(filepath.Join(store, "manifest.json"), metadata.Manifest{
		Version:      metadata.CurrentVersion,
		ID:           id,
		WorktreePath: linkedRoot,
	}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := ws.withLock(ctx, "test repair partial history", func() error {
		return ws.ensureLayout(ctx, true)
	}); err != nil {
		t.Fatalf("ensureLayout() error = %v", err)
	}
	assertValidLinkedStore(t, ws)
	if got, err := metadata.LoadPointer(ws.repo.WorktreeIDPath); err != nil || got != id {
		t.Fatalf("pointer = %q, %v; want recovered ID %q", got, err, id)
	}
}

func TestLinkedStoreRecoversWithoutAdoptingMalformedOrUnassociatedStores(t *testing.T) {
	ws, _, _ := newLinkedWorkspace(t)
	malformedID := strings.Repeat("c", 32)
	malformed := filepath.Join(ws.repo.LinkedStoresDir, malformedID)
	if err := os.MkdirAll(filepath.Join(malformed, "history.git"), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(malformed, "history.git", "keep.txt")
	if err := os.WriteFile(marker, []byte("untouched\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := ws.withLock(ctx, "test skip malformed store", func() error {
		return ws.ensureLayout(ctx, true)
	}); err != nil {
		t.Fatalf("ensureLayout() error = %v", err)
	}
	id, err := metadata.LoadPointer(ws.repo.WorktreeIDPath)
	if err != nil {
		t.Fatal(err)
	}
	if id == malformedID {
		t.Fatalf("adopted malformed store %q", malformedID)
	}
	if got, readErr := os.ReadFile(marker); readErr != nil || string(got) != "untouched\n" {
		t.Fatalf("malformed store marker = %q, %v; want untouched", got, readErr)
	}
	assertValidLinkedStore(t, ws)
}

func assertValidLinkedStore(t *testing.T, ws *Workspace) {
	t.Helper()
	id, err := metadata.LoadPointer(ws.repo.WorktreeIDPath)
	if err != nil {
		t.Fatalf("LoadPointer() error = %v", err)
	}
	store := filepath.Join(ws.repo.LinkedStoresDir, id)
	manifest, err := metadata.Load(filepath.Join(store, "manifest.json"))
	if err != nil {
		t.Fatalf("Load(manifest) error = %v", err)
	}
	if manifest.ID != id || manifest.WorktreePath != ws.repo.Root {
		t.Fatalf("manifest = %#v, want ID %q and path %q", manifest, id, ws.repo.Root)
	}
	if got := testrepo.Output(t, ws.repo.Root, "--git-dir="+filepath.Join(store, "history.git"), "rev-parse", "--is-bare-repository"); got != "true" {
		t.Fatalf("bare state = %q, want true", got)
	}
	configs := map[string]string{
		"core.hooksPath":      filepath.Join(store, "hooks"),
		"core.attributesFile": filepath.Join(store, "attributes"),
		"core.autocrlf":       "false",
		"commit.gpgSign":      "false",
	}
	for key, want := range configs {
		if got := testrepo.Output(t, ws.repo.Root, "--git-dir="+filepath.Join(store, "history.git"), "config", "--get", key); got != want {
			t.Fatalf("config %s = %q, want %q", key, got, want)
		}
	}
	private, err := os.ReadFile(filepath.Join(store, "history.git", "info", "attributes"))
	if err != nil {
		t.Fatal(err)
	}
	if string(private) != privateAttributes {
		t.Fatalf("private attributes = %q, want %q", private, privateAttributes)
	}
	for _, filename := range []string{filepath.Join(store, "hooks"), filepath.Join(store, "attributes")} {
		info, err := os.Lstat(filename)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("managed path %s is a symlink", filename)
		}
	}
}

func newLinkedWorkspace(t *testing.T) (*Workspace, string, string) {
	t.Helper()
	mainRoot := testrepo.Init(t)
	testrepo.Write(t, mainRoot, "README.md", "main\n")
	testrepo.CommitAll(t, mainRoot, "initial", "README.md")
	linkedRoot := filepath.Join(filepath.Dir(mainRoot), filepath.Base(mainRoot)+"-linked")
	testrepo.Run(t, mainRoot, "worktree", "add", "-q", "-b", "linked-branch", linkedRoot)
	t.Cleanup(func() { _ = os.RemoveAll(linkedRoot) })

	repo, err := repository.Discover(context.Background(), gitpkg.Client{Path: "git"}, linkedRoot)
	if err != nil {
		t.Fatal(err)
	}
	return NewWorkspace(repo, gitpkg.Client{Path: "git"}, linkedRoot), mainRoot, linkedRoot
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names
}
