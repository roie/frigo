package frigo

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/roie/frigo/internal/testexec"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"

	gitpkg "github.com/roie/frigo/internal/git"
	"github.com/roie/frigo/internal/ignore"
	"github.com/roie/frigo/internal/registry"
	"github.com/roie/frigo/internal/repository"
	"github.com/roie/frigo/internal/testrepo"
)

func TestAddAcceptsSymlinkedWorktreePath(t *testing.T) {
	ws, root := newBareWorkspace(t)
	alias := filepath.Join(t.TempDir(), "worktree-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	testrepo.Write(t, root, "PLAN.md", "draft\n")
	aliased := NewWorkspace(ws.repo, gitpkg.Client{Path: "git"}, alias)

	result, err := aliased.Add(context.Background(), []string{filepath.Join(alias, "PLAN.md")})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Added, []string{"PLAN.md"}) {
		t.Fatalf("Add() added = %v, want [PLAN.md]", result.Added)
	}
}

func TestAddRejectsExplicitSymlinkPaths(t *testing.T) {
	tests := []struct {
		name      string
		setupPath func(*testing.T, string) string
		wantError string
	}{
		{
			name: "symlink file",
			setupPath: func(t *testing.T, root string) string {
				testrepo.Write(t, root, "target.txt", "private\n")
				alias := filepath.Join(root, "alias.txt")
				if err := os.Symlink("target.txt", alias); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				return alias
			},
			wantError: "alias.txt resolves through a symlink; add the target explicitly",
		},
		{
			name: "symlink directory",
			setupPath: func(t *testing.T, root string) string {
				testrepo.Write(t, root, "real/PLAN.md", "private\n")
				alias := filepath.Join(root, "alias-dir")
				if err := os.Symlink("real", alias); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				return filepath.Join(alias, "PLAN.md")
			},
			wantError: "alias-dir resolves through a symlink; add the target explicitly",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws, root := newBareWorkspace(t)
			managedPath := tt.setupPath(t, root)

			_, err := ws.Add(context.Background(), []string{managedPath})
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Add() error = %v, want %q", err, tt.wantError)
			}
			for _, metadataPath := range []string{ws.repo.RegistryPath, ws.repo.HistoryDir} {
				exists, inspectErr := pathExists(metadataPath)
				if inspectErr != nil {
					t.Fatal(inspectErr)
				}
				if exists {
					t.Fatalf("metadata path exists after rejected add: %s", metadataPath)
				}
			}
		})
	}
}

