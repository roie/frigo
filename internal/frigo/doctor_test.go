package frigo

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	gitpkg "github.com/roie/frigo/internal/git"
	"github.com/roie/frigo/internal/lockfile"
	"github.com/roie/frigo/internal/metadata"
	"github.com/roie/frigo/internal/registry"
	"github.com/roie/frigo/internal/repository"
	"github.com/roie/frigo/internal/testrepo"
)

func TestDoctorHealthyMainAndLinkedState(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(map[bool]string{false: "main", true: "linked"}[linked], func(t *testing.T) {
			ws := newDoctorWorkspace(t, linked)
			result, err := ws.Doctor(context.Background(), DoctorOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Issues) != 0 {
				t.Fatalf("Doctor() issues = %#v, want none", result.Issues)
			}
		})
	}
}

func TestDoctorDiagnosesOrphanStableStore(t *testing.T) {
	ws := newDoctorWorkspace(t, true)
	id := strings.Repeat("e", 32)
	manifestPath := filepath.Join(ws.repo.LinkedStoresDir, id, manifestName)
	if err := metadata.Save(manifestPath, metadata.Manifest{
		Version: metadata.CurrentVersion, ID: id, WorktreePath: filepath.Join(ws.repo.Root, "orphan"),
	}); err != nil {
		t.Fatal(err)
	}

	result := diagnoseDoctor(t, ws)
	assertDoctorIssue(t, result, "orphan-store", filepath.Dir(manifestPath), false)
}

func TestDoctorDoesNotFollowSymlinkedStableStoreParent(t *testing.T) {
	ws := newDoctorWorkspace(t, true)
	id, err := metadata.LoadPointer(ws.repo.WorktreeIDPath)
	if err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(filepath.Dir(ws.repo.CommonFrigoDir), "doctor-external-worktrees")
	if err := os.Rename(ws.repo.LinkedStoresDir, external); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, ws.repo.LinkedStoresDir); err != nil {
		t.Fatal(err)
	}
	externalAttributes := filepath.Join(external, id, "history.git", "info", "attributes")
	if err := os.Remove(externalAttributes); err != nil {
		t.Fatal(err)
	}

	result, err := ws.Doctor(context.Background(), DoctorOptions{Repair: true})
	if err != nil {
		t.Fatal(err)
	}
	assertDoctorIssue(t, result, "metadata-malformed", ws.repo.LinkedStoresDir, false)
	if _, err := os.Lstat(externalAttributes); !os.IsNotExist(err) {
		t.Fatalf("doctor repaired through symlinked stable-store parent, err=%v", err)
	}
}

func TestDoctorOrphanScanDoesNotFollowSymlinkedAdminEntry(t *testing.T) {
	ws := newDoctorWorkspace(t, true)
	id := strings.Repeat("a", 32)
	store := filepath.Join(ws.repo.LinkedStoresDir, id)
	if err := metadata.Save(filepath.Join(store, manifestName), metadata.Manifest{
		Version: metadata.CurrentVersion, ID: id, WorktreePath: filepath.Join(ws.repo.Root, "orphan"),
	}); err != nil {
		t.Fatal(err)
	}
	externalAdmin := t.TempDir()
	if err := metadata.SavePointer(filepath.Join(externalAdmin, "frigo-id"), id); err != nil {
		t.Fatal(err)
	}
	fakeAdmin := filepath.Join(ws.repo.CommonDir, "worktrees", "symlinked-doctor-entry")
	if err := os.Symlink(externalAdmin, fakeAdmin); err != nil {
		t.Fatal(err)
	}

	result := diagnoseDoctor(t, ws)
	assertDoctorIssue(t, result, "orphan-store", store, false)
}

func TestDoctorDiagnosesStoreWithStaleAdminPointerAsOrphan(t *testing.T) {
	ws := newDoctorWorkspace(t, true)
	id, err := metadata.LoadPointer(ws.repo.WorktreeIDPath)
	if err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(ws.repo.LinkedStoresDir, id)
	if err := os.RemoveAll(ws.repo.Root); err != nil {
		t.Fatal(err)
	}

	result := diagnoseDoctor(t, ws)
	assertDoctorIssue(t, result, "orphan-store", store, false)
}

