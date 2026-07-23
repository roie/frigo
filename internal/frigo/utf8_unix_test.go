//go:build !windows

package frigo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddRejectsInvalidUTF8PathBeforeMutation(t *testing.T) {
	ws, root := newWorkspace(t)
	invalid := string([]byte{'b', 'a', 'd', '-', 0xff})
	if err := os.WriteFile(filepath.Join(root, invalid), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(ws.repo.RegistryPath)
	if err != nil {
		t.Fatal(err)
	}

	result, err := ws.Add(context.Background(), []string{invalid})
	if err == nil || !strings.Contains(err.Error(), "not a valid UTF-8 path") {
		t.Fatalf("Add() error = %v", err)
	}
	if len(result.Added) != 0 || len(result.ReleasedCovered) != 0 || len(result.AlreadyOwned) != 0 {
		t.Fatalf("Add() result = %#v, want zero value", result)
	}

	after, err := os.ReadFile(ws.repo.RegistryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("registry mutated:\nbefore: %q\nafter:  %q", before, after)
	}
	assertNoPersistentIndex(t, ws)
	assertNoTemporaryIndexes(t, ws)
}
