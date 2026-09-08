package frigo

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/roie/frigo/internal/atomicfile"
	"github.com/roie/frigo/internal/registry"
	"github.com/roie/frigo/internal/testrepo"
)

func TestReleaseRollbackPublishedRegistryStillRestoresExclusions(t *testing.T) {
	ws, _, root := initializedLinkedWorkspace(t)
	testrepo.Write(t, root, "private.local", "private\n")
	if _, err := ws.Add(context.Background(), []string{"private.local"}); err != nil {
		t.Fatal(err)
	}
	operationErr := errors.New("unlock failed")
	ws.lifecycleHook = func(name string) error {
		if name == "worktree-unlock-command" {
			return operationErr
		}
		return nil
	}
	syncErr := errors.New("rollback directory sync failed")
	originalSave := saveRegistry
	calls := 0
	saveRegistry = func(filename string, owned registry.Registry) error {
		calls++
		if err := registry.Save(filename, owned); err != nil {
			return err
		}
		if calls == 2 {
			return &atomicfile.PublishedError{Path: filename, Err: syncErr}
		}
		return nil
	}
	t.Cleanup(func() { saveRegistry = originalSave })
	_, err := ws.ReleaseAll(context.Background(), true)
	if !errors.Is(err, operationErr) || !errors.Is(err, syncErr) {
		t.Fatalf("lost errors: %v", err)
	}
	owned, err := registry.Load(ws.repo.RegistryPath)
	if err != nil || !owned.OwnsExact("private.local") {
		t.Fatalf("registry=%v %v", owned, err)
	}
	contents, err := os.ReadFile(ws.repo.ExcludePath)
	if err != nil || !strings.Contains(string(contents), "/private.local\n") {
		t.Fatalf("exclusions=%q %v", contents, err)
	}
	lock, err := ws.inspectWorktreeLock(context.Background())
	if err != nil || !lock.exists || !linkedManifest(t, ws).LockOwned {
		t.Fatalf("protection=%+v %v", lock, err)
	}
}