func TestDoctorDiagnosesMissingPointer(t *testing.T) {
	ws := newDoctorWorkspace(t, true)
	if err := os.Remove(ws.repo.WorktreeIDPath); err != nil {
		t.Fatal(err)
	}

	result := diagnoseDoctor(t, ws)
	assertDoctorIssue(t, result, "association-pointer-missing", ws.repo.WorktreeIDPath, true)
}

func TestDoctorDiagnosesAssociationIDMismatch(t *testing.T) {
	ws := newDoctorWorkspace(t, true)
	id, err := metadata.LoadPointer(ws.repo.WorktreeIDPath)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(ws.repo.LinkedStoresDir, id, manifestName)
	manifest, err := metadata.Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ID = strings.Repeat("d", 32)
	if err := metadata.Save(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}

	result := diagnoseDoctor(t, ws)
	assertDoctorIssue(t, result, "association-id-mismatch", manifestPath, false)
}

func TestDoctorDiagnosesMissingAndForeignLifecycleLocks(t *testing.T) {
	t.Run("missing-owned-lock", func(t *testing.T) {
		ws := newDoctorWorkspace(t, true)
		lockPath := filepath.Join(ws.repo.GitDir, "locked")
		if err := os.Remove(lockPath); err != nil {
			t.Fatal(err)
		}

		result := diagnoseDoctor(t, ws)
		assertDoctorIssue(t, result, "lifecycle-lock-missing", lockPath, true)
	})

	t.Run("foreign-lock", func(t *testing.T) {
		ws := newDoctorWorkspace(t, true)
		lockPath := filepath.Join(ws.repo.GitDir, "locked")
		if err := os.WriteFile(lockPath, []byte("foreign owner\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		result := diagnoseDoctor(t, ws)
		assertDoctorIssue(t, result, "lifecycle-lock-foreign", lockPath, false)
	})
}

func TestDoctorRepairClearsOnlyExactOwnedStaleLifecycleLock(t *testing.T) {
	ws := newDoctorWorkspace(t, true)
	id, err := metadata.LoadPointer(ws.repo.WorktreeIDPath)
	if err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(ws.repo.LinkedStoresDir, id)
	if err := registry.Save(filepath.Join(store, "registry.json"), registry.New()); err != nil {
		t.Fatal(err)
	}

	before := diagnoseDoctor(t, ws)
	assertDoctorIssue(t, before, "lifecycle-lock-stale", filepath.Join(ws.repo.GitDir, "locked"), true)
	result, err := ws.Doctor(context.Background(), DoctorOptions{Repair: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Issues) != 0 {
		t.Fatalf("post-repair issues = %#v, want none", result.Issues)
	}
	if _, err := os.Lstat(filepath.Join(ws.repo.GitDir, "locked")); !os.IsNotExist(err) {
		t.Fatalf("stale owned lifecycle lock remains, err=%v", err)
	}
	manifest, err := metadata.Load(filepath.Join(store, manifestName))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.LockOwned {
		t.Fatal("stale lifecycle ownership remains recorded")
	}
}

func TestDoctorDiagnosesStaleExclusions(t *testing.T) {
	ws := newDoctorWorkspace(t, false)
	if err := os.WriteFile(ws.repo.ExcludePath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	result := diagnoseDoctor(t, ws)
	assertDoctorIssue(t, result, "exclusions-stale", ws.repo.ExcludePath, true)
}

func TestDoctorDiagnosesMalformedAndNonUTF8Metadata(t *testing.T) {
	t.Run("malformed-manifest", func(t *testing.T) {
		ws := newDoctorWorkspace(t, true)
		id, err := metadata.LoadPointer(ws.repo.WorktreeIDPath)
		if err != nil {
			t.Fatal(err)
		}
		manifestPath := filepath.Join(ws.repo.LinkedStoresDir, id, manifestName)
		if err := os.WriteFile(manifestPath, []byte("{broken\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		result := diagnoseDoctor(t, ws)
		assertDoctorIssue(t, result, "metadata-malformed", manifestPath, false)
	})

	t.Run("raw-invalid-utf8-pointer", func(t *testing.T) {
		ws := newDoctorWorkspace(t, true)
		if err := os.WriteFile(ws.repo.WorktreeIDPath, []byte{0xff, '\n'}, 0o600); err != nil {
			t.Fatal(err)
		}

		result := diagnoseDoctor(t, ws)
		assertDoctorIssue(t, result, "metadata-invalid-utf8", ws.repo.WorktreeIDPath, false)
	})
}

func TestDoctorWarnsAboutUnicodeReplacementCharacter(t *testing.T) {
	ws := newDoctorWorkspace(t, true)
	id, err := metadata.LoadPointer(ws.repo.WorktreeIDPath)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(ws.repo.LinkedStoresDir, id, manifestName)
	manifest, err := metadata.Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.WorktreePath += "\ufffd"
	if err := metadata.Save(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}

	result := diagnoseDoctor(t, ws)
	assertDoctorIssue(t, result, "metadata-replacement-character", manifestPath, false)
}

func TestDoctorDiagnosesInvalidHistory(t *testing.T) {
	ws := newDoctorWorkspace(t, false)
	if err := os.Remove(filepath.Join(ws.repo.HistoryDir, "HEAD")); err != nil {
		t.Fatal(err)
	}

	result := diagnoseDoctor(t, ws)
	assertDoctorIssue(t, result, "history-invalid", ws.repo.HistoryDir, false)
}

func TestDoctorDiagnosesMissingExactAttributes(t *testing.T) {
	ws := newDoctorWorkspace(t, false)
	if err := os.Remove(ws.repo.PrivateAttributesPath); err != nil {
		t.Fatal(err)
	}

	result := diagnoseDoctor(t, ws)
	assertDoctorIssue(t, result, "attributes-private", ws.repo.PrivateAttributesPath, true)
}

func TestDoctorDiagnosesIncompleteStableInitialization(t *testing.T) {
	ws, _, _ := newLinkedWorkspace(t)
	id := strings.Repeat("c", 32)
	store := filepath.Join(ws.repo.LinkedStoresDir, id)
	if err := metadata.Save(filepath.Join(store, manifestName), metadata.Manifest{
		Version: metadata.CurrentVersion, ID: id, WorktreePath: ws.repo.Root,
	}); err != nil {
		t.Fatal(err)
	}

	result := diagnoseDoctor(t, ws)
	assertDoctorIssue(t, result, "stable-initialization-incomplete", store, false)
}

func TestDoctorRepairDoesNotAssociateIncompleteStableInitialization(t *testing.T) {
	ws, _, _ := newLinkedWorkspace(t)
	id := strings.Repeat("b", 32)
	store := filepath.Join(ws.repo.LinkedStoresDir, id)
	if err := metadata.Save(filepath.Join(store, manifestName), metadata.Manifest{
		Version: metadata.CurrentVersion, ID: id, WorktreePath: ws.repo.Root,
	}); err != nil {
		t.Fatal(err)
	}

	result, err := ws.Doctor(context.Background(), DoctorOptions{Repair: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range result.Applied {
		if strings.HasPrefix(action.Code, "association-pointer") {
			t.Fatalf("applied unsafe pointer repair to incomplete store: %#v", result.Applied)
		}
	}
	if _, err := os.Lstat(ws.repo.WorktreeIDPath); !os.IsNotExist(err) {
		t.Fatalf("incomplete stable store was associated, err=%v", err)
	}
}

func TestDoctorDiagnosesUnsupportedPreV02LinkedState(t *testing.T) {
	ws, _, _ := newLinkedWorkspace(t)
	legacy := filepath.Join(ws.repo.GitDir, "frigo")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}

	result := diagnoseDoctor(t, ws)
	assertDoctorIssue(t, result, "unsupported-pre-v0.2", legacy, false)
}

func TestDoctorRepairDoesNotMutateAlongsideUnsupportedPreV02State(t *testing.T) {
	ws := newDoctorWorkspace(t, true)
	legacy := filepath.Join(ws.repo.GitDir, "frigo")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	originalExclude := []byte("user content\n")
	if err := os.WriteFile(ws.repo.ExcludePath, originalExclude, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := ws.Doctor(context.Background(), DoctorOptions{Repair: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Applied) != 0 {
		t.Fatalf("applied repairs alongside unsupported state: %#v", result.Applied)
	}
	if got, err := os.ReadFile(ws.repo.ExcludePath); err != nil || !bytes.Equal(got, originalExclude) {
		t.Fatalf("exclude changed alongside unsupported state: %q, %v", got, err)
	}
}

func TestDoctorReportsOnlyOperationLockMetadataWhenCommonLockUnavailable(t *testing.T) {
	ws := newDoctorWorkspace(t, false)
	lock, err := lockfile.Acquire(context.Background(), ws.repo.OperationLockPath, "held-by-doctor-test", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	ws.lockWait = 0
	if err := os.Remove(ws.repo.PrivateAttributesPath); err != nil {
		t.Fatal(err)
	}

	result, err := ws.Doctor(context.Background(), DoctorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Issues) != 1 {
		t.Fatalf("Doctor() issues = %#v, want only operation lock issue", result.Issues)
	}
	issue := result.Issues[0]
	if issue.Code != "operation-lock-unavailable" || issue.Path != ws.repo.OperationLockPath {
		t.Fatalf("Doctor() issue = %#v, want operation lock metadata", issue)
	}
	if !strings.Contains(issue.Message, "held-by-doctor-test") {
		t.Fatalf("Doctor() lock message = %q, want owner operation", issue.Message)
	}
}

func TestDoctorRepairPlansBeforeMutationAppliesBoundedRepairsAndReruns(t *testing.T) {
	ws := newDoctorWorkspace(t, true)
	id, err := metadata.LoadPointer(ws.repo.WorktreeIDPath)
	if err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(ws.repo.LinkedStoresDir, id)
	manifestPath := filepath.Join(store, manifestName)
	manifest, err := metadata.Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.LockOwned = false
	if err := metadata.Save(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{
		ws.repo.WorktreeIDPath,
		filepath.Join(store, "attributes"),
		filepath.Join(store, "history.git", "info", "attributes"),
	} {
		if err := os.Remove(filename); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(ws.repo.ExcludePath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	callbackRan := false
	result, err := ws.DoctorWithPlan(context.Background(), DoctorOptions{Repair: true}, func(plan []DoctorAction) error {
		callbackRan = true
		if len(plan) != 5 {
			t.Fatalf("repair plan = %#v, want five bounded actions", plan)
		}
		for _, filename := range []string{
			ws.repo.WorktreeIDPath,
			filepath.Join(store, "attributes"),
			filepath.Join(store, "history.git", "info", "attributes"),
		} {
			if _, err := os.Lstat(filename); !os.IsNotExist(err) {
				t.Fatalf("%s exists before complete plan callback, err=%v", filename, err)
			}
		}
		contents, err := os.ReadFile(ws.repo.ExcludePath)
		if err != nil || len(contents) != 0 {
			t.Fatalf("exclude mutated before complete plan callback: %q, %v", contents, err)
		}
		loaded, err := metadata.Load(manifestPath)
		if err != nil || loaded.LockOwned {
			t.Fatalf("manifest ownership mutated before complete plan callback: %#v, %v", loaded, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !callbackRan {
		t.Fatal("complete plan callback did not run")
	}
	if !slices.Equal(result.Planned, result.Applied) {
		t.Fatalf("planned = %#v, applied = %#v", result.Planned, result.Applied)
	}
	if len(result.Issues) != 0 {
		t.Fatalf("post-repair issues = %#v, want none", result.Issues)
	}
	if got, err := metadata.LoadPointer(ws.repo.WorktreeIDPath); err != nil || got != id {
		t.Fatalf("repaired pointer = %q, %v; want %s", got, err, id)
	}
	if got, err := os.ReadFile(filepath.Join(store, "history.git", "info", "attributes")); err != nil || string(got) != privateAttributes {
		t.Fatalf("private attributes = %q, %v", got, err)
	}
	repairedManifest, err := metadata.Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !repairedManifest.LockOwned {
		t.Fatal("exact lifecycle lock ownership was not repaired")
	}
}

func TestDoctorRepairLeavesDestructiveAmbiguousAndForeignStateUntouched(t *testing.T) {
	ws := newDoctorWorkspace(t, true)
	id, err := metadata.LoadPointer(ws.repo.WorktreeIDPath)
	if err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(ws.repo.LinkedStoresDir, id)
	manifestPath := filepath.Join(store, manifestName)
	manifest, err := metadata.Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.WorktreePath += "-foreign"
	if err := metadata.Save(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(ws.repo.GitDir, "locked")
	foreignLock := []byte("foreign owner\n")
	if err := os.WriteFile(lockPath, foreignLock, 0o600); err != nil {
		t.Fatal(err)
	}
	historyHead := filepath.Join(store, "history.git", "HEAD")
	if err := os.Remove(historyHead); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(ws.repo.GitDir, "frigo")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	beforeManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	result, err := ws.Doctor(context.Background(), DoctorOptions{Repair: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Applied) != 0 {
		t.Fatalf("applied destructive repairs = %#v, want none", result.Applied)
	}
	if _, err := os.Lstat(historyHead); !os.IsNotExist(err) {
		t.Fatalf("history HEAD was recreated, err=%v", err)
	}
	if got, err := os.ReadFile(manifestPath); err != nil || !bytes.Equal(got, beforeManifest) {
		t.Fatalf("manifest path was rewritten: %q, %v", got, err)
	}
	if got, err := os.ReadFile(lockPath); err != nil || !bytes.Equal(got, foreignLock) {
		t.Fatalf("foreign lock was changed: %q, %v", got, err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("unsupported state was adopted or removed: %v", err)
	}
	for _, code := range []string{"association-worktree-mismatch", "unsupported-pre-v0.2"} {
		found := false
		for _, issue := range result.Issues {
			found = found || issue.Code == code
		}
		if !found {
			t.Fatalf("post-repair issues = %#v, want unresolved %s", result.Issues, code)
		}
	}
}

func TestDoctorIssuesAreSortedByCodeThenPath(t *testing.T) {
	ws := newDoctorWorkspace(t, false)
	if err := os.Remove(ws.repo.AttributesPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ws.repo.PrivateAttributesPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ws.repo.ExcludePath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	result := diagnoseDoctor(t, ws)
	if !slices.IsSortedFunc(result.Issues, func(a, b DoctorIssue) int {
		if a.Code != b.Code {
			return strings.Compare(a.Code, b.Code)
		}
		return strings.Compare(a.Path, b.Path)
	}) {
		t.Fatalf("Doctor() issues are not sorted: %#v", result.Issues)
	}
}

func newDoctorWorkspace(t *testing.T, linked bool) *Workspace {
	t.Helper()
	var ws *Workspace
	var root string
	if linked {
		ws, _, root = newLinkedWorkspace(t)
	} else {
		ws, root = newBareWorkspace(t)
	}
	testrepo.Write(t, root, "private.txt", "private\n")
	if _, err := ws.Add(context.Background(), []string{"private.txt"}); err != nil {
		t.Fatal(err)
	}
	repo, err := repository.Discover(context.Background(), gitpkg.Client{Path: "git"}, root)
	if err != nil {
		t.Fatal(err)
	}
	return NewWorkspace(repo, gitpkg.Client{Path: "git"}, root)
}

func diagnoseDoctor(t *testing.T, ws *Workspace) DoctorResult {
	t.Helper()
	result, err := ws.Doctor(context.Background(), DoctorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertDoctorIssue(t *testing.T, result DoctorResult, code, path string, repairable bool) {
	t.Helper()
	for _, issue := range result.Issues {
		if issue.Code == code && issue.Path == path {
			if issue.Repairable != repairable {
				t.Fatalf("issue %s repairable = %v, want %v", code, issue.Repairable, repairable)
			}
			return
		}
	}
	t.Fatalf("Doctor() issues = %#v, want %s at %s", result.Issues, code, path)
}
