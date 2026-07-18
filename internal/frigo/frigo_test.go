package frigo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	gitpkg "frigo/internal/git"
	"frigo/internal/registry"
	"frigo/internal/repository"
	"frigo/internal/testrepo"
)

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

func newWorkspace(t *testing.T) (*Workspace, string) {
	t.Helper()
	root := testrepo.Init(t)
	testrepo.Write(t, root, "README.md", "main\n")
	testrepo.CommitAll(t, root, "initial", "README.md")

	repo, err := repository.Discover(context.Background(), gitpkg.Client{Path: "git"}, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := initWorkspaceMetadata(t, repo); err != nil {
		t.Fatal(err)
	}
	return NewWorkspace(repo, gitpkg.Client{Path: "git"}, root), root
}

func initWorkspaceMetadata(t *testing.T, repo repository.Repository) error {
	t.Helper()
	if err := os.MkdirAll(repo.HooksDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(repo.AttributesPath, nil, 0o600); err != nil {
		return err
	}
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
