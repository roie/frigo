//go:build !windows && !darwin

package frigo

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/roie/frigo/internal/testrepo"
)

func TestAddRejectsInvalidUTF8PathBeforeMutation(t *testing.T) {
	ws, root := newWorkspace(t)
	invalid := string([]byte{'b', 'a', 'd', '-', 0xff})
	if err := os.WriteFile(filepath.Join(root, invalid), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	beforeRegistry, err := os.ReadFile(ws.repo.RegistryPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeExclude, err := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	beforeHistory := snapshotHistoryState(t, ws.repo.HistoryDir)

	result, err := ws.Add(context.Background(), []string{invalid})
	if err == nil || !strings.Contains(err.Error(), "not a valid UTF-8 path") {
		t.Fatalf("Add() error = %v", err)
	}
	if len(result.Added) != 0 || len(result.ReleasedCovered) != 0 || len(result.AlreadyOwned) != 0 {
		t.Fatalf("Add() result = %#v, want zero value", result)
	}

	afterRegistry, err := os.ReadFile(ws.repo.RegistryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterRegistry) != string(beforeRegistry) {
		t.Fatalf("registry mutated:\nbefore: %q\nafter:  %q", beforeRegistry, afterRegistry)
	}
	afterExclude, err := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if string(afterExclude) != string(beforeExclude) {
		t.Fatalf("exclude mutated:\nbefore: %q\nafter:  %q", beforeExclude, afterExclude)
	}
	if afterHistory := snapshotHistoryState(t, ws.repo.HistoryDir); afterHistory != beforeHistory {
		t.Fatalf("history mutated:\nbefore: %s\nafter:  %s", beforeHistory, afterHistory)
	}
	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}

func TestAddRejectsInvalidUTF8PathBeforeInitializingLinkedStore(t *testing.T) {
	ws, _, root := newLinkedWorkspace(t)
	invalid := string([]byte{'b', 'a', 'd', '-', 0xff})
	if err := os.WriteFile(filepath.Join(root, invalid), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	gitDir := ws.repo.GitDir
	commonFrigoDir := ws.repo.CommonFrigoDir
	excludePath := ws.repo.ExcludePath
	lockPath := ws.repo.OperationLockPath
	beforeGitDir := snapshotManagedTree(t, gitDir)
	beforeExclude := snapshotManagedPath(t, excludePath)
	for _, path := range []string{commonFrigoDir, lockPath} {
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			t.Fatalf("unexpected preexisting managed path %s: %v", path, statErr)
		}
	}

	result, err := ws.Add(context.Background(), []string{invalid})
	if err == nil || !strings.Contains(err.Error(), "not a valid UTF-8 path") {
		t.Fatalf("Add() error = %v", err)
	}
	if len(result.Added) != 0 || len(result.ReleasedCovered) != 0 || len(result.AlreadyOwned) != 0 {
		t.Fatalf("Add() result = %#v, want zero value", result)
	}

	assertManagedTreeUnchanged(t, gitDir, beforeGitDir)
	assertManagedPathUnchanged(t, excludePath, beforeExclude)
	for _, path := range []string{commonFrigoDir, lockPath} {
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			t.Fatalf("managed path created at %s: %v", path, statErr)
		}
	}
}

func snapshotHistoryState(t *testing.T, root string) string {
	t.Helper()
	var entries []string
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			entries = append(entries, "dir:"+rel)
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		entries = append(entries, "file:"+rel+"="+string(data))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(entries)
	return strings.Join(entries, "\n")
}

func TestAddRejectsInvalidUTF8DescendantsBeforeAdoption(t *testing.T) {
	for _, setup := range []string{"new", "existing", "linked"} {
		for _, directory := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/directory=%v", setup, directory), func(t *testing.T) {
				var ws *Workspace
				var root string
				switch setup {
				case "new":
					ws, root = newBareWorkspace(t)
				case "existing":
					ws, root = committedWorkspace(t, "saved", "history\n")
				case "linked":
					ws, _, root = newLinkedWorkspace(t)
				}
				testrepo.Write(t, root, "notes/a-valid", "valid\n")
				invalid := filepath.Join(root, "notes", "z-"+string([]byte{0xff}))
				if directory {
					if err := os.Mkdir(invalid, 0o755); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(invalid, []byte("invalid\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				before := snapshotManagedTree(t, ws.repo.CommonDir)
				result, err := ws.Add(context.Background(), []string{"notes"})
				if err == nil || !strings.Contains(err.Error(), "not a valid UTF-8 path") {
					t.Fatalf("Add() = %#v, %v", result, err)
				}
				if len(result.Added) != 0 || len(result.AlreadyOwned) != 0 || len(result.ReleasedCovered) != 0 {
					t.Fatalf("nonzero result: %#v", result)
				}
				assertManagedTreeUnchanged(t, ws.repo.CommonDir, before)
			})
		}
	}
}

func TestCommitRejectsInvalidUTF8DescendantsInTemporaryIndex(t *testing.T) {
	for _, all := range []bool{false, true} {
		for _, late := range []bool{false, true} {
			t.Run(fmt.Sprintf("all=%v/late=%v", all, late), func(t *testing.T) {
				ws, root := committedWorkspace(t, "notes/saved", "history\n")
				ownForTest(t, ws, "notes")
				ctx := context.Background()
				// Normalize private metadata before taking the immutable state snapshots.
				if _, err := ws.Status(ctx, nil); err != nil {
					t.Fatal(err)
				}
				base, err := ws.resolveHistoryBase(ctx)
				if err != nil {
					t.Fatal(err)
				}
				beforeRegistry := snapshotManagedPath(t, ws.repo.RegistryPath)
				beforeExclude := snapshotManagedPath(t, ws.repo.ExcludePath)
				beforeCommits := privateCommitObjects(t, ws)
				invalid := filepath.Join(root, "notes", "bad-"+string([]byte{0xff}))
				writeInvalid := func() {
					if err := os.WriteFile(invalid, []byte("bad\n"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				if late {
					original := createTemporaryIndex
					t.Cleanup(func() { createTemporaryIndex = original })
					createTemporaryIndex = func(dir, pattern string) (*os.File, error) { writeInvalid(); return original(dir, pattern) }
				} else {
					writeInvalid()
				}
				options := CommitOptions{Message: "reject", All: all}
				if !all {
					options.Paths = []string{"notes"}
				}
				result, err := ws.Commit(ctx, options)
				if err == nil || !strings.Contains(err.Error(), "not a valid UTF-8 path") {
					t.Fatalf("Commit() = %#v, %v", result, err)
				}
				if result.Committed || result.Commit != "" {
					t.Fatalf("nonzero result: %#v", result)
				}
				assertHistoryHead(t, ws, base.OID)
				if after := privateCommitObjects(t, ws); after != beforeCommits {
					t.Fatalf("commit objects changed: %q => %q", beforeCommits, after)
				}
				assertManagedPathUnchanged(t, ws.repo.RegistryPath, beforeRegistry)
				assertManagedPathUnchanged(t, ws.repo.ExcludePath, beforeExclude)
				assertNoPersistentIndex(t, ws)
				assertNoTemporaryIndexes(t, ws)
			})
		}
	}
}

func TestCommitValidatesInheritedIndexPathsWithoutRewritingHistory(t *testing.T) {
	ws, root := workspaceWithOwnership(t, "notes", "good")
	invalid := "notes/bad-" + string([]byte{0xff})
	testrepo.Write(t, root, invalid, "historical\n")
	testrepo.Write(t, root, "good", "old\n")
	// This fixture reproduces history written by older Frigo releases.
	saveForTest(t, ws, "existing history")
	base, err := ws.resolveHistoryBase(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, invalid)); err != nil {
		t.Fatal(err)
	}
	testrepo.Write(t, root, "good", "new\n")
	before := privateCommitObjects(t, ws)
	result, err := ws.Commit(context.Background(), CommitOptions{Paths: []string{"good"}, Message: "reject inherited path"})
	if err == nil || !strings.Contains(err.Error(), "not a valid UTF-8 path") {
		t.Fatalf("Commit() = %#v, %v", result, err)
	}
	assertHistoryHead(t, ws, base.OID)
	if after := privateCommitObjects(t, ws); after != before {
		t.Fatalf("new commit objects: %q", after)
	}
	// A full commit may remove the invalid historical path, without rewriting old commits.
	result, err = ws.Commit(context.Background(), CommitOptions{All: true, Message: "remove invalid path"})
	if err != nil || !result.Committed {
		t.Fatalf("removal Commit() = %#v, %v", result, err)
	}
	if _, err := ws.ShowBlob(context.Background(), base.OID, "good"); err != nil {
		t.Fatalf("historical valid blob: %v", err)
	}
}

func TestUTF8DescendantsPreserveUnusualNamesSymlinksAndScope(t *testing.T) {
	ws, root := newBareWorkspace(t)
	names := []string{"notes/é雪", "notes/tab\tname", "notes/quote\"name", "notes/back\\slash", "notes/-dash", "notes/:(glob)*"}
	for _, name := range names {
		testrepo.Write(t, root, name, "exact\r\n")
	}
	outside := t.TempDir()
	testrepo.Write(t, outside, "bad-"+string([]byte{0xff}), "must not traverse\n")
	if err := os.Symlink(outside, filepath.Join(root, "notes", "link")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := ws.Add(ctx, []string{"notes"}); err != nil {
		t.Fatal(err)
	}
	result, err := ws.Commit(ctx, CommitOptions{All: true, Message: "valid unusual names"})
	if err != nil || !result.Committed {
		t.Fatalf("Commit() = %#v, %v", result, err)
	}
	status, err := ws.ShowNameStatus(ctx, "HEAD", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if !bytes.Contains(status, []byte("A\x00"+name+"\x00")) {
			t.Fatalf("missing exact name %q: %q", name, status)
		}
		got, err := ws.ShowBlob(ctx, "HEAD", name)
		if err != nil || string(got) != "exact\r\n" {
			t.Fatalf("blob %q = %q, %v", name, got, err)
		}
	}
	testrepo.Write(t, root, "notes/bad-"+string([]byte{0xff}), "unselected\n")
	testrepo.Write(t, root, names[0], "changed\n")
	result, err = ws.Commit(ctx, CommitOptions{Paths: names[:1], Message: "valid scope"})
	if err != nil || !result.Committed {
		t.Fatalf("scoped Commit() = %#v, %v", result, err)
	}
	got, err := ws.ShowBlob(ctx, "HEAD", "notes/link")
	if err != nil || string(got) != outside {
		t.Fatalf("symlink blob = %q, %v", got, err)
	}
	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}

func privateCommitObjects(t *testing.T, ws *Workspace) string {
	t.Helper()
	out, err := ws.privateOutput(context.Background(), ws.git, "cat-file", "--batch-all-objects", "--batch-check=%(objectname) %(objecttype)")
	if err != nil {
		t.Fatal(err)
	}
	var commits []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasSuffix(line, " commit") {
			commits = append(commits, line)
		}
	}
	sort.Strings(commits)
	return strings.Join(commits, "\n")
}
