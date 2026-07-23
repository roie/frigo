package ignore

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roie/frigo/internal/git"
	"github.com/roie/frigo/internal/metadata"
	"github.com/roie/frigo/internal/registry"
	"github.com/roie/frigo/internal/repository"
	"github.com/roie/frigo/internal/testrepo"
)

func TestLiteralPatternEscapesGitMetacharacters(t *testing.T) {
	got, err := LiteralPattern(`docs/My [local]* notes`)
	if err != nil {
		t.Fatal(err)
	}
	if want := `/docs/My\ \[local\]\*\ notes`; got != want {
		t.Fatalf("LiteralPattern() = %q, want %q", got, want)
	}
}

func TestSyncPreservesContentOutsideManagedSection(t *testing.T) {
	root := testrepo.Init(t)
	repo := discoverRepository(t, root)
	original := "keep one\n# >>> frigo >>>\n/old\n# <<< frigo <<<\nkeep two\n"
	if err := os.WriteFile(repo.ExcludePath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Sync(repo, registry.Registry{Version: registry.CurrentVersion, Paths: []string{"PLAN.md"}}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(repo.ExcludePath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(contents), "keep one\n# >>> frigo >>>\n/PLAN.md\n# <<< frigo <<<\nkeep two\n"; got != want {
		t.Fatalf("exclude = %q, want %q", got, want)
	}
}

func TestSyncNonemptyThenEmptyRestoresExactOutsideBytes(t *testing.T) {
	for _, tt := range []struct {
		name         string
		original     string
		syncedPrefix string
	}{
		{name: "with-terminal-newline", original: "keep one\n\nkeep two\n", syncedPrefix: "keep one\n\nkeep two\n# >>> frigo >>>\n"},
		{name: "without-terminal-newline", original: "keep without terminal newline", syncedPrefix: "# >>> frigo >>>\n/PLAN.md\n# <<< frigo <<<\nkeep without terminal newline"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := testrepo.Init(t)
			repo := discoverRepository(t, root)
			if err := os.WriteFile(repo.ExcludePath, []byte(tt.original), 0o644); err != nil {
				t.Fatal(err)
			}
			owned := registry.Registry{Version: registry.CurrentVersion, Paths: []string{"PLAN.md"}}
			for i := 0; i < 2; i++ {
				if err := Sync(repo, owned); err != nil {
					t.Fatal(err)
				}
				contents, err := os.ReadFile(repo.ExcludePath)
				if err != nil {
					t.Fatal(err)
				}
				if got := string(contents); !strings.HasPrefix(got, tt.syncedPrefix) {
					t.Fatalf("synced exclude = %q, want prefix %q", got, tt.syncedPrefix)
				}
			}
			for i := 0; i < 2; i++ {
				if err := Sync(repo, registry.New()); err != nil {
					t.Fatal(err)
				}
			}
			contents, err := os.ReadFile(repo.ExcludePath)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(contents); got != tt.original {
				t.Fatalf("exclude after repeated Sync = %q, want exact original %q", got, tt.original)
			}
		})
	}
}

func TestSyncUsesLivePointersAndExactAssociationAgreement(t *testing.T) {
	root := testrepo.Init(t)
	testrepo.Write(t, root, "README.md", "main\n")
	testrepo.CommitAll(t, root, "initial", "README.md")
	mainRepo := discoverRepository(t, root)
	linkedRoot := addLinkedWorktree(t, root, "linked", "linked-branch")
	otherRoot := addLinkedWorktree(t, root, "other", "other-branch")
	staleRoot := addLinkedWorktree(t, root, "stale", "stale-branch")
	mismatchRoot := addLinkedWorktree(t, root, "mismatch", "mismatch-branch")
	legacyRoot := addLinkedWorktree(t, root, "legacy", "legacy-branch")
	linkedRepo := associateLinkedRegistry(t, discoverRepository(t, linkedRoot), strings.Repeat("a", 32), "stale-current.txt")
	_ = associateLinkedRegistry(t, discoverRepository(t, otherRoot), strings.Repeat("b", 32), "other.txt")
	_ = associateLinkedRegistry(t, discoverRepository(t, staleRoot), strings.Repeat("c", 32), "stale.txt")
	mismatchRepo := associateLinkedRegistry(t, discoverRepository(t, mismatchRoot), strings.Repeat("d", 32), "mismatch.txt")
	legacyRepo := discoverRepository(t, legacyRoot)

	if err := registry.Save(mainRepo.RegistryPath, registry.Registry{Version: registry.CurrentVersion, Paths: []string{"main.txt"}}); err != nil {
		t.Fatal(err)
	}
	mismatchManifest := filepath.Join(mismatchRepo.FrigoDir, "manifest.json")
	manifest, err := metadata.Load(mismatchManifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.WorktreePath += "-wrong"
	if err := metadata.Save(mismatchManifest, manifest); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(legacyRepo.RegistryPath, registry.Registry{Version: registry.CurrentVersion, Paths: []string{"legacy.txt"}}); err != nil {
		t.Fatal(err)
	}
	orphanID := strings.Repeat("e", 32)
	orphanStore := filepath.Join(mainRepo.LinkedStoresDir, orphanID)
	if err := metadata.Save(filepath.Join(orphanStore, "manifest.json"), metadata.Manifest{
		Version: metadata.CurrentVersion, ID: orphanID, WorktreePath: filepath.Join(root, "orphan"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(filepath.Join(orphanStore, "registry.json"), registry.Registry{Version: registry.CurrentVersion, Paths: []string{"orphan.txt"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(staleRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(linkedRepo.ExcludePath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	owned := registry.Registry{Version: registry.CurrentVersion, Paths: []string{"current.txt"}}
	if err := Sync(linkedRepo, owned); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(linkedRepo.ExcludePath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(contents), "# >>> frigo >>>\n/current.txt\n/main.txt\n/other.txt\n# <<< frigo <<<\n"; got != want {
		t.Fatalf("exclude = %q, want %q", got, want)
	}
	for _, excluded := range []string{"stale-current.txt", "stale.txt", "mismatch.txt", "legacy.txt", "orphan.txt"} {
		if strings.Contains(string(contents), "/"+excluded) {
			t.Fatalf("exclude used unagreed association path %q: %q", excluded, contents)
		}
	}
}

func TestSyncUsesLivePointersWithoutFollowingSymlinks(t *testing.T) {
	root := testrepo.Init(t)
	testrepo.Write(t, root, "README.md", "main\n")
	testrepo.CommitAll(t, root, "initial", "README.md")
	mainRepo := discoverRepository(t, root)
	linkedRoot := addLinkedWorktree(t, root, "linked", "linked-branch")
	linkedRepo := discoverRepository(t, linkedRoot)
	target := filepath.Join(t.TempDir(), "pointer")
	id := strings.Repeat("f", 32)
	if err := metadata.SavePointer(target, id); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, linkedRepo.WorktreeIDPath); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(mainRepo.LinkedStoresDir, id)
	if err := metadata.Save(filepath.Join(store, "manifest.json"), metadata.Manifest{
		Version: metadata.CurrentVersion, ID: id, WorktreePath: linkedRoot,
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(filepath.Join(store, "registry.json"), registry.Registry{Version: registry.CurrentVersion, Paths: []string{"symlinked.txt"}}); err != nil {
		t.Fatal(err)
	}

	if err := Sync(mainRepo, registry.New()); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(mainRepo.ExcludePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "symlinked.txt") {
		t.Fatalf("exclude followed symlinked association: %q", contents)
	}
}

func TestSyncUsesLivePointersWithoutFollowingSymlinkedCommonStore(t *testing.T) {
	root := testrepo.Init(t)
	testrepo.Write(t, root, "README.md", "main\n")
	testrepo.CommitAll(t, root, "initial", "README.md")
	linkedRoot := addLinkedWorktree(t, root, "linked", "linked-branch")
	linkedRepo := discoverRepository(t, linkedRoot)
	target := t.TempDir()
	if err := registry.Save(filepath.Join(target, "registry.json"), registry.Registry{
		Version: registry.CurrentVersion,
		Paths:   []string{"symlinked-main.txt"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, linkedRepo.CommonFrigoDir); err != nil {
		t.Fatal(err)
	}

	if err := Sync(linkedRepo, registry.New()); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(linkedRepo.ExcludePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "symlinked-main.txt") {
		t.Fatalf("exclude followed symlinked common store: %q", contents)
	}
}

func TestCheckAndSyncRejectSymlinkedExcludeParent(t *testing.T) {
	root := testrepo.Init(t)
	repo := discoverRepository(t, root)
	externalInfo := filepath.Join(root, "external-info")
	if err := os.Rename(filepath.Dir(repo.ExcludePath), externalInfo); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalInfo, filepath.Dir(repo.ExcludePath)); err != nil {
		t.Fatal(err)
	}
	externalExclude := filepath.Join(externalInfo, "exclude")
	foreign := []byte("foreign-before\n")
	if err := os.WriteFile(externalExclude, foreign, 0o644); err != nil {
		t.Fatal(err)
	}
	owned := registry.Registry{Version: registry.CurrentVersion, Paths: []string{"PLAN.md"}}

	if _, err := Check(repo, owned); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Check() error = %v, want symlinked-parent rejection", err)
	}
	if err := Sync(repo, owned); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Sync() error = %v, want symlinked-parent rejection", err)
	}
	contents, err := os.ReadFile(externalExclude)
	if err != nil || !bytes.Equal(contents, foreign) {
		t.Fatalf("external exclude = %q, %v; want unchanged %q", contents, err, foreign)
	}
}

func TestSyncRejectsExternalEditBeforeReplace(t *testing.T) {
	root := testrepo.Init(t)
	repo := discoverRepository(t, root)
	if err := os.WriteFile(repo.ExcludePath, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldHook := syncBeforeReplace
	syncBeforeReplace = func() error {
		return os.WriteFile(repo.ExcludePath, []byte("external edit\n"), 0o644)
	}
	t.Cleanup(func() { syncBeforeReplace = oldHook })

	err := Sync(repo, registry.Registry{Version: registry.CurrentVersion, Paths: []string{"PLAN.md"}})
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("Sync() error = %v, want external-edit rejection", err)
	}
	contents, readErr := os.ReadFile(repo.ExcludePath)
	if readErr != nil || string(contents) != "external edit\n" {
		t.Fatalf("exclude = %q, %v; want external edit preserved", contents, readErr)
	}
}

func addLinkedWorktree(t *testing.T, root, name, branch string) string {
	t.Helper()
	linkedRoot := filepath.Join(root, name)
	testrepo.Run(t, root, "worktree", "add", "-q", "-b", branch, linkedRoot)
	return linkedRoot
}

func associateLinkedRegistry(t *testing.T, repo repository.Repository, id string, paths ...string) repository.Repository {
	t.Helper()
	store := filepath.Join(repo.LinkedStoresDir, id)
	if err := metadata.Save(filepath.Join(store, "manifest.json"), metadata.Manifest{
		Version:      metadata.CurrentVersion,
		ID:           id,
		WorktreePath: repo.Root,
	}); err != nil {
		t.Fatal(err)
	}
	if err := metadata.SavePointer(repo.WorktreeIDPath, id); err != nil {
		t.Fatal(err)
	}
	selected := repo.WithFrigoDir(store)
	if err := registry.Save(selected.RegistryPath, registry.Registry{Version: registry.CurrentVersion, Paths: paths}); err != nil {
		t.Fatal(err)
	}
	return selected
}

func discoverRepository(t *testing.T, root string) repository.Repository {
	t.Helper()
	repo, err := repository.Discover(context.Background(), git.Client{Path: "git"}, root)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}
