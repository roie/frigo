package frigo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/roie/frigo/internal/metadata"
	"github.com/roie/frigo/internal/registry"
	"github.com/roie/frigo/internal/testrepo"
)

func TestWorktreeLockAcquirePersistsExactOwnership(t *testing.T) {
	ws, _, _ := initializedLinkedWorkspace(t)
	id := linkedWorkspaceID(t, ws)

	acquired, err := ws.ensureWorktreeProtection(context.Background(), id)
	if err != nil {
		t.Fatalf("ensureWorktreeProtection() error = %v", err)
	}
	if !acquired {
		t.Fatal("ensureWorktreeProtection() acquired = false, want true")
	}
	lock, err := ws.inspectWorktreeLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !lock.exists || lock.reason != worktreeLockReason(id) {
		t.Fatalf("lock = %#v, want exact owned reason", lock)
	}
	manifest := linkedManifest(t, ws)
	if !manifest.LockOwned {
		t.Fatalf("manifest LockOwned = false, want true")
	}

	again, err := ws.ensureWorktreeProtection(context.Background(), id)
	if err != nil {
		t.Fatalf("second ensureWorktreeProtection() error = %v", err)
	}
	if again {
		t.Fatal("second ensureWorktreeProtection() acquired = true, want idempotent false")
	}
}

func TestWorktreeLockRejectsForeignLockWithoutMutation(t *testing.T) {
	ws, _, linkedRoot := initializedLinkedWorkspace(t)
	id := linkedWorkspaceID(t, ws)
	testrepo.Run(t, linkedRoot, "worktree", "lock", "--reason", "foreign owner", linkedRoot)

	_, err := ws.ensureWorktreeProtection(context.Background(), id)
	if err == nil || !strings.Contains(err.Error(), "foreign") {
		t.Fatalf("ensureWorktreeProtection() error = %v, want foreign-lock rejection", err)
	}
	lock, inspectErr := ws.inspectWorktreeLock(context.Background())
	if inspectErr != nil {
		t.Fatal(inspectErr)
	}
	if lock.reason != "foreign owner" {
		t.Fatalf("foreign lock reason = %q, want unchanged", lock.reason)
	}
	if linkedManifest(t, ws).LockOwned {
		t.Fatal("foreign lock was recorded as Frigo-owned")
	}
}

