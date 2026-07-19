package frigo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitpkg "github.com/roie/frigo/internal/git"
	"github.com/roie/frigo/internal/repository"
	"github.com/roie/frigo/internal/testrepo"
)

type workspaceOperation struct {
	name string
	run  func(*Workspace, string) error
}

var separationOperations = []workspaceOperation{
	{
		name: "status",
		run: func(ws *Workspace, _ string) error {
			_, err := ws.Status(context.Background(), nil)
			return err
		},
	},
	{
		name: "diff",
		run: func(ws *Workspace, _ string) error {
			_, err := ws.Diff(context.Background(), nil)
			return err
		},
	},
	{
		name: "commit",
		run: func(ws *Workspace, _ string) error {
			_, err := ws.Commit(context.Background(), CommitOptions{Message: "checkpoint", All: true})
			return err
		},
	},
	{
		name: "restore",
		run: func(ws *Workspace, path string) error {
			_, err := ws.Restore(context.Background(), []string{path})
			return err
		},
	},
}

func TestPrivateOperationsRejectMainIndexDrift(t *testing.T) {
	ws, root := committedWorkspace(t, "PLAN.md", "saved\n")
	testrepo.Run(t, root, "add", "--force", "--", "PLAN.md")

	for _, operation := range separationOperations {
		t.Run(operation.name, func(t *testing.T) {
			err := operation.run(ws, "PLAN.md")
			if err == nil || !strings.Contains(err.Error(), "tracked by the main repository") {
				t.Fatalf("%s error = %v, want main-index separation error", operation.name, err)
			}
		})
	}
}

func TestPrivateOperationsRejectEffectiveIgnoreDrift(t *testing.T) {
	ws, root := committedWorkspace(t, "PLAN.md", "saved\n")
	testrepo.Write(t, root, ".gitignore", "!PLAN.md\n")

	for _, operation := range separationOperations {
		t.Run(operation.name, func(t *testing.T) {
			err := operation.run(ws, "PLAN.md")
			if err == nil || !strings.Contains(err.Error(), "not ignored by the main repository") {
				t.Fatalf("%s error = %v, want effective-ignore separation error", operation.name, err)
			}
		})
	}
}

func TestPrivateOperationsRejectEffectiveIgnoreDriftForMissingRoots(t *testing.T) {
	tests := []struct {
		path         string
		ignoreNegate string
	}{
		{path: "PLAN.md", ignoreNegate: "!/PLAN.md\n"},
		{path: ":/foo", ignoreNegate: "!/:/foo\n"},
		{path: "\"quote.md", ignoreNegate: "!/\"quote.md\n"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			ws, root := committedWorkspace(t, tt.path, "saved\n")
			filename := filepath.Join(root, filepath.FromSlash(tt.path))
			if err := os.Remove(filename); err != nil {
				t.Fatal(err)
			}
			testrepo.Write(t, root, ".gitignore", tt.ignoreNegate)

			for _, operation := range separationOperations {
				t.Run(operation.name, func(t *testing.T) {
					err := operation.run(ws, tt.path)
					if err == nil || !strings.Contains(err.Error(), "not ignored by the main repository") {
						t.Fatalf("%s error = %v, want effective-ignore separation error", operation.name, err)
					}
					if _, statErr := os.Stat(filename); !os.IsNotExist(statErr) {
						t.Fatalf("%s recreated %s despite separation error; stat error = %v", operation.name, tt.path, statErr)
					}
				})
			}
		})
	}
}

func TestPrivateOperationsRejectEffectiveIgnoreDriftForDirectoryRoot(t *testing.T) {
	ws, root := workspaceWithOwnership(t, "foo")
	testrepo.Write(t, root, "foo/current.txt", "saved\n")
	saveForTest(t, ws, "save foo")
	testrepo.Write(t, root, ".gitignore", "!/foo/\n/foo/current.txt\n")

	for _, operation := range separationOperations {
		t.Run(operation.name, func(t *testing.T) {
			err := operation.run(ws, "foo")
			if err == nil || !strings.Contains(err.Error(), "not ignored by the main repository") {
				t.Fatalf("%s error = %v, want effective-ignore separation error", operation.name, err)
			}
		})
	}
}

