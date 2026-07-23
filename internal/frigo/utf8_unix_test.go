//go:build !windows

package frigo

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
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