func TestWorktreeLockRejectsNoncanonicalMatchingReason(t *testing.T) {
	ws, _, _ := initializedLinkedWorkspace(t)
	id := linkedWorkspaceID(t, ws)
	filename := filepath.Join(ws.repo.GitDir, "locked")
	reason := worktreeLockReason(id)
	if err := os.WriteFile(filename, []byte(reason), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := ws.ensureWorktreeProtection(context.Background(), id)
	if err == nil {
		t.Fatal("ensureWorktreeProtection() error = nil, want noncanonical lock rejection")
	}
	contents, readErr := os.ReadFile(filename)
	if readErr != nil || string(contents) != reason {
		t.Fatalf("noncanonical lock = %q, %v; want untouched", contents, readErr)
	}
	if linkedManifest(t, ws).LockOwned {
		t.Fatal("noncanonical matching text was persisted as owned")
	}
}

func TestWorktreeLockRejectsMismatchedFrigoReason(t *testing.T) {
	ws, _, linkedRoot := initializedLinkedWorkspace(t)
	id := linkedWorkspaceID(t, ws)
	otherID := strings.Repeat("8", 32)
	reason := worktreeLockReason(otherID)
	testrepo.Run(t, linkedRoot, "worktree", "lock", "--reason", reason, linkedRoot)

	_, err := ws.ensureWorktreeProtection(context.Background(), id)
	if err == nil || !strings.Contains(err.Error(), "mismatched") {
		t.Fatalf("ensureWorktreeProtection() error = %v, want mismatched-ID rejection", err)
	}
	lock, inspectErr := ws.inspectWorktreeLock(context.Background())
	if inspectErr != nil {
		t.Fatal(inspectErr)
	}
	if !lock.matches(reason) {
		t.Fatalf("mismatched Frigo lock changed: %#v", lock)
	}
	if linkedManifest(t, ws).LockOwned {
		t.Fatal("mismatched Frigo reason was persisted as owned")
	}
}

func TestWorktreeLockAdoptsOnlyExactInterruptedReason(t *testing.T) {
	ws, _, linkedRoot := initializedLinkedWorkspace(t)
	id := linkedWorkspaceID(t, ws)
	reason := worktreeLockReason(id)
	testrepo.Run(t, linkedRoot, "worktree", "lock", "--reason", reason, linkedRoot)

	acquired, err := ws.ensureWorktreeProtection(context.Background(), id)
	if err != nil {
		t.Fatalf("ensureWorktreeProtection() error = %v", err)
	}
	if acquired {
		t.Fatal("ensureWorktreeProtection() acquired = true for pre-existing exact lock")
	}
	if !linkedManifest(t, ws).LockOwned {
		t.Fatal("exact interrupted lock ownership was not persisted")
	}
}

func TestWorktreeLockRejectsSymlinkWithoutTouchingTarget(t *testing.T) {
	ws, _, _ := initializedLinkedWorkspace(t)
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("foreign owner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(ws.repo.GitDir, "locked")); err != nil {
		t.Fatal(err)
	}

	if _, err := ws.inspectWorktreeLock(context.Background()); err == nil {
		t.Fatal("inspectWorktreeLock() error = nil, want symlink rejection")
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "foreign owner\n" {
		t.Fatalf("lock symlink target = %q, %v; want untouched", got, err)
	}
}

func TestWorktreeLockRequiresAcquirePostcondition(t *testing.T) {
	ws, _, _ := initializedLinkedWorkspace(t)
	id := linkedWorkspaceID(t, ws)
	ws.lifecycleHook = func(name string) error {
		if name == "worktree-lock-command" {
			return os.Remove(filepath.Join(ws.repo.GitDir, "locked"))
		}
		return nil
	}

	_, err := ws.ensureWorktreeProtection(context.Background(), id)
	if err == nil || !strings.Contains(err.Error(), "postcondition") {
		t.Fatalf("ensureWorktreeProtection() error = %v, want missing postcondition", err)
	}
	if linkedManifest(t, ws).LockOwned {
		t.Fatal("missing lock postcondition was persisted as owned")
	}
}

func TestWorktreeLockCompensatesOwnershipSaveFailure(t *testing.T) {
	ws, _, _ := initializedLinkedWorkspace(t)
	id := linkedWorkspaceID(t, ws)
	injected := errors.New("persist ownership failed")
	ws.lifecycleHook = func(name string) error {
		if name == "worktree-lock-before-owned-save" {
			return injected
		}
		return nil
	}

	_, err := ws.ensureWorktreeProtection(context.Background(), id)
	if !errors.Is(err, injected) {
		t.Fatalf("ensureWorktreeProtection() error = %v, want injected failure", err)
	}
	lock, inspectErr := ws.inspectWorktreeLock(context.Background())
	if inspectErr != nil {
		t.Fatal(inspectErr)
	}
	if lock.exists {
		t.Fatalf("new lock remains after ownership-save compensation: %#v", lock)
	}
	if linkedManifest(t, ws).LockOwned {
		t.Fatal("manifest ownership changed despite failed save")
	}
	if _, statErr := os.Stat(ws.repo.WorktreeIDPath); statErr != nil {
		t.Fatalf("association evidence removed after compensation: %v", statErr)
	}
}

func TestWorktreeLockOwnershipSaveBoundaryFollowsDurableWrite(t *testing.T) {
	ws, _, _ := initializedLinkedWorkspace(t)
	id := linkedWorkspaceID(t, ws)
	injected := errors.New("interrupt after ownership save")
	observed := false
	ws.lifecycleHook = func(name string) error {
		if name != "worktree-lock-owned-save" {
			return nil
		}
		observed = linkedManifest(t, ws).LockOwned
		return injected
	}

	_, err := ws.ensureWorktreeProtection(context.Background(), id)
	if !errors.Is(err, injected) {
		t.Fatalf("ensureWorktreeProtection() error = %v, want post-save interruption", err)
	}
	if !observed {
		t.Fatal("ownership-save callback ran before durable LockOwned=true")
	}
}

func TestWorktreeUnlockOwnershipSaveBoundaryFollowsDurableWrite(t *testing.T) {
	ws, _, _ := initializedLinkedWorkspace(t)
	id := linkedWorkspaceID(t, ws)
	if _, err := ws.ensureWorktreeProtection(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("interrupt after ownership clear")
	observed := false
	ws.lifecycleHook = func(name string) error {
		if name != "worktree-unlock-owned-save" {
			return nil
		}
		observed = !linkedManifest(t, ws).LockOwned
		return injected
	}

	err := ws.releaseOwnedWorktreeLock(context.Background())
	if !errors.Is(err, injected) {
		t.Fatalf("releaseOwnedWorktreeLock() error = %v, want post-save interruption", err)
	}
	if !observed {
		t.Fatal("ownership-save callback ran before durable LockOwned=false")
	}
}

func TestWorktreeRelockOwnershipSaveBoundaryFollowsDurableWrite(t *testing.T) {
	ws, _, _ := initializedLinkedWorkspace(t)
	id := linkedWorkspaceID(t, ws)
	if _, err := ws.ensureWorktreeProtection(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	manifest := linkedManifest(t, ws)
	if err := ws.persistLockOwnership(manifest, false, "test-before-clear", "test-after-clear"); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("interrupt after relock ownership save")
	observed := false
	ws.lifecycleHook = func(name string) error {
		if name != "worktree-relock-owned-save" {
			return nil
		}
		observed = linkedManifest(t, ws).LockOwned
		return injected
	}

	err := ws.reacquireWorktreeProtection(context.Background(), id, worktreeLockReason(id))
	if !errors.Is(err, injected) {
		t.Fatalf("reacquireWorktreeProtection() error = %v, want post-save interruption", err)
	}
	if !observed {
		t.Fatal("relock ownership-save callback ran before durable LockOwned=true")
	}
}

func TestWorktreeLockReacquiresAfterUnlockBoundaryFailure(t *testing.T) {
	ws, _, _ := initializedLinkedWorkspace(t)
	id := linkedWorkspaceID(t, ws)
	if _, err := ws.ensureWorktreeProtection(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("interrupted after unlock")
	ws.lifecycleHook = func(name string) error {
		if name == "worktree-unlock-command" {
			return injected
		}
		return nil
	}

	err := ws.releaseOwnedWorktreeLock(context.Background())
	if !errors.Is(err, injected) {
		t.Fatalf("releaseOwnedWorktreeLock() error = %v, want injected failure", err)
	}
	assertExactWorktreeLock(t, ws, id)
}

func TestWorktreeLockReacquiresAfterUnlockOwnershipSaveFailure(t *testing.T) {
	ws, _, _ := initializedLinkedWorkspace(t)
	id := linkedWorkspaceID(t, ws)
	if _, err := ws.ensureWorktreeProtection(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("clear ownership failed")
	ws.lifecycleHook = func(name string) error {
		if name == "worktree-unlock-before-owned-save" {
			return injected
		}
		return nil
	}

	err := ws.releaseOwnedWorktreeLock(context.Background())
	if !errors.Is(err, injected) {
		t.Fatalf("releaseOwnedWorktreeLock() error = %v, want injected failure", err)
	}
	lock, inspectErr := ws.inspectWorktreeLock(context.Background())
	if inspectErr != nil {
		t.Fatal(inspectErr)
	}
	if !lock.exists || lock.reason != worktreeLockReason(id) {
		t.Fatalf("lock after compensation = %#v, want exact protection", lock)
	}
	if !linkedManifest(t, ws).LockOwned {
		t.Fatal("manifest lost ownership evidence after failed clear")
	}
}

func TestWorktreeLockPrecedesFirstNonemptyRegistrySave(t *testing.T) {
	ws, _, linkedRoot := newLinkedWorkspace(t)
	testrepo.Write(t, linkedRoot, "PLAN.md", "private\n")
	oldSave := saveRegistry
	saveRegistry = func(filename string, owned registry.Registry) error {
		if len(owned.Paths) > 0 {
			id, err := metadata.LoadPointer(ws.repo.WorktreeIDPath)
			if err != nil {
				return err
			}
			lock, err := ws.inspectWorktreeLock(context.Background())
			if err != nil {
				return err
			}
			if !lock.matches(worktreeLockReason(id)) || !linkedManifest(t, ws).LockOwned {
				return errors.New("non-empty registry save preceded exact lifecycle protection")
			}
		}
		return registry.Save(filename, owned)
	}
	t.Cleanup(func() { saveRegistry = oldSave })

	if _, err := ws.Add(context.Background(), []string{"PLAN.md"}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
}

func TestWorktreeLockLifecycleFollowsFirstAndLastRegistryPath(t *testing.T) {
	ws, _, linkedRoot := newLinkedWorkspace(t)
	testrepo.Write(t, linkedRoot, "alpha.local", "alpha\n")
	testrepo.Write(t, linkedRoot, "beta.local", "beta\n")

	if _, err := ws.Add(context.Background(), []string{"alpha.local", "beta.local"}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	id := linkedWorkspaceID(t, ws)
	assertExactWorktreeLock(t, ws, id)

	if _, err := ws.Release(context.Background(), []string{"alpha.local"}, true); err != nil {
		t.Fatalf("Release(alpha) error = %v", err)
	}
	assertExactWorktreeLock(t, ws, id)

	if _, err := ws.Release(context.Background(), []string{"beta.local"}, true); err != nil {
		t.Fatalf("Release(beta) error = %v", err)
	}
	lock, err := ws.inspectWorktreeLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if lock.exists {
		t.Fatalf("lock remains after last release: %#v", lock)
	}
	if linkedManifest(t, ws).LockOwned {
		t.Fatal("manifest ownership remains after last release")
	}
}

func TestWorktreeLockReleaseDoesNotRestoreActiveRegistryWithoutReacquiredProtection(t *testing.T) {
	ws, _, linkedRoot := newLinkedWorkspace(t)
	testrepo.Write(t, linkedRoot, "PLAN.md", "private\n")
	if _, err := ws.Add(context.Background(), []string{"PLAN.md"}); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("clear ownership failed")
	ws.lifecycleHook = func(name string) error {
		switch name {
		case "worktree-unlock-before-owned-save":
			return injected
		case "worktree-relock-command":
			return os.Remove(filepath.Join(ws.repo.GitDir, "locked"))
		default:
			return nil
		}
	}

	_, err := ws.Release(context.Background(), []string{"PLAN.md"}, true)
	if !errors.Is(err, injected) || !strings.Contains(err.Error(), "reacquire") {
		t.Fatalf("Release() error = %v, want ownership and reacquire failures", err)
	}
	owned, loadErr := registry.Load(ws.repo.RegistryPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(owned.Paths) != 0 {
		t.Fatalf("registry paths = %v, want inactive after unproven compensation", owned.Paths)
	}
	contents, readErr := os.ReadFile(ws.repo.ExcludePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(contents), "/PLAN.md") {
		t.Fatalf("exclude restored active path without protection: %q", contents)
	}
	if !linkedManifest(t, ws).LockOwned {
		t.Fatal("manifest ownership evidence was removed after failed compensation")
	}
	if _, statErr := os.Stat(ws.repo.WorktreeIDPath); statErr != nil {
		t.Fatalf("pointer evidence removed after failed compensation: %v", statErr)
	}
}

func TestWorktreeLockLastReleaseWithoutExactProtectionLeavesRegistryInactive(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string, string) bool
	}{
		{
			name: "missing lock",
			setup: func(t *testing.T, lockPath, _ string) bool {
				t.Helper()
				if err := os.Remove(lockPath); err != nil {
					t.Fatal(err)
				}
				return false
			},
		},
		{
			name: "foreign lock",
			setup: func(t *testing.T, lockPath, _ string) bool {
				t.Helper()
				if err := os.WriteFile(lockPath, []byte("foreign owner\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return true
			},
		},
		{
			name: "mismatched frigo lock",
			setup: func(t *testing.T, lockPath, _ string) bool {
				t.Helper()
				mismatched := worktreeLockReason(strings.Repeat("f", 32)) + "\n"
				if err := os.WriteFile(lockPath, []byte(mismatched), 0o600); err != nil {
					t.Fatal(err)
				}
				return true
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws, _, linkedRoot := newLinkedWorkspace(t)
			testrepo.Write(t, linkedRoot, "PLAN.md", "private\n")
			if _, err := ws.Add(context.Background(), []string{"PLAN.md"}); err != nil {
				t.Fatal(err)
			}
			lockPath := filepath.Join(ws.repo.GitDir, "locked")
			lockExists := tt.setup(t, lockPath, linkedWorkspaceID(t, ws))
			var lockBefore managedPathSnapshot
			if lockExists {
				lockBefore = snapshotManagedPath(t, lockPath)
			}
			pointerBefore := snapshotManagedPath(t, ws.repo.WorktreeIDPath)
			manifestPath := filepath.Join(ws.repo.FrigoDir, manifestName)
			manifestBefore := snapshotManagedPath(t, manifestPath)

			_, err := ws.Release(context.Background(), []string{"PLAN.md"}, true)
			if err == nil || !strings.Contains(err.Error(), "does not exactly match") {
				t.Fatalf("Release() error = %v, want missing exact lifecycle protection", err)
			}
			owned, loadErr := registry.Load(ws.repo.RegistryPath)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if len(owned.Paths) != 0 {
				t.Fatalf("registry paths = %v, want inactive without exact protection", owned.Paths)
			}
			contents, readErr := os.ReadFile(ws.repo.ExcludePath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if strings.Contains(string(contents), "/PLAN.md") {
				t.Fatalf("exclude restored active path without protection: %q", contents)
			}
			assertManagedPathUnchanged(t, ws.repo.WorktreeIDPath, pointerBefore)
			assertManagedPathUnchanged(t, manifestPath, manifestBefore)
			if !linkedManifest(t, ws).LockOwned {
				t.Fatal("manifest ownership evidence was removed without exact protection")
			}
			if lockExists {
				assertManagedPathUnchanged(t, lockPath, lockBefore)
			} else if _, statErr := os.Lstat(lockPath); !os.IsNotExist(statErr) {
				t.Fatalf("missing lock was recreated: %v", statErr)
			}
		})
	}
}

func TestWorktreeLockReleaseJoinsOperationAndRegistryRollbackFailures(t *testing.T) {
	ws, _, linkedRoot := newLinkedWorkspace(t)
	testrepo.Write(t, linkedRoot, "PLAN.md", "private\n")
	if _, err := ws.Add(context.Background(), []string{"PLAN.md"}); err != nil {
		t.Fatal(err)
	}
	operationErr := errors.New("clear ownership failed")
	rollbackErr := errors.New("registry rollback failed")
	ws.lifecycleHook = func(name string) error {
		if name == "worktree-unlock-before-owned-save" {
			return operationErr
		}
		return nil
	}
	oldSave := saveRegistry
	calls := 0
	saveRegistry = func(filename string, owned registry.Registry) error {
		calls++
		if calls == 2 {
			return rollbackErr
		}
		return registry.Save(filename, owned)
	}
	t.Cleanup(func() { saveRegistry = oldSave })

	_, err := ws.Release(context.Background(), []string{"PLAN.md"}, true)
	if !errors.Is(err, operationErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("Release() error = %v, want joined operation and rollback failures", err)
	}
	assertExactWorktreeLock(t, ws, linkedWorkspaceID(t, ws))
}

func TestWorktreeLockReleaseRollbackRestoresRegistryAndProtection(t *testing.T) {
	ws, _, linkedRoot := newLinkedWorkspace(t)
	testrepo.Write(t, linkedRoot, "PLAN.md", "private\n")
	if _, err := ws.Add(context.Background(), []string{"PLAN.md"}); err != nil {
		t.Fatal(err)
	}
	id := linkedWorkspaceID(t, ws)
	injected := errors.New("clear ownership failed")
	ws.lifecycleHook = func(name string) error {
		if name == "worktree-unlock-before-owned-save" {
			return injected
		}
		return nil
	}

	_, err := ws.Release(context.Background(), []string{"PLAN.md"}, true)
	if !errors.Is(err, injected) {
		t.Fatalf("Release() error = %v, want injected failure", err)
	}
	owned, loadErr := registry.Load(ws.repo.RegistryPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if !slices.Equal(owned.Paths, []string{"PLAN.md"}) {
		t.Fatalf("registry paths = %v, want rollback to PLAN.md", owned.Paths)
	}
	assertExactWorktreeLock(t, ws, id)
	contents, readErr := os.ReadFile(ws.repo.ExcludePath)
	if readErr != nil || !strings.Contains(string(contents), "/PLAN.md") {
		t.Fatalf("exclude after rollback = %q, %v; want PLAN.md", contents, readErr)
	}
}

func TestLinkedHistorySurvivesDoubleForceRemoval(t *testing.T) {
	ws, mainRoot, linkedRoot := newLinkedWorkspace(t)
	testrepo.Write(t, linkedRoot, "PLAN.md", "private\n")
	if _, err := ws.Add(context.Background(), []string{"PLAN.md"}); err != nil {
		t.Fatal(err)
	}
	id := linkedWorkspaceID(t, ws)
	history := filepath.Join(ws.repo.LinkedStoresDir, id, "history.git")
	admin := ws.repo.GitDir

	for _, args := range [][]string{
		{"-C", mainRoot, "worktree", "remove", linkedRoot},
		{"-C", mainRoot, "worktree", "remove", "--force", linkedRoot},
	} {
		if _, err := ws.git.Output(context.Background(), "", args...); err == nil {
			t.Fatalf("git %v succeeded, want lifecycle lock rejection", args)
		}
		if _, err := os.Stat(linkedRoot); err != nil {
			t.Fatalf("linked checkout removed after rejected command: %v", err)
		}
	}
	if _, err := ws.git.Output(context.Background(), "", "-C", mainRoot, "worktree", "remove", "--force", "--force", linkedRoot); err != nil {
		t.Fatalf("double-force worktree remove error = %v", err)
	}
	if _, err := os.Stat(linkedRoot); !os.IsNotExist(err) {
		t.Fatalf("linked checkout remains after double force: %v", err)
	}
	if _, err := os.Stat(admin); !os.IsNotExist(err) {
		t.Fatalf("linked administration remains after double force: %v", err)
	}
	if got := testrepo.Output(t, mainRoot, "--git-dir="+history, "rev-parse", "--is-bare-repository"); got != "true" {
		t.Fatalf("stable common history bare state = %q, want readable true", got)
	}
	if _, err := metadata.Load(filepath.Join(ws.repo.LinkedStoresDir, id, manifestName)); err != nil {
		t.Fatalf("stable common manifest missing after double force: %v", err)
	}
}

func initializedLinkedWorkspace(t *testing.T) (*Workspace, string, string) {
	t.Helper()
	ws, mainRoot, linkedRoot := newLinkedWorkspace(t)
	ctx := context.Background()
	if err := ws.withLock(ctx, "test initialize", func() error { return ws.ensureLayout(ctx, true) }); err != nil {
		t.Fatal(err)
	}
	return ws, mainRoot, linkedRoot
}

func linkedWorkspaceID(t *testing.T, ws *Workspace) string {
	t.Helper()
	id, err := metadata.LoadPointer(ws.repo.WorktreeIDPath)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func linkedManifest(t *testing.T, ws *Workspace) metadata.Manifest {
	t.Helper()
	manifest, err := metadata.Load(filepath.Join(ws.repo.FrigoDir, manifestName))
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func assertExactWorktreeLock(t *testing.T, ws *Workspace, id string) {
	t.Helper()
	lock, err := ws.inspectWorktreeLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !lock.matches(worktreeLockReason(id)) {
		t.Fatalf("worktree lock = %#v, want exact reason %q", lock, worktreeLockReason(id))
	}
	if !linkedManifest(t, ws).LockOwned {
		t.Fatal("manifest LockOwned = false while exact lifecycle lock exists")
	}
}
