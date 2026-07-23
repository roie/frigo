package frigo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/roie/frigo/internal/metadata"
	"github.com/roie/frigo/internal/testsync"
)

type worktreeLock struct {
	exists    bool
	reason    string
	canonical bool
}

func (l worktreeLock) matches(reason string) bool {
	return l.exists && l.canonical && l.reason == reason
}

type worktreeLockReleaseError struct {
	cause                 error
	restoreActiveRegistry bool
}

func (e *worktreeLockReleaseError) Error() string { return e.cause.Error() }
func (e *worktreeLockReleaseError) Unwrap() error { return e.cause }

func worktreeLockReason(id string) string {
	return "frigo:" + id + ": managed local files; run frigo release --all before removal"
}

func (w *Workspace) inspectWorktreeLock(context.Context) (worktreeLock, error) {
	filename := filepath.Join(w.repo.GitDir, "locked")
	info, err := os.Lstat(filename)
	if os.IsNotExist(err) {
		return worktreeLock{}, nil
	}
	if err != nil {
		return worktreeLock{}, fmt.Errorf("inspect worktree lock: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return worktreeLock{}, fmt.Errorf("worktree lock %s is not a regular file", filename)
	}
	contents, err := os.ReadFile(filename)
	if err != nil {
		return worktreeLock{}, fmt.Errorf("read worktree lock: %w", err)
	}
	if !utf8.Valid(contents) {
		return worktreeLock{}, fmt.Errorf("worktree lock reason is not valid UTF-8")
	}
	reason := string(contents)
	canonical := strings.HasSuffix(reason, "\n")
	if canonical {
		reason = strings.TrimSuffix(reason, "\n")
		canonical = !strings.ContainsAny(reason, "\r\n")
	}
	return worktreeLock{exists: true, reason: reason, canonical: canonical}, nil
}

func (w *Workspace) ensureWorktreeProtection(ctx context.Context, id string) (bool, error) {
	if !w.repo.LinkedWorktree {
		return false, nil
	}
	manifest, err := w.proveLinkedAssociation(ctx, id)
	if err != nil {
		return false, err
	}
	expected := worktreeLockReason(id)
	lock, err := w.inspectWorktreeLock(ctx)
	if err != nil {
		return false, err
	}
	if lock.exists {
		if !lock.matches(expected) {
			return false, fmt.Errorf("linked worktree has a foreign or mismatched lock reason %q; refusing to overwrite it", lock.reason)
		}
		if manifest.LockOwned {
			return false, nil
		}
		if err := w.persistLockOwnership(manifest, true, "worktree-lock-before-owned-save"); err != nil {
			return false, fmt.Errorf("persist ownership of existing exact worktree lock: %w", err)
		}
		return false, nil
	}

	if _, err := w.git.Output(ctx, "", "-C", w.repo.Root, "worktree", "lock", "--reason", expected, w.repo.Root); err != nil {
		return false, fmt.Errorf("lock linked worktree: %w", err)
	}
	if err := w.lifecycleBoundary("worktree-lock-command"); err != nil {
		return false, err
	}
	lock, err = w.inspectWorktreeLock(ctx)
	if err != nil {
		return false, err
	}
	if !lock.matches(expected) {
		return false, fmt.Errorf("worktree lock postcondition missing or mismatched after lock command")
	}
	if manifest.LockOwned {
		return true, nil
	}
	if err := w.persistLockOwnership(manifest, true, "worktree-lock-before-owned-save"); err != nil {
		rollbackErr := w.undoNewWorktreeLock(ctx, id, expected)
		return false, errors.Join(
			fmt.Errorf("persist lifecycle lock ownership: %w", err),
			wrapOptional("undo newly acquired lifecycle lock", rollbackErr),
		)
	}
	return true, nil
}

func (w *Workspace) releaseOwnedWorktreeLock(ctx context.Context) error {
	if !w.repo.LinkedWorktree {
		return nil
	}
	id, exists, err := loadManagedPointer(w.repo.WorktreeIDPath)
	if err != nil {
		return fmt.Errorf("load linked frigo pointer before unlock: %w", err)
	}
	if !exists {
		return fmt.Errorf("linked frigo pointer is missing; refusing to unlock")
	}
	manifest, err := w.proveLinkedAssociation(ctx, id)
	if err != nil {
		return err
	}
	if !manifest.LockOwned {
		return fmt.Errorf("linked frigo manifest does not prove lifecycle lock ownership")
	}
	expected := worktreeLockReason(id)
	lock, err := w.inspectWorktreeLock(ctx)
	if err != nil {
		return err
	}
	if !lock.matches(expected) {
		return &worktreeLockReleaseError{
			cause:                 fmt.Errorf("linked worktree lock does not exactly match owned reason; refusing to unlock"),
			restoreActiveRegistry: true,
		}
	}

	if _, err := w.git.Output(ctx, "", "-C", w.repo.Root, "worktree", "unlock", w.repo.Root); err != nil {
		return w.compensateUnlockFailure(ctx, id, expected, fmt.Errorf("unlock linked worktree: %w", err))
	}
	if err := w.lifecycleBoundary("worktree-unlock-command"); err != nil {
		return w.compensateUnlockFailure(ctx, id, expected, err)
	}
	lock, err = w.inspectWorktreeLock(ctx)
	if err != nil {
		return w.compensateUnlockFailure(ctx, id, expected, err)
	}
	if lock.exists {
		return w.compensateUnlockFailure(ctx, id, expected,
			fmt.Errorf("worktree unlock postcondition failed; lock remains with reason %q", lock.reason))
	}
	if err := w.persistLockOwnership(manifest, false, "worktree-unlock-before-owned-save"); err != nil {
		return w.compensateUnlockFailure(ctx, id, expected,
			fmt.Errorf("persist cleared lifecycle lock ownership: %w", err))
	}
	return nil
}

func (w *Workspace) proveLinkedAssociation(ctx context.Context, id string) (metadata.Manifest, error) {
	if err := w.ensureLayout(ctx, false); err != nil {
		return metadata.Manifest{}, err
	}
	pointerID, exists, err := loadManagedPointer(w.repo.WorktreeIDPath)
	if err != nil {
		return metadata.Manifest{}, fmt.Errorf("load linked frigo pointer: %w", err)
	}
	if !exists || pointerID != id {
		return metadata.Manifest{}, fmt.Errorf("linked frigo pointer does not agree with expected ID %s", id)
	}
	expectedStore := filepath.Join(w.repo.LinkedStoresDir, id)
	if filepath.Clean(w.repo.FrigoDir) != expectedStore {
		return metadata.Manifest{}, fmt.Errorf("selected linked frigo store %s does not agree with pointer ID %s", w.repo.FrigoDir, id)
	}
	manifestPath := filepath.Join(expectedStore, manifestName)
	if err := requireManagedRegularFile(manifestPath); err != nil {
		return metadata.Manifest{}, fmt.Errorf("inspect linked frigo manifest: %w", err)
	}
	manifest, err := metadata.Load(manifestPath)
	if err != nil {
		return metadata.Manifest{}, fmt.Errorf("load linked frigo manifest: %w", err)
	}
	if manifest.ID != id || manifest.WorktreePath != w.repo.Root {
		return metadata.Manifest{}, fmt.Errorf("linked pointer, store, manifest, and current worktree do not exactly agree")
	}
	return manifest, nil
}

func (w *Workspace) persistLockOwnership(manifest metadata.Manifest, owned bool, boundary string) error {
	if err := w.lifecycleBoundary(boundary); err != nil {
		return err
	}
	manifest.LockOwned = owned
	filename := filepath.Join(w.repo.FrigoDir, manifestName)
	if err := requireManagedRegularFile(filename); err != nil {
		return err
	}
	if err := metadata.Save(filename, manifest); err != nil {
		return err
	}
	loaded, err := metadata.Load(filename)
	if err != nil {
		return err
	}
	if loaded.ID != manifest.ID || loaded.WorktreePath != manifest.WorktreePath || loaded.LockOwned != owned {
		return fmt.Errorf("manifest ownership postcondition failed")
	}
	return nil
}

func (w *Workspace) undoNewWorktreeLock(ctx context.Context, id, expected string) error {
	if _, err := w.proveLinkedAssociation(ctx, id); err != nil {
		return fmt.Errorf("prove association before compensation: %w", err)
	}
	lock, err := w.inspectWorktreeLock(ctx)
	if err != nil {
		return err
	}
	if !lock.matches(expected) {
		return fmt.Errorf("new lifecycle lock can no longer be proven exact; preserving association evidence")
	}
	if _, err := w.git.Output(ctx, "", "-C", w.repo.Root, "worktree", "unlock", w.repo.Root); err != nil {
		return err
	}
	lock, err = w.inspectWorktreeLock(ctx)
	if err != nil {
		return err
	}
	if lock.exists {
		return fmt.Errorf("compensating unlock postcondition failed")
	}
	return nil
}

func (w *Workspace) compensateUnlockFailure(ctx context.Context, id, expected string, cause error) error {
	compensationErr := w.reacquireWorktreeProtection(ctx, id, expected)
	protected, proofErr := w.hasExactWorktreeProtection(ctx, id, expected)
	if !protected && proofErr == nil {
		proofErr = fmt.Errorf("exact lifecycle protection was not restored")
	}
	joined := errors.Join(
		cause,
		wrapOptional("reacquire lifecycle protection", compensationErr),
		wrapOptional("verify reacquired lifecycle protection", proofErr),
	)
	return &worktreeLockReleaseError{cause: joined, restoreActiveRegistry: protected && proofErr == nil}
}

func (w *Workspace) hasExactWorktreeProtection(ctx context.Context, id, expected string) (bool, error) {
	manifest, err := w.proveLinkedAssociation(ctx, id)
	if err != nil {
		return false, err
	}
	if !manifest.LockOwned {
		return false, nil
	}
	lock, err := w.inspectWorktreeLock(ctx)
	if err != nil {
		return false, err
	}
	return lock.matches(expected), nil
}

func (w *Workspace) reacquireWorktreeProtection(ctx context.Context, id, expected string) error {
	manifest, err := w.proveLinkedAssociation(ctx, id)
	if err != nil {
		return err
	}
	lock, err := w.inspectWorktreeLock(ctx)
	if err != nil {
		return err
	}
	if lock.exists {
		if !lock.matches(expected) {
			return fmt.Errorf("foreign lock appeared during lifecycle compensation")
		}
	} else {
		if _, err := w.git.Output(ctx, "", "-C", w.repo.Root, "worktree", "lock", "--reason", expected, w.repo.Root); err != nil {
			return err
		}
		if err := w.lifecycleBoundary("worktree-relock-command"); err != nil {
			return err
		}
		lock, err = w.inspectWorktreeLock(ctx)
		if err != nil {
			return err
		}
		if !lock.matches(expected) {
			return fmt.Errorf("compensating lifecycle lock postcondition failed")
		}
	}
	if !manifest.LockOwned {
		manifest.LockOwned = true
		if err := metadata.Save(filepath.Join(w.repo.FrigoDir, manifestName), manifest); err != nil {
			return fmt.Errorf("restore lifecycle ownership evidence: %w", err)
		}
	}
	return nil
}

func worktreeLockAllowsActiveRegistryRestore(err error) bool {
	var releaseErr *worktreeLockReleaseError
	if !errors.As(err, &releaseErr) {
		return true
	}
	return releaseErr.restoreActiveRegistry
}

func (w *Workspace) lifecycleBoundary(name string) error {
	if w.lifecycleHook != nil {
		if err := w.lifecycleHook(name); err != nil {
			return err
		}
	}
	return testsync.Fail(name)
}

func wrapOptional(message string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err)
}
