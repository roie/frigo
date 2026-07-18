package frigo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	gitpkg "frigo/internal/git"
	"frigo/internal/ignore"
	"frigo/internal/registry"
	"frigo/internal/repository"
	"frigo/internal/testrepo"
)

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

func TestListIncludesSavedAndNewOwnedFiles(t *testing.T) {
	ws, root := newWorkspace(t)
	ownForTest(t, ws, "docs/local")
	testrepo.Write(t, root, "docs/local/a.md", "saved\n")
	saveForTest(t, ws, "save docs")
	testrepo.Write(t, root, "docs/local/b.md", "new\n")

	got, err := ws.List(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"docs/local/a.md", "docs/local/b.md"}
	if !slices.Equal(got, want) {
		t.Fatalf("List() = %v, want %v", got, want)
	}

	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
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

	wantErr := errors.New("boom")
	err := ws.withTemporaryIndex(context.Background(), []string{"PLAN.md"}, func(client gitpkg.Client) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("withTemporaryIndex() error = %v, want %v", err, wantErr)
	}

	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}

func TestTemporaryIndexRemovedOnCloseFailure(t *testing.T) {
	ws, root := newWorkspace(t)
	ownForTest(t, ws, "PLAN.md")
	testrepo.Write(t, root, "PLAN.md", "draft\n")

	oldClose := closeTemporaryIndex
	closeTemporaryIndex = func(*os.File) error { return errors.New("close failed") }
	t.Cleanup(func() { closeTemporaryIndex = oldClose })

	err := ws.withTemporaryIndex(context.Background(), []string{"PLAN.md"}, func(client gitpkg.Client) error {
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "close temporary index") {
		t.Fatalf("withTemporaryIndex() error = %v, want close temporary index error", err)
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

	err := ws.withTemporaryIndex(context.Background(), []string{"PLAN.md"}, func(client gitpkg.Client) error {
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "remove temporary index") {
		t.Fatalf("withTemporaryIndex() error = %v, want remove temporary index error", err)
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
	return registry.Save(repo.RegistryPath, registry.New())
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
}

func saveForTest(t *testing.T, ws *Workspace, message string) {
	t.Helper()
	owned, err := registry.Load(ws.repo.RegistryPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.withTemporaryIndex(context.Background(), owned.Paths, func(client gitpkg.Client) error {
		args := append([]string{"add", "-A", "--"}, owned.Paths...)
		if _, err := ws.privateOutput(context.Background(), client, args...); err != nil {
			return err
		}
		tree, err := ws.privateOutput(context.Background(), client, "write-tree")
		if err != nil {
			return err
		}
		commitArgs := []string{"commit-tree", tree, "-m", message}
		hasHead, err := ws.hasHead(context.Background())
		if err != nil {
			return err
		}
		if hasHead {
			parent, err := ws.privateOutput(context.Background(), client, "rev-parse", "HEAD")
			if err != nil {
				return err
			}
			commitArgs = append([]string{"commit-tree", tree, "-p", parent, "-m", message}, commitArgs[4:]...)
		}
		commit, err := ws.privateOutput(context.Background(), client, commitArgs...)
		if err != nil {
			return err
		}
		_, err = ws.privateOutput(context.Background(), client, "update-ref", "HEAD", commit)
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

func assertNoPersistentIndex(t *testing.T, ws *Workspace) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(ws.repo.HistoryDir, "index")); !os.IsNotExist(err) {
		t.Fatalf("history index = %v, want not exist", err)
	}
}

func assertNoTemporaryIndexes(t *testing.T, ws *Workspace) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(ws.repo.FrigoDir, "temporary-index-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary indexes remain: %v", matches)
	}
}

func failingGitClient(t *testing.T, failGitDir, failCommand, failArg string) gitpkg.Client {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required")
	}
	wrapper := filepath.Join(t.TempDir(), "git-wrapper")
	script := "#!/bin/sh\n" +
		"set -eu\n" +
		"match_dir=0\n" +
		"if [ \"${FRIGO_FAIL_GIT_DIR:-}\" = \"\" ]; then\n" +
		"  match_dir=1\n" +
		"elif [ \"${1-}\" = \"--git-dir=${FRIGO_FAIL_GIT_DIR}\" ]; then\n" +
		"  match_dir=1\n" +
		"fi\n" +
		"if [ \"$match_dir\" = 1 ]; then\n" +
		"  seen_command=0\n" +
		"  seen_arg=0\n" +
		"  for arg in \"$@\"; do\n" +
		"    if [ \"$arg\" = \"${FRIGO_FAIL_COMMAND:-}\" ]; then\n" +
		"      seen_command=1\n" +
		"    fi\n" +
		"    if [ \"${FRIGO_FAIL_ARG:-}\" = \"\" ] || [ \"$arg\" = \"${FRIGO_FAIL_ARG}\" ]; then\n" +
		"      seen_arg=1\n" +
		"    fi\n" +
		"  done\n" +
		"  if [ \"$seen_command\" = 1 ] && [ \"$seen_arg\" = 1 ]; then\n" +
		"    echo 'forced git failure' >&2\n" +
		"    exit 42\n" +
		"  fi\n" +
		"fi\n" +
		"exec \"${FRIGO_REAL_GIT}\" \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return gitpkg.Client{Path: wrapper}.WithEnv(
		"FRIGO_REAL_GIT="+realGit,
		"FRIGO_FAIL_GIT_DIR="+failGitDir,
		"FRIGO_FAIL_COMMAND="+failCommand,
		"FRIGO_FAIL_ARG="+failArg,
	)
}