func TestEstablishedMetadataRequiresRegistryHistoryPair(t *testing.T) {
	tests := []struct {
		name   string
		remove func(*Workspace) error
	}{
		{name: "missing registry", remove: func(ws *Workspace) error { return os.Remove(ws.repo.RegistryPath) }},
		{name: "missing history", remove: func(ws *Workspace) error { return os.RemoveAll(ws.repo.HistoryDir) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws, _ := newWorkspace(t)
			if err := tt.remove(ws); err != nil {
				t.Fatal(err)
			}

			_, err := ws.List(context.Background(), nil)
			if err == nil || !strings.Contains(err.Error(), "metadata is incomplete") {
				t.Fatalf("List() error = %v, want incomplete metadata error", err)
			}
		})
	}
}

func TestEstablishedMetadataRequiresValidBareHistory(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(*testing.T, *Workspace, string)
		want    string
	}{
		{
			name: "malformed history directory",
			corrupt: func(t *testing.T, ws *Workspace, _ string) {
				if err := os.RemoveAll(ws.repo.HistoryDir); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(ws.repo.HistoryDir, 0o700); err != nil {
					t.Fatal(err)
				}
			},
			want: "valid bare Git repository",
		},
		{
			name: "non-bare history",
			corrupt: func(t *testing.T, ws *Workspace, root string) {
				testrepo.Run(t, root, "--git-dir="+ws.repo.HistoryDir, "config", "core.bare", "false")
			},
			want: "not bare",
		},
		{
			name: "corrupt history config",
			corrupt: func(t *testing.T, ws *Workspace, _ string) {
				if err := os.WriteFile(filepath.Join(ws.repo.HistoryDir, "config"), []byte("[core\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "valid bare Git repository",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws, root := newWorkspace(t)
			tt.corrupt(t, ws, root)

			_, err := ws.List(context.Background(), nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("List() error = %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func TestEstablishedMetadataAllowsBareHistoryWithoutHEAD(t *testing.T) {
	ws, _ := newWorkspace(t)

	paths, err := ws.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(paths) != 0 {
		t.Fatalf("List() = %v, want empty", paths)
	}
}

func TestPrivateOperationsReportSeparationBeforeCorruptHistory(t *testing.T) {
	ws, root := committedWorkspace(t, "PLAN.md", "saved\n")
	testrepo.Run(t, root, "add", "--force", "--", "PLAN.md")
	if err := os.WriteFile(filepath.Join(ws.repo.HistoryDir, "config"), []byte("[core\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, operation := range separationOperations {
		t.Run(operation.name, func(t *testing.T) {
			err := operation.run(ws, "PLAN.md")
			if err == nil || !strings.Contains(err.Error(), "tracked by the main repository") {
				t.Fatalf("%s error = %v, want separation error before history validation", operation.name, err)
			}
		})
	}
}

func TestListReturnsExactSortedRegistryRoots(t *testing.T) {
	ws, root := workspaceWithOwnership(t, "gone.md", "docs/local")
	testrepo.Write(t, root, "docs/local/a.md", "saved\n")
	testrepo.Write(t, root, "gone.md", "saved\n")
	saveForTest(t, ws, "save roots")
	if err := os.Remove(filepath.Join(root, "gone.md")); err != nil {
		t.Fatal(err)
	}
	testrepo.Write(t, root, "docs/local/b.md", "new\n")

	paths, err := ws.List(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"docs/local", "gone.md"}
	if len(paths) != len(want) {
		t.Fatalf("List() = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("List() = %v, want %v", paths, want)
		}
	}
}

func TestAddRejectsSymlinkAliasToGitMetadata(t *testing.T) {
	ws, root := newWorkspace(t)
	alias := filepath.Join(root, "git-metadata-alias")
	if err := os.Symlink(ws.repo.GitDir, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := ws.Add(context.Background(), []string{alias})
	if err == nil || !strings.Contains(err.Error(), "Git metadata") {
		t.Fatalf("Add() error = %v, want Git metadata rejection", err)
	}
}

func TestAddRejectsLinkedWorktreeSymlinkAliasToGitFile(t *testing.T) {
	root := testrepo.Init(t)
	linkedRoot := filepath.Join(root, "linked")
	testrepo.Run(t, root, "worktree", "add", "-q", "-b", "linked-branch", linkedRoot)
	repo, err := repository.Discover(context.Background(), gitpkg.Client{Path: "git"}, linkedRoot)
	if err != nil {
		t.Fatal(err)
	}
	ws := NewWorkspace(repo, gitpkg.Client{Path: "git"}, linkedRoot)
	alias := filepath.Join(linkedRoot, "git-metadata-alias")
	if err := os.Symlink(".git", alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err = ws.Add(context.Background(), []string{alias})
	if err == nil || !strings.Contains(err.Error(), "Git metadata") {
		t.Fatalf("Add() error = %v, want Git metadata rejection", err)
	}
}

func TestTemporaryIndexJoinsFinalCleanupFailure(t *testing.T) {
	ws, _ := newWorkspace(t)
	callbackErr := errors.New("callback failed")
	cleanupErr := errors.New("final cleanup failed")
	oldRemove := removeTemporaryIndex
	calls := 0
	removeTemporaryIndex = func(name string) error {
		calls++
		if calls == 2 {
			if err := oldRemove(name); err != nil {
				return err
			}
			return cleanupErr
		}
		return oldRemove(name)
	}
	t.Cleanup(func() { removeTemporaryIndex = oldRemove })

	err := ws.withTemporaryIndex(context.Background(), nil, func(client gitpkg.Client) error {
		return callbackErr
	})
	if !errors.Is(err, callbackErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("withTemporaryIndex() error = %v, want joined callback and cleanup errors", err)
	}
	if calls != 2 {
		t.Fatalf("removeTemporaryIndex() calls = %d, want 2", calls)
	}
}

func TestCommitDoesNotUpdateHeadWhenTemporaryIndexCleanupFails(t *testing.T) {
	ws, root := workspaceWithOwnership(t, "PLAN.md")
	testrepo.Write(t, root, "PLAN.md", "plan\n")
	cleanupErr := errors.New("final cleanup failed")
	oldRemove := removeTemporaryIndex
	calls := 0
	removeTemporaryIndex = func(name string) error {
		calls++
		if calls == 2 {
			if err := oldRemove(name); err != nil {
				return err
			}
			return cleanupErr
		}
		return oldRemove(name)
	}
	t.Cleanup(func() { removeTemporaryIndex = oldRemove })

	_, err := ws.Commit(context.Background(), CommitOptions{Message: "save plan", All: true})
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("Commit() error = %v, want cleanup error", err)
	}
	hasHead, headErr := ws.hasHead(context.Background())
	if headErr != nil {
		t.Fatal(headErr)
	}
	if hasHead {
		t.Fatal("Commit() updated HEAD before temporary-index cleanup succeeded")
	}
}

func TestReleaseReportsOnlyActuallyDirtyRequestedRoots(t *testing.T) {
	ws, root := workspaceWithOwnership(t, "PLAN.md", "NOTES.md")
	testrepo.Write(t, root, "PLAN.md", "saved plan\n")
	testrepo.Write(t, root, "NOTES.md", "saved notes\n")
	saveForTest(t, ws, "save both")
	testrepo.Write(t, root, "PLAN.md", "changed plan\n")

	_, err := ws.Release(context.Background(), []string{"PLAN.md", "NOTES.md"}, false)
	if err == nil || !strings.Contains(err.Error(), "PLAN.md") {
		t.Fatalf("Release() error = %v, want PLAN.md dirty", err)
	}
	if strings.Contains(err.Error(), "NOTES.md") {
		t.Fatalf("Release() error = %v, clean NOTES.md must not be reported", err)
	}
}

func TestCommitDoesNotUpdateHeadWhenShortHashFails(t *testing.T) {
	ws, root := workspaceWithOwnership(t, "PLAN.md")
	testrepo.Write(t, root, "PLAN.md", "plan\n")
	failing := NewWorkspace(ws.repo, failingGitClient(t, ws.repo.HistoryDir, "rev-parse", "--short"), root)

	_, err := failing.Commit(context.Background(), CommitOptions{Message: "save plan", All: true})
	if err == nil || !strings.Contains(err.Error(), "forced git failure") {
		t.Fatalf("Commit() error = %v, want short-hash failure", err)
	}
	hasHead, headErr := ws.hasHead(context.Background())
	if headErr != nil {
		t.Fatal(headErr)
	}
	if hasHead {
		t.Fatal("Commit() updated HEAD before short-hash computation succeeded")
	}
}