func TestNormalizeHistoricalPaths(t *testing.T) {
	ws, root := newBareWorkspace(t)

	paths, err := ws.normalizeHistoricalPaths([]string{"z.md", "./a.md", "z.md"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(paths, []string{"a.md", "z.md"}) {
		t.Fatalf("normalizeHistoricalPaths() = %v, want [a.md z.md]", paths)
	}

	paths, err = ws.normalizeHistoricalPaths([]string{filepath.Join(root, "absolute.md"), "missing.md"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(paths, []string{"absolute.md", "missing.md"}) {
		t.Fatalf("absolute normalizeHistoricalPaths() = %v", paths)
	}

	for _, raw := range []string{
		root,
		filepath.Join(root, "..", "outside.md"),
		filepath.Join(root, ".git", "config"),
		"bad\npath",
		string([]byte{0xff}),
	} {
		if _, err := ws.normalizeHistoricalPaths([]string{raw}); err == nil {
			t.Fatalf("normalizeHistoricalPaths(%q) succeeded, want error", raw)
		}
	}

	t.Run("preserves current symlink lexically", func(t *testing.T) {
		testrepo.Write(t, root, "target.txt", "target\n")
		alias := filepath.Join(root, "alias.txt")
		if err := os.Symlink("target.txt", alias); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		paths, err := ws.normalizeHistoricalPaths([]string{alias})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(paths, []string{"alias.txt"}) {
			t.Fatalf("normalizeHistoricalPaths() = %v, want [alias.txt]", paths)
		}
	})

	t.Run("rebases symlinked worktree", func(t *testing.T) {
		alias := filepath.Join(t.TempDir(), "worktree-alias")
		if err := os.Symlink(root, alias); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		aliased := NewWorkspace(ws.repo, gitpkg.Client{Path: "git"}, alias)
		paths, err := aliased.normalizeHistoricalPaths([]string{filepath.Join(alias, "PLAN.md")})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(paths, []string{"PLAN.md"}) {
			t.Fatalf("normalizeHistoricalPaths() = %v, want [PLAN.md]", paths)
		}
	})
}

func TestAddInitializesWithoutCommitting(t *testing.T) {
	ws, root := newBareWorkspace(t)
	testrepo.Write(t, root, "PLAN.md", "draft\n")

	result, err := ws.Add(context.Background(), []string{"./PLAN.md"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Added, []string{"PLAN.md"}) {
		t.Fatalf("Add() added = %v, want [PLAN.md]", result.Added)
	}
	if len(result.AlreadyOwned) != 0 {
		t.Fatalf("Add() already owned = %v, want empty", result.AlreadyOwned)
	}
	if len(result.ReleasedCovered) != 0 {
		t.Fatalf("Add() released covered = %v, want empty", result.ReleasedCovered)
	}
	if _, err := os.Stat(ws.repo.HistoryDir); err != nil {
		t.Fatalf("history dir stat = %v", err)
	}
	if _, err := os.Stat(ws.repo.RegistryPath); err != nil {
		t.Fatalf("registry stat = %v", err)
	}
	if _, err := os.Stat(ws.repo.HooksDir); err != nil {
		t.Fatalf("hooks dir stat = %v", err)
	}
	if _, err := os.Stat(ws.repo.AttributesPath); err != nil {
		t.Fatalf("attributes file stat = %v", err)
	}
	owned, err := registry.Load(ws.repo.RegistryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(owned.Paths, []string{"PLAN.md"}) {
		t.Fatalf("registry paths = %v, want [PLAN.md]", owned.Paths)
	}
	contents := testrepo.Read(t, root, ".git/info/exclude")
	if !strings.Contains(contents, "/PLAN.md") {
		t.Fatalf("exclude file = %q, want /PLAN.md", contents)
	}
	hasHead, err := ws.hasHead(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hasHead {
		t.Fatal("Add() created a commit")
	}

	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}

func TestAddRollsBackNewMetadataOnInitialSaveFailure(t *testing.T) {
	ws, root := newBareWorkspace(t)
	testrepo.Write(t, root, "PLAN.md", "draft\n")
	originalExclude := "keep me\n"
	testrepo.Write(t, root, ".git/info/exclude", originalExclude)

	oldSave := saveRegistry
	saveRegistry = func(filename string, owned registry.Registry) error {
		if err := os.MkdirAll(ws.repo.FrigoDir, 0o700); err != nil {
			return err
		}
		if err := os.MkdirAll(ws.repo.HooksDir, 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(ws.repo.AttributesPath, []byte("partial\n"), 0o600); err != nil {
			return err
		}
		if err := os.MkdirAll(ws.repo.HistoryDir, 0o700); err != nil {
			return err
		}
		return errors.New("forced save failure")
	}
	t.Cleanup(func() { saveRegistry = oldSave })

	_, err := ws.Add(context.Background(), []string{"PLAN.md"})
	if err == nil || !strings.Contains(err.Error(), "forced save failure") {
		t.Fatalf("Add() error = %v", err)
	}
	if _, statErr := os.Stat(ws.repo.FrigoDir); !os.IsNotExist(statErr) {
		t.Fatalf("frigo metadata remains after rollback: %v", statErr)
	}
	contents := testrepo.Read(t, root, ".git/info/exclude")
	if got := contents; got != originalExclude {
		t.Fatalf("exclude file = %q, want %q", got, originalExclude)
	}
}

func TestAddRefusesPreexistingFrigoDirWithoutMetadata(t *testing.T) {
	ws, root := newBareWorkspace(t)
	testrepo.Write(t, root, "PLAN.md", "draft\n")
	if err := os.MkdirAll(ws.repo.FrigoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.repo.FrigoDir, "keep.txt"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := ws.Add(context.Background(), []string{"PLAN.md"})
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("Add() error = %v, want incomplete metadata", err)
	}
	if _, statErr := os.Stat(filepath.Join(ws.repo.FrigoDir, "keep.txt")); statErr != nil {
		t.Fatalf("preexisting frigo dir content removed: %v", statErr)
	}
}

func TestAddReturnsNormalizedAlreadyOwnedPaths(t *testing.T) {
	ws, root := newWorkspace(t)
	if err := os.MkdirAll(filepath.Join(root, "docs", "local"), 0o755); err != nil {
		t.Fatal(err)
	}
	ownForTest(t, ws, "docs/local")

	result, err := ws.Add(context.Background(), []string{"./docs/local"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Added) != 0 {
		t.Fatalf("Add() added = %v, want empty", result.Added)
	}
	want := map[string]string{"docs/local": "docs/local"}
	if len(result.AlreadyOwned) != len(want) {
		t.Fatalf("Add() already owned = %v, want %v", result.AlreadyOwned, want)
	}
	for path, covering := range want {
		if got, ok := result.AlreadyOwned[path]; !ok || got != covering {
			t.Fatalf("Add() already owned = %v, want %v", result.AlreadyOwned, want)
		}
	}
}

func TestAddRejectsMainTrackedPaths(t *testing.T) {
	ws, _ := newBareWorkspace(t)

	_, err := ws.Add(context.Background(), []string{"README.md"})
	if err == nil || !strings.Contains(err.Error(), "tracked by the main repository") {
		t.Fatalf("Add() error = %v", err)
	}
	if _, statErr := os.Stat(ws.repo.RegistryPath); !os.IsNotExist(statErr) {
		t.Fatalf("registry remains after tracked add: %v", statErr)
	}
	if _, statErr := os.Stat(ws.repo.HistoryDir); !os.IsNotExist(statErr) {
		t.Fatalf("history remains after tracked add: %v", statErr)
	}
}

func TestAddTreatsEmptyDirectoryAsIgnored(t *testing.T) {
	ws, root := newBareWorkspace(t)
	if err := os.MkdirAll(filepath.Join(root, "notes", "private"), 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := ws.Add(context.Background(), []string{"notes/private"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Added, []string{"notes/private"}) {
		t.Fatalf("Add() added = %v, want [notes/private]", result.Added)
	}
	if got, err := ws.Status(context.Background(), nil); err != nil {
		t.Fatalf("Status() error = %v", err)
	} else if got != "" {
		t.Fatalf("Status() = %q, want clean", got)
	}
	if got, err := ws.Diff(context.Background(), nil); err != nil {
		t.Fatalf("Diff() error = %v", err)
	} else if got != "" {
		t.Fatalf("Diff() = %q, want clean", got)
	}
	testrepo.Run(t, root, "check-ignore", "--quiet", "--no-index", "--", "notes/private")
}

func TestAddRollsBackNewMetadataOnConfigFailure(t *testing.T) {
	ws, root := newBareWorkspace(t)
	testrepo.Write(t, root, "PLAN.md", "draft\n")
	failing := NewWorkspace(ws.repo, failingGitClient(t, ws.repo.HistoryDir, "config", ""), root)

	_, err := failing.Add(context.Background(), []string{"PLAN.md"})
	if err == nil || !strings.Contains(err.Error(), "forced git failure") {
		t.Fatalf("Add() error = %v", err)
	}
	if _, statErr := os.Stat(ws.repo.FrigoDir); !os.IsNotExist(statErr) {
		t.Fatalf("frigo metadata remains after rollback: %v", statErr)
	}
	contents := testrepo.Read(t, root, ".git/info/exclude")
	if strings.Contains(contents, "/PLAN.md") {
		t.Fatalf("exclude file still contains PLAN.md after rollback: %q", contents)
	}
}

func TestAddRollsBackRegistryAndIgnoreOnVisibilityCheckFailure(t *testing.T) {
	ws, root := newWorkspace(t)
	ownForTest(t, ws, "docs/local")
	owned, err := registry.Load(ws.repo.RegistryPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ignore.Sync(ws.repo, owned); err != nil {
		t.Fatal(err)
	}
	testrepo.Write(t, root, "PLAN.md", "draft\n")
	failing := NewWorkspace(ws.repo, failingGitClient(t, "", "ls-files", "--others"), root)

	_, err = failing.Add(context.Background(), []string{"PLAN.md"})
	if err == nil || !strings.Contains(err.Error(), "forced git failure") {
		t.Fatalf("Add() error = %v", err)
	}
	owned, err = registry.Load(ws.repo.RegistryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(owned.Paths, []string{"docs/local"}) {
		t.Fatalf("registry paths after rollback = %v, want [docs/local]", owned.Paths)
	}
	text := testrepo.Read(t, root, ".git/info/exclude")
	if !strings.Contains(text, "/docs/local") {
		t.Fatalf("exclude file lost original path after rollback: %q", text)
	}
	if strings.Contains(text, "/PLAN.md") {
		t.Fatalf("exclude file kept new path after rollback: %q", text)
	}
	if _, statErr := os.Stat(ws.repo.HistoryDir); statErr != nil {
		t.Fatalf("existing history missing after rollback: %v", statErr)
	}
}

func TestDiffShowsNewOwnedFileWithoutPersistentIndex(t *testing.T) {
	ws, root := newWorkspace(t)
	ownForTest(t, ws, "PLAN.md")
	testrepo.Write(t, root, "PLAN.md", "draft\n")

	diff, err := ws.Diff(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "+draft") {
		t.Fatalf("diff = %q", diff)
	}

	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}

func TestPrivateAttributesPreserveBytesAndDiff(t *testing.T) {
	t.Run("existing history", func(t *testing.T) {
		ws, root := newWorkspace(t)
		runPrivateAttributesScenario(t, ws, root, false)
	})

	t.Run("new history", func(t *testing.T) {
		ws, root := newBareWorkspace(t)
		runPrivateAttributesScenario(t, ws, root, true)
	})
}

func TestCommitSelectedPathLeavesOtherOwnedChangeUncommitted(t *testing.T) {
	ws, root := workspaceWithOwnership(t, "PLAN.md", "NOTES.md")
	testrepo.Write(t, root, "PLAN.md", "plan\n")
	testrepo.Write(t, root, "NOTES.md", "notes\n")

	result, err := ws.Commit(context.Background(), CommitOptions{
		Message: "add plan",
		Paths:   []string{"PLAN.md"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Committed || result.Commit == "" {
		t.Fatalf("Commit() = %+v, want committed result", result)
	}

	tree, err := ws.privateOutput(context.Background(), ws.git.WithEnv("GIT_ATTR_NOSYSTEM=1"), "ls-tree", "-r", "--name-only", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if tree != "PLAN.md" {
		t.Fatalf("HEAD files = %q", tree)
	}
	diff, err := ws.Diff(context.Background(), []string{"NOTES.md"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "+notes") {
		t.Fatalf("diff = %q", diff)
	}

	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}

func TestCommitAllIncludesDirectoryChildrenAndDeletions(t *testing.T) {
	ws, root := workspaceWithOwnership(t, "docs/local")
	testrepo.Write(t, root, "docs/local/old.md", "old\n")
	saveForTest(t, ws, "save docs")
	if err := os.Remove(filepath.Join(root, "docs/local/old.md")); err != nil {
		t.Fatal(err)
	}
	testrepo.Write(t, root, "docs/local/sub/new.md", "new\n")

	result, err := ws.Commit(context.Background(), CommitOptions{Message: "update docs", All: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Committed || result.Commit == "" {
		t.Fatalf("Commit() = %+v, want committed result", result)
	}

	tree, err := ws.privateOutput(context.Background(), ws.git.WithEnv("GIT_ATTR_NOSYSTEM=1"), "ls-tree", "-r", "--name-only", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if tree != "docs/local/sub/new.md" {
		t.Fatalf("HEAD files = %q", tree)
	}

	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}

func TestCommitRejectsEmptyMessage(t *testing.T) {
	ws, _ := newWorkspace(t)

	_, err := ws.Commit(context.Background(), CommitOptions{Paths: []string{"PLAN.md"}})
	if err == nil || !strings.Contains(err.Error(), "commit message cannot be empty") {
		t.Fatalf("Commit() error = %v", err)
	}
}

func TestCommitRejectsMissingScopeWithoutAll(t *testing.T) {
	ws, _ := newWorkspace(t)

	_, err := ws.Commit(context.Background(), CommitOptions{Message: "save"})
	if err == nil || !strings.Contains(err.Error(), "no paths specified") {
		t.Fatalf("Commit() error = %v", err)
	}
}

func TestCommitRejectsAllWithPaths(t *testing.T) {
	ws, _ := newWorkspace(t)

	_, err := ws.Commit(context.Background(), CommitOptions{
		Message: "save",
		All:     true,
		Paths:   []string{"PLAN.md"},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot combine -a with commit paths") {
		t.Fatalf("Commit() error = %v", err)
	}
}

func TestCommitReturnsNotCommittedWhenScopeUnchanged(t *testing.T) {
	ws, root := workspaceWithOwnership(t, "PLAN.md")
	testrepo.Write(t, root, "PLAN.md", "saved\n")
	saveForTest(t, ws, "save plan")

	result, err := ws.Commit(context.Background(), CommitOptions{
		Message: "save plan again",
		Paths:   []string{"PLAN.md"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Committed || result.Commit != "" {
		t.Fatalf("Commit() = %+v, want not committed", result)
	}

	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}

func TestCommitUsesEffectiveMainRepositoryIdentity(t *testing.T) {
	ws, root := workspaceWithOwnership(t, "PLAN.md")
	testrepo.Write(t, root, "PLAN.md", "plan\n")
	testrepo.Run(t, root, "config", "user.name", "Repo Name")
	testrepo.Run(t, root, "config", "user.email", "repo@example.invalid")
	ws.git = ws.git.WithEnv(
		"GIT_AUTHOR_NAME=Env Author Name",
		"GIT_AUTHOR_EMAIL=author@example.invalid",
		"GIT_COMMITTER_NAME=Env Committer Name",
		"GIT_COMMITTER_EMAIL=committer@example.invalid",
	)

	result, err := ws.Commit(context.Background(), CommitOptions{
		Message: "add plan",
		Paths:   []string{"PLAN.md"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Committed || result.Commit == "" {
		t.Fatalf("Commit() = %+v, want committed result", result)
	}

	header, err := ws.privateOutput(context.Background(), ws.git.WithEnv("GIT_ATTR_NOSYSTEM=1"), "show", "-s", "--format=%an <%ae>|%cn <%ce>", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := header, "Env Author Name <author@example.invalid>|Env Committer Name <committer@example.invalid>"; got != want {
		t.Fatalf("private HEAD identity = %q, want %q", got, want)
	}

	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}

func TestCommitRemovedTemporaryIndexOnIdentityFailure(t *testing.T) {
	ws, root := workspaceWithOwnership(t, "PLAN.md")
	testrepo.Write(t, root, "PLAN.md", "plan\n")
	testrepo.Run(t, root, "config", "--unset", "user.name")
	testrepo.Run(t, root, "config", "--unset", "user.email")
	home := t.TempDir()
	ws.git = ws.git.WithEnv(
		"HOME="+home,
		"XDG_CONFIG_HOME="+home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=",
		"GIT_AUTHOR_EMAIL=",
		"GIT_COMMITTER_NAME=",
		"GIT_COMMITTER_EMAIL=",
		"EMAIL=",
	)

	_, err := ws.Commit(context.Background(), CommitOptions{
		Message: "add plan",
		Paths:   []string{"PLAN.md"},
	})
	if err == nil {
		t.Fatal("Commit() error = nil, want identity failure")
	}

	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}

func TestTemporaryIndexRemovedOnFailure(t *testing.T) {
	ws, root := newWorkspace(t)
	ownForTest(t, ws, "PLAN.md")
	testrepo.Write(t, root, "PLAN.md", "draft\n")

	base, err := ws.resolveHistoryBase(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("boom")
	err = ws.withTemporaryIndexAt(context.Background(), base, []string{"PLAN.md"}, func(client gitpkg.Client) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("withTemporaryIndexAt() error = %v, want %v", err, wantErr)
	}

	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}

func TestTemporaryIndexRemovedOnCloseFailure(t *testing.T) {
	ws, root := newWorkspace(t)
	ownForTest(t, ws, "PLAN.md")
	testrepo.Write(t, root, "PLAN.md", "draft\n")

	oldClose := closeTemporaryIndex
	closeTemporaryIndex = func(file *os.File) error {
		_ = file.Close()
		return errors.New("close failed")
	}
	t.Cleanup(func() { closeTemporaryIndex = oldClose })

	base, err := ws.resolveHistoryBase(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = ws.withTemporaryIndexAt(context.Background(), base, []string{"PLAN.md"}, func(client gitpkg.Client) error {
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "close temporary index") {
		t.Fatalf("withTemporaryIndexAt() error = %v, want close temporary index error", err)
	}

	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}

func TestTemporaryIndexRemovedOnRemoveFailure(t *testing.T) {
	ws, root := newWorkspace(t)
	ownForTest(t, ws, "PLAN.md")
	testrepo.Write(t, root, "PLAN.md", "draft\n")

	oldRemove := removeTemporaryIndex
	calls := 0
	removeTemporaryIndex = func(name string) error {
		calls++
		if calls == 1 {
			return errors.New("remove failed")
		}
		return oldRemove(name)
	}
	t.Cleanup(func() { removeTemporaryIndex = oldRemove })

	base, err := ws.resolveHistoryBase(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = ws.withTemporaryIndexAt(context.Background(), base, []string{"PLAN.md"}, func(client gitpkg.Client) error {
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "remove temporary index") {
		t.Fatalf("withTemporaryIndexAt() error = %v, want remove temporary index error", err)
	}
	if calls < 2 {
		t.Fatalf("removeTemporaryIndex() calls = %d, want cleanup retry", calls)
	}

	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}

func TestHasHeadReturnsErrorOnBrokenHistory(t *testing.T) {
	ws, _ := newWorkspace(t)
	if err := os.WriteFile(filepath.Join(ws.repo.HistoryDir, "HEAD"), []byte("not-a-ref\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	hasHead, err := ws.hasHead(context.Background())
	if err == nil {
		t.Fatalf("hasHead() = %v, want corruption error", hasHead)
	}
	if hasHead {
		t.Fatal("hasHead() = true, want false on broken history")
	}
}

func TestStatusScopesToOwnedPaths(t *testing.T) {
	ws, root := newWorkspace(t)
	ownForTest(t, ws, "docs/local")
	testrepo.Write(t, root, "docs/local/plan.md", "draft\n")
	testrepo.Write(t, root, "docs/other.md", "ignore\n")

	status, err := ws.Status(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "docs/local/plan.md") {
		t.Fatalf("status = %q", status)
	}
	if strings.Contains(status, "docs/other.md") {
		t.Fatalf("status leaked unowned path: %q", status)
	}

	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}

func TestStatusUsesResolvedHistoryBaseAfterExternalRefUpdate(t *testing.T) {
	ws, root := committedWorkspace(t, "PLAN.md", "base\n")
	base, winner := createWinnerAndRestoreBase(t, ws, root, "PLAN.md", "winner\n")

	changing := NewWorkspace(ws.repo, externalHeadChangeGitClient(t, ws.repo.HistoryDir, winner, base, "status,diff-index"), root)
	status, err := changing.Status(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if status != " M PLAN.md" {
		t.Fatalf("Status() = %q, want worktree change against captured base", status)
	}
	assertHistoryHead(t, ws, winner)
	assertNoTemporaryIndexes(t, changing)
}

func TestDiffRejectsUnownedPath(t *testing.T) {
	ws, _ := newWorkspace(t)
	ownForTest(t, ws, "docs/local")

	_, err := ws.Diff(context.Background(), []string{"README.md"})
	if err == nil || !strings.Contains(err.Error(), "not owned by frigo") {
		t.Fatalf("Diff() error = %v", err)
	}
}

func TestDiffRejectsOutsideAndGitMetadataPaths(t *testing.T) {
	ws, root := newWorkspace(t)
	ownForTest(t, ws, "docs/local")

	outside := filepath.Join(filepath.Dir(root), "outside.md")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Diff(context.Background(), []string{outside}); err == nil || !strings.Contains(err.Error(), "outside the Git worktree") {
		t.Fatalf("outside Diff() error = %v", err)
	}
	if _, err := ws.Diff(context.Background(), []string{".git"}); err == nil || !strings.Contains(err.Error(), "Git metadata") {
		t.Fatalf(".git Diff() error = %v", err)
	}
}

func TestShowReportsLatestCommitAndFullPatch(t *testing.T) {
	ws, root := newWorkspace(t)
	ownForTest(t, ws, "NOTES.md", "PLAN.md")
	testrepo.Write(t, root, "NOTES.md", "notes body\n")
	testrepo.Write(t, root, "PLAN.md", "plan body\n")
	saveForTest(t, ws, "save both files")

	output, err := ws.Show(context.Background(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"save both files", "diff --git a/NOTES.md b/NOTES.md", "diff --git a/PLAN.md b/PLAN.md", "+notes body", "+plan body"} {
		if !strings.Contains(output, want) {
			t.Fatalf("Show() output missing %q:\n%s", want, output)
		}
	}
}

func TestShowSelectsAbbreviatedCommit(t *testing.T) {
	ws, root := newWorkspace(t)
	ownForTest(t, ws, "PLAN.md")
	testrepo.Write(t, root, "PLAN.md", "first body\n")
	saveForTest(t, ws, "first snapshot")
	first, err := ws.resolveHistoryBase(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	testrepo.Write(t, root, "PLAN.md", "second body\n")
	saveForTest(t, ws, "second snapshot")

	output, err := ws.Show(context.Background(), first.OID[:8], nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "first snapshot") || !strings.Contains(output, "+first body") {
		t.Fatalf("Show() = %q, want first commit", output)
	}
	if strings.Contains(output, "second snapshot") || strings.Contains(output, "second body") {
		t.Fatalf("Show() = %q, unexpectedly contains second commit", output)
	}
}

func TestShowFiltersHistoricalPaths(t *testing.T) {
	ws, root := newWorkspace(t)
	ownForTest(t, ws, "NOTES.md", "PLAN.md")
	testrepo.Write(t, root, "NOTES.md", "notes body\n")
	testrepo.Write(t, root, "PLAN.md", "plan body\n")
	saveForTest(t, ws, "save both files")

	output, err := ws.Show(context.Background(), "", []string{"PLAN.md"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "diff --git a/PLAN.md b/PLAN.md") || !strings.Contains(output, "+plan body") {
		t.Fatalf("Show() = %q, want PLAN.md patch", output)
	}
	if strings.Contains(output, "NOTES.md") || strings.Contains(output, "notes body") {
		t.Fatalf("Show() = %q, unexpectedly contains NOTES.md", output)
	}
}

func TestShowReportsNoSavedHistory(t *testing.T) {
	ws, _ := newWorkspace(t)
	output, err := ws.Show(context.Background(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if output != "no saved history" {
		t.Fatalf("Show() = %q, want no saved history", output)
	}
}

func TestShowRejectsInvalidRevision(t *testing.T) {
	ws, _ := committedWorkspace(t, "PLAN.md", "saved\n")
	for _, revision := range []string{"missing-commit", "-n1"} {
		_, err := ws.Show(context.Background(), revision, nil)
		if err == nil || !strings.Contains(err.Error(), revision) {
			t.Fatalf("Show(%q) error = %v, want revision in error", revision, err)
		}
	}
}

func TestShowPreservesRevisionResolutionFailure(t *testing.T) {
	ws, root := committedWorkspace(t, "PLAN.md", "saved\n")
	failing := NewWorkspace(
		ws.repo,
		failingGitClientWithStderr(t, ws.repo.HistoryDir, "rev-parse", "HEAD~0^{commit}", "injected revision failure"),
		root,
	)

	_, err := failing.Show(context.Background(), "HEAD~0", nil)
	if err == nil || !strings.Contains(err.Error(), "resolve frigo revision") || !strings.Contains(err.Error(), "injected revision failure") {
		t.Fatalf("Show() error = %v, want preserved revision resolution failure", err)
	}
}

func TestShowViewsReleasedHistoricalPath(t *testing.T) {
	ws, _ := committedWorkspace(t, "PLAN.md", "saved body\n")
	if _, err := ws.Release(context.Background(), []string{"PLAN.md"}, false); err != nil {
		t.Fatal(err)
	}

	output, err := ws.Show(context.Background(), "", []string{"PLAN.md"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "diff --git a/PLAN.md b/PLAN.md") || !strings.Contains(output, "+saved body") {
		t.Fatalf("Show() = %q, want released PLAN.md history", output)
	}
}

func TestShowUsesLiteralHistoricalPathspecs(t *testing.T) {
	ws, root := newWorkspace(t)
	paths := []string{"space plan.md", "literal[1].md", "star*.md", "question?.md"}
	ownForTest(t, ws, paths...)
	for _, path := range paths {
		testrepo.Write(t, root, path, path+"\n")
	}
	saveForTest(t, ws, "save literal names")

	output, err := ws.Show(context.Background(), "", []string{"literal[1].md"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "literal[1].md") {
		t.Fatalf("Show() = %q, want literal path", output)
	}
	for _, excluded := range []string{"space plan.md", "star*.md", "question?.md"} {
		if strings.Contains(output, excluded) {
			t.Fatalf("Show() = %q, unexpectedly contains %q", output, excluded)
		}
	}
}

func TestShowUsesResolvedCommitAfterExternalRefUpdate(t *testing.T) {
	ws, root := committedWorkspace(t, "PLAN.md", "base\n")
	base, winner := createWinnerAndRestoreBase(t, ws, root, "PLAN.md", "winner\n")

	changing := NewWorkspace(ws.repo, externalHeadChangeGitClient(t, ws.repo.HistoryDir, winner, base, "show"), root)
	output, err := changing.Show(context.Background(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "save PLAN.md") || strings.Contains(output, "winner snapshot") {
		t.Fatalf("Show() = %q, want captured base commit", output)
	}
	assertHistoryHead(t, ws, winner)
}

func TestLogReportsSavedHistory(t *testing.T) {
	ws, root := newWorkspace(t)
	ownForTest(t, ws, "PLAN.md")
	testrepo.Write(t, root, "PLAN.md", "saved\n")
	saveForTest(t, ws, "save plan")

	log, err := ws.Log(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log, "save plan") {
		t.Fatalf("Log() = %q", log)
	}
}

func TestLogUsesResolvedHistoryBaseAfterExternalRefUpdate(t *testing.T) {
	ws, root := committedWorkspace(t, "PLAN.md", "base\n")
	base, winner := createWinnerAndRestoreBase(t, ws, root, "PLAN.md", "winner\n")

	changing := NewWorkspace(ws.repo, externalHeadChangeGitClient(t, ws.repo.HistoryDir, winner, base, "log"), root)
	log, err := changing.Log(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log, "save PLAN.md") || strings.Contains(log, "winner snapshot") {
		t.Fatalf("Log() = %q, want history ending at captured base", log)
	}
	assertHistoryHead(t, ws, winner)
}

func TestLogReportsNoSavedHistoryWithoutHead(t *testing.T) {
	ws, _ := newWorkspace(t)

	log, err := ws.Log(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if log != "no saved history" {
		t.Fatalf("Log() = %q, want no saved history", log)
	}
}

func TestReleaseDirtyPathRequiresForce(t *testing.T) {
	ws, root := committedWorkspace(t, "PLAN.md", "saved\n")
	testrepo.Write(t, root, "PLAN.md", "changed\n")

	_, err := ws.Release(context.Background(), []string{"PLAN.md"}, false)
	if err == nil || !strings.Contains(err.Error(), "uncommitted frigo changes") {
		t.Fatalf("Release() error = %v", err)
	}
	owned, loadErr := registry.Load(ws.repo.RegistryPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if !owned.OwnsExact("PLAN.md") {
		t.Fatal("dirty path was released")
	}

	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}

func TestReleaseDirtyCheckUsesResolvedHistoryBaseAfterExternalRefUpdate(t *testing.T) {
	ws, root := committedWorkspace(t, "PLAN.md", "base\n")
	base, winner := createWinnerAndRestoreBase(t, ws, root, "PLAN.md", "winner\n")
	testrepo.Write(t, root, "PLAN.md", "base\n")

	changing := NewWorkspace(ws.repo, externalHeadChangeGitClient(t, ws.repo.HistoryDir, winner, base, "status,diff-index"), root)
	result, err := changing.Release(context.Background(), []string{"PLAN.md"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Released, []string{"PLAN.md"}) {
		t.Fatalf("Release() = %#v, want captured-base-clean release", result)
	}
	assertHistoryHead(t, ws, winner)
	assertNoTemporaryIndexes(t, changing)
}

func TestReleaseAndRestoreExactPathWithSpacesAndMetacharacters(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("magic filenames are not supported on Windows")
	}
	name := "  [keep]*?.md  "
	ws, root := committedWorkspace(t, name, "saved\n")
	testrepo.Write(t, root, name, "changed\n")

	_, err := ws.Release(context.Background(), []string{name}, false)
	if err == nil || !strings.Contains(err.Error(), "uncommitted frigo changes") {
		t.Fatalf("Release() error = %v, want dirty refusal", err)
	}
	owned, err := registry.Load(ws.repo.RegistryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !owned.OwnsExact(name) {
		t.Fatalf("registry lost exact ownership of %q", name)
	}

	restored, err := ws.Restore(context.Background(), []string{name})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(restored, []string{name}) {
		t.Fatalf("Restore() = %v, want %q", restored, name)
	}
	if got := testrepo.Read(t, root, name); got != "saved\n" {
		t.Fatalf("restored file = %q, want saved content", got)
	}

	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}

func TestReleaseForceRemovesExactOwnershipAndPreservesPhysicalFiles(t *testing.T) {
	ws, root := committedWorkspace(t, "PLAN.md", "saved\n")
	testrepo.Write(t, root, "PLAN.md", "changed\n")

	result, err := ws.Release(context.Background(), []string{"PLAN.md"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Released, []string{"PLAN.md"}) {
		t.Fatalf("Release() released = %v, want [PLAN.md]", result.Released)
	}
	if len(result.Missing) != 0 {
		t.Fatalf("Release() missing = %v, want empty", result.Missing)
	}
	owned, err := registry.Load(ws.repo.RegistryPath)
	if err != nil {
		t.Fatal(err)
	}
	if owned.OwnsExact("PLAN.md") {
		t.Fatal("forced release kept exact ownership")
	}
	if got := testrepo.Read(t, root, "PLAN.md"); got != "changed\n" {
		t.Fatalf("PLAN.md = %q, want changed content preserved", got)
	}
	contents := testrepo.Read(t, root, ".git/info/exclude")
	if strings.Contains(contents, "/PLAN.md") {
		t.Fatalf("exclude file still contains PLAN.md after release: %q", contents)
	}

	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}

func TestReleaseRequiresExactOwnedRoots(t *testing.T) {
	ws, root := newWorkspace(t)
	ownForTest(t, ws, "docs/local")
	syncIgnoreForTest(t, ws)
	testrepo.Write(t, root, "docs/local/plan.md", "saved\n")
	saveForTest(t, ws, "save docs")

	_, err := ws.Release(context.Background(), []string{"docs/local/plan.md"}, false)
	if err == nil || !strings.Contains(err.Error(), "exact owned frigo root") {
		t.Fatalf("Release() error = %v", err)
	}
	owned, loadErr := registry.Load(ws.repo.RegistryPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if !owned.OwnsExact("docs/local") {
		t.Fatal("covered child released parent root")
	}
}

func TestReleaseFinalRootRetainsHistory(t *testing.T) {
	ws, root := committedWorkspace(t, "PLAN.md", "saved\n")

	result, err := ws.Release(context.Background(), []string{"PLAN.md"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Released, []string{"PLAN.md"}) {
		t.Fatalf("Release() released = %v, want [PLAN.md]", result.Released)
	}
	owned, err := registry.Load(ws.repo.RegistryPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(owned.Paths) != 0 {
		t.Fatalf("registry paths = %v, want empty", owned.Paths)
	}
	if _, err := os.Stat(ws.repo.FrigoDir); err != nil {
		t.Fatalf("frigo dir stat = %v", err)
	}
	if _, err := os.Stat(ws.repo.HistoryDir); err != nil {
		t.Fatalf("history dir stat = %v", err)
	}
	hasHead, err := ws.hasHead(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !hasHead {
		t.Fatal("Release() removed private HEAD")
	}
	log, err := ws.Log(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log, "save PLAN.md") {
		t.Fatalf("Log() = %q", log)
	}
	if got := testrepo.Read(t, root, "PLAN.md"); got != "saved\n" {
		t.Fatalf("PLAN.md = %q, want physical file preserved", got)
	}

	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}

func TestReleaseAllPreflightsEveryOwnedRootBeforeMutating(t *testing.T) {
	ws, root := newWorkspace(t)
	ownForTest(t, ws, "PLAN.md", "NOTES.md")
	syncIgnoreForTest(t, ws)
	testrepo.Write(t, root, "PLAN.md", "saved plan\n")
	testrepo.Write(t, root, "NOTES.md", "saved notes\n")
	saveForTest(t, ws, "save owned files")
	testrepo.Write(t, root, "NOTES.md", "dirty notes\n")

	_, err := ws.ReleaseAll(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "uncommitted frigo changes") {
		t.Fatalf("ReleaseAll() error = %v", err)
	}
	owned, loadErr := registry.Load(ws.repo.RegistryPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if !slices.Equal(owned.Paths, []string{"NOTES.md", "PLAN.md"}) {
		t.Fatalf("registry paths = %v, want both roots retained", owned.Paths)
	}
	if got := testrepo.Read(t, root, "PLAN.md"); got != "saved plan\n" {
		t.Fatalf("PLAN.md = %q, want saved content preserved", got)
	}
	if got := testrepo.Read(t, root, "NOTES.md"); got != "dirty notes\n" {
		t.Fatalf("NOTES.md = %q, want dirty content preserved", got)
	}
	contents := testrepo.Read(t, root, ".git/info/exclude")
	if !strings.Contains(contents, "/PLAN.md") || !strings.Contains(contents, "/NOTES.md") {
		t.Fatalf("exclude file lost owned paths after failed release all: %q", contents)
	}

	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}

func TestReleaseAllReleasesCurrentWorktreeOnlyAndKeepsPointerManifestHistory(t *testing.T) {
	ws, mainRoot, linkedRoot := newLinkedWorkspace(t)
	mainRepo, err := repository.Discover(context.Background(), gitpkg.Client{Path: "git"}, mainRoot)
	if err != nil {
		t.Fatal(err)
	}
	mainWS := NewWorkspace(mainRepo, gitpkg.Client{Path: "git"}, mainRoot)

	testrepo.Write(t, mainRoot, "main.local", "main\n")
	testrepo.Write(t, linkedRoot, "alpha.local", "alpha\n")
	testrepo.Write(t, linkedRoot, "beta.local", "beta\n")

	if _, err := mainWS.Add(context.Background(), []string{"main.local"}); err != nil {
		t.Fatal(err)
	}
	saveForTest(t, mainWS, "save main")
	if _, err := ws.Add(context.Background(), []string{"alpha.local", "beta.local"}); err != nil {
		t.Fatal(err)
	}
	saveForTest(t, ws, "save linked")
	id := linkedWorkspaceID(t, ws)

	result, err := ws.ReleaseAll(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Released, []string{"alpha.local", "beta.local"}) {
		t.Fatalf("ReleaseAll() released = %v, want both linked roots", result.Released)
	}
	linkedOwned, err := registry.Load(ws.repo.RegistryPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(linkedOwned.Paths) != 0 {
		t.Fatalf("linked registry paths = %v, want empty", linkedOwned.Paths)
	}
	mainOwned, err := registry.Load(mainWS.repo.RegistryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(mainOwned.Paths, []string{"main.local"}) {
		t.Fatalf("main registry paths = %v, want main worktree untouched", mainOwned.Paths)
	}
	if got := linkedManifest(t, ws); got.ID != id || got.WorktreePath != linkedRoot || got.LockOwned {
		t.Fatalf("linked manifest after release all = %+v, want preserved pointer and cleared lock", got)
	}
	lock, err := ws.inspectWorktreeLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if lock.exists {
		t.Fatalf("linked worktree lock remains after release all: %#v", lock)
	}
	if _, err := os.Stat(ws.repo.WorktreeIDPath); err != nil {
		t.Fatalf("linked pointer removed after release all: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws.repo.FrigoDir, manifestName)); err != nil {
		t.Fatalf("linked manifest removed after release all: %v", err)
	}
	if _, err := os.Stat(ws.repo.HistoryDir); err != nil {
		t.Fatalf("linked history removed after release all: %v", err)
	}
	contents, err := os.ReadFile(ws.repo.ExcludePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "/alpha.local") || strings.Contains(string(contents), "/beta.local") || !strings.Contains(string(contents), "/main.local") {
		t.Fatalf("exclude file after release all = %q, want only main worktree paths", contents)
	}
	log, err := ws.Log(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log, "save linked") {
		t.Fatalf("Log() = %q, want linked history preserved", log)
	}

	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}

func TestReleaseRollsBackRegistryOnExcludeFailure(t *testing.T) {
	ws, root := committedWorkspace(t, "PLAN.md", "saved\n")
	originalExclude := "# >>> frigo >>>\n# >>> frigo >>>\n"
	testrepo.Write(t, root, ".git/info/exclude", originalExclude)

	_, err := ws.Release(context.Background(), []string{"PLAN.md"}, false)
	if err == nil || !strings.Contains(err.Error(), "malformed frigo section") {
		t.Fatalf("Release() error = %v", err)
	}
	owned, loadErr := registry.Load(ws.repo.RegistryPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if !owned.OwnsExact("PLAN.md") {
		t.Fatal("registry not rolled back after exclude failure")
	}
	contents := testrepo.Read(t, root, ".git/info/exclude")
	if contents != originalExclude {
		t.Fatalf("exclude file = %q, want original malformed contents", contents)
	}
}

func TestRestoreRestoresSavedFilesFromHeadAndKeepsUnsavedNewFiles(t *testing.T) {
	ws, root := newWorkspace(t)
	ownForTest(t, ws, "docs/local")
	testrepo.Write(t, root, "docs/local/plan.md", "saved\n")
	saveForTest(t, ws, "save docs")
	testrepo.Write(t, root, "docs/local/plan.md", "changed\n")
	testrepo.Write(t, root, "docs/local/new.md", "new\n")

	restored, err := ws.Restore(context.Background(), []string{"docs/local"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(restored, []string{"docs/local"}) {
		t.Fatalf("Restore() = %v, want [docs/local]", restored)
	}
	if got := testrepo.Read(t, root, "docs/local/plan.md"); got != "saved\n" {
		t.Fatalf("restored plan = %q, want saved contents", got)
	}
	if got := testrepo.Read(t, root, "docs/local/new.md"); got != "new\n" {
		t.Fatalf("unsaved new file = %q, want preserved", got)
	}

	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}

func TestRestoreRejectsUnownedPath(t *testing.T) {
	ws, _ := newWorkspace(t)
	ownForTest(t, ws, "docs/local")

	_, err := ws.Restore(context.Background(), []string{"README.md"})
	if err == nil || !strings.Contains(err.Error(), "not owned by frigo") {
		t.Fatalf("Restore() error = %v", err)
	}
}

func TestRestoreRejectsBeforeFirstCommit(t *testing.T) {
	ws, root := newWorkspace(t)
	ownForTest(t, ws, "PLAN.md")
	testrepo.Write(t, root, "PLAN.md", "draft\n")

	_, err := ws.Restore(context.Background(), []string{"PLAN.md"})
	if err == nil || !strings.Contains(err.Error(), "no saved history") {
		t.Fatalf("Restore() error = %v", err)
	}

	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}

func TestRestorePreflightsSavedVersions(t *testing.T) {
	ws, root := newWorkspace(t)
	ownForTest(t, ws, "PLAN.md")
	testrepo.Write(t, root, "PLAN.md", "saved\n")
	saveForTest(t, ws, "save plan")
	ownForTest(t, ws, "NOTES.md")
	testrepo.Write(t, root, "PLAN.md", "changed\n")
	testrepo.Write(t, root, "NOTES.md", "new\n")

	_, err := ws.Restore(context.Background(), []string{"PLAN.md", "NOTES.md"})
	if err == nil || !strings.Contains(err.Error(), "NOTES.md has no saved version") {
		t.Fatalf("Restore() error = %v", err)
	}
	if got := testrepo.Read(t, root, "PLAN.md"); got != "changed\n" {
		t.Fatalf("PLAN.md = %q, want unchanged because restore should preflight", got)
	}
	if got := testrepo.Read(t, root, "NOTES.md"); got != "new\n" {
		t.Fatalf("NOTES.md = %q, want never-committed file preserved", got)
	}

	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}

func newWorkspace(t *testing.T) (*Workspace, string) {
	t.Helper()
	ws, root := newBareWorkspace(t)
	if err := initWorkspaceMetadata(t, ws.repo); err != nil {
		t.Fatal(err)
	}
	return ws, root
}

func workspaceWithOwnership(t *testing.T, paths ...string) (*Workspace, string) {
	t.Helper()
	ws, root := newWorkspace(t)
	ownForTest(t, ws, paths...)
	return ws, root
}

func committedWorkspace(t *testing.T, path, contents string) (*Workspace, string) {
	t.Helper()
	ws, root := newWorkspace(t)
	ownForTest(t, ws, path)
	syncIgnoreForTest(t, ws)
	testrepo.Write(t, root, path, contents)
	saveForTest(t, ws, "save "+path)
	return ws, root
}

func newBareWorkspace(t *testing.T) (*Workspace, string) {
	t.Helper()
	root := testrepo.Init(t)
	testrepo.Write(t, root, "README.md", "main\n")
	testrepo.CommitAll(t, root, "initial", "README.md")

	repo, err := repository.Discover(context.Background(), gitpkg.Client{Path: "git"}, root)
	if err != nil {
		t.Fatal(err)
	}
	return NewWorkspace(repo, gitpkg.Client{Path: "git"}, root), root
}

func initWorkspaceMetadata(t *testing.T, repo repository.Repository) error {
	t.Helper()
	if err := os.MkdirAll(repo.HooksDir, 0o700); err != nil {
		return err
	}
	testrepo.Write(t, repo.Root, filepath.Join(".git", "frigo", "attributes"), "")
	testrepo.Run(t, repo.Root, "init", "--bare", "--quiet", repo.HistoryDir)
	if err := registry.Save(repo.RegistryPath, registry.New()); err != nil {
		return fmt.Errorf("save test registry: %w", err)
	}
	return nil
}

func ownForTest(t *testing.T, ws *Workspace, paths ...string) {
	t.Helper()
	owned, err := registry.Load(ws.repo.RegistryPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owned.Add(paths...); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(ws.repo.RegistryPath, owned); err != nil {
		t.Fatal(err)
	}
	if err := ignore.Sync(ws.repo, owned); err != nil {
		t.Fatal(err)
	}
}

func saveForTest(t *testing.T, ws *Workspace, message string) {
	t.Helper()
	owned, err := registry.Load(ws.repo.RegistryPath)
	if err != nil {
		t.Fatal(err)
	}
	base, err := ws.resolveHistoryBase(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.withTemporaryIndexAt(context.Background(), base, owned.Paths, func(client gitpkg.Client) error {
		args := append([]string{"add", "-A", "--"}, owned.Paths...)
		if _, err := ws.privateOutput(context.Background(), client, args...); err != nil {
			return err
		}
		tree, err := ws.privateOutput(context.Background(), client, "write-tree")
		if err != nil {
			return err
		}
		commitArgs := []string{"commit-tree", tree, "-m", message}
		if base.Exists {
			commitArgs = []string{"commit-tree", tree, "-p", base.OID, "-m", message}
		}
		identityClient, err := ws.commitClient(context.Background(), client)
		if err != nil {
			return err
		}
		commit, err := ws.privateOutput(context.Background(), identityClient, commitArgs...)
		if err != nil {
			return err
		}
		_, err = ws.privateOutput(context.Background(), client, "update-ref", "HEAD", commit, base.OID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func syncIgnoreForTest(t *testing.T, ws *Workspace) {
	t.Helper()
	owned, err := registry.Load(ws.repo.RegistryPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ignore.Sync(ws.repo, owned); err != nil {
		t.Fatal(err)
	}
}

func runPrivateAttributesScenario(t *testing.T, ws *Workspace, root string, initialize bool) {
	t.Helper()

	const rootPath = "root.txt"
	const nestedPath = "nested/encoded.txt"

	rootOriginal := []byte("root line 1\r\n$Id$\r\nTAIL")
	rootChanged := []byte("root line 1\r\n$Id$\r\nCHANGED")
	nestedOriginal := utf16LE("nested line 1\nTAIL")
	nestedChanged := utf16LE("nested changed\nTAIL")

	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatalf("create nested directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "filter-sentinel.txt"), []byte("safe\n"), 0o644); err != nil {
		t.Fatalf("write filter sentinel: %v", err)
	}
	script := testexec.Build(t)
	testrepo.Run(t, root, "config", "filter.frigo-test.clean", script)
	testrepo.Run(t, root, "config", "filter.frigo-test.smudge", script)
	testrepo.Write(t, root, ".gitattributes", "root.txt text eol=crlf filter=frigo-test ident -diff\n")
	testrepo.Write(t, root, filepath.Join("nested", ".gitattributes"), "encoded.txt working-tree-encoding=UTF-16LE\n")
	if err := os.WriteFile(filepath.Join(root, rootPath), rootOriginal, 0o644); err != nil {
		t.Fatalf("write root file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, nestedPath), nestedOriginal, 0o644); err != nil {
		t.Fatalf("write nested file: %v", err)
	}

	if initialize {
		result, err := ws.Add(context.Background(), []string{rootPath, nestedPath})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Added) != 2 || !slices.Contains(result.Added, rootPath) || !slices.Contains(result.Added, nestedPath) {
			t.Fatalf("Add() added = %v, want both %s and %s", result.Added, rootPath, nestedPath)
		}
	} else {
		ownForTest(t, ws, rootPath, nestedPath)
	}

	result, err := ws.Commit(context.Background(), CommitOptions{All: true, Message: "save private attributes"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Committed || result.Commit == "" {
		t.Fatalf("Commit() = %+v, want committed result", result)
	}

	assertBlobBytes(t, ws, rootPath, rootOriginal)
	assertBlobBytes(t, ws, nestedPath, nestedOriginal)

	if err := os.WriteFile(filepath.Join(root, rootPath), rootChanged, 0o644); err != nil {
		t.Fatalf("rewrite root file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, nestedPath), nestedChanged, 0o644); err != nil {
		t.Fatalf("rewrite nested file: %v", err)
	}

	diff, err := ws.Diff(context.Background(), []string{rootPath})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(diff, "Binary files differ") {
		t.Fatalf("diff = %q", diff)
	}
	if !strings.Contains(diff, "@@") {
		t.Fatalf("diff = %q", diff)
	}
	if !strings.Contains(diff, "CHANGED") {
		t.Fatalf("diff = %q", diff)
	}

	restored, err := ws.Restore(context.Background(), []string{rootPath, nestedPath})
	if err != nil {
		t.Fatal(err)
	}
	wantRestored := []string{rootPath, nestedPath}
	slices.Sort(restored)
	slices.Sort(wantRestored)
	if !slices.Equal(restored, wantRestored) {
		t.Fatalf("Restore() = %v, want [%s %s]", restored, rootPath, nestedPath)
	}

	assertFileBytes(t, root, rootPath, rootOriginal)
	assertFileBytes(t, root, nestedPath, nestedOriginal)
	if got := testrepo.Read(t, root, "filter-sentinel.txt"); got != "safe\n" {
		t.Fatalf("filter sentinel = %q, want unchanged safe sentinel", got)
	}

	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}

func assertBlobBytes(t *testing.T, ws *Workspace, path string, want []byte) {
	t.Helper()
	got, err := ws.privateOutput(context.Background(), ws.git, "show", "HEAD:"+path)
	if err != nil {
		t.Fatalf("show %s: %v", path, err)
	}
	if !slices.Equal([]byte(got), want) {
		t.Fatalf("blob %s = %x, want %x", path, []byte(got), want)
	}
}

func assertFileBytes(t *testing.T, root, rel string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("file %s = %x, want %x", rel, got, want)
	}
}

func utf16LE(text string) []byte {
	encoded := utf16.Encode([]rune(text))
	data := make([]byte, len(encoded)*2)
	for i, r := range encoded {
		binary.LittleEndian.PutUint16(data[i*2:], r)
	}
	return data
}

func assertNoPersistentIndex(t *testing.T, ws *Workspace) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(ws.repo.HistoryDir, "index")); !os.IsNotExist(err) {
		t.Fatalf("history index = %v, want not exist", err)
	}
}

func assertNoTemporaryIndexes(t *testing.T, ws *Workspace) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(ws.repo.FrigoDir, "temporary-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary private-history files remain: %v", matches)
	}
}

func failingGitClient(t *testing.T, failGitDir, failCommand, failArg string) gitpkg.Client {
	t.Helper()
	return failingGitClientWithStderr(t, failGitDir, failCommand, failArg, "forced git failure")
}

func failingGitClientWithStderr(t *testing.T, failGitDir, failCommand, failArg, stderr string) gitpkg.Client {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required")
	}
	return gitpkg.Client{Path: testexec.Build(t)}.WithEnv(
		"FRIGO_REAL_GIT="+realGit,
		"FRIGO_FAIL_GIT_DIR="+failGitDir,
		"FRIGO_FAIL_COMMAND="+failCommand,
		"FRIGO_FAIL_ARG="+failArg,
		"FRIGO_FAIL_STDERR="+stderr,
	)
}

func createWinnerAndRestoreBase(t *testing.T, ws *Workspace, root, path, winnerContents string) (string, string) {
	t.Helper()
	base, err := ws.privateOutput(context.Background(), ws.git, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	testrepo.Write(t, root, path, winnerContents)
	saveForTest(t, ws, "winner snapshot")
	winner, err := ws.privateOutput(context.Background(), ws.git, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.privateOutput(context.Background(), ws.git, "update-ref", "HEAD", base, winner); err != nil {
		t.Fatal(err)
	}
	return base, winner
}

func assertHistoryHead(t *testing.T, ws *Workspace, want string) {
	t.Helper()
	got, err := ws.privateOutput(context.Background(), ws.git, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("history HEAD = %s, want external update %s", got, want)
	}
}

func externalHeadChangeGitClient(t *testing.T, historyDir, winner, expected, commands string) gitpkg.Client {
	t.Helper()
	return concurrentHeadChangeGitClient(t, historyDir, winner, expected).WithEnv("FRIGO_UPDATE_BEFORE_COMMAND=" + commands)
}

func concurrentHeadChangeGitClient(t *testing.T, historyDir, winner, expected string) gitpkg.Client {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required")
	}
	return gitpkg.Client{Path: testexec.Build(t)}.WithEnv(
		"FRIGO_REAL_GIT="+realGit,
		"FRIGO_HISTORY_DIR="+historyDir,
		"FRIGO_WINNER="+winner,
		"FRIGO_EXPECTED="+expected,
	)
}
