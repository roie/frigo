package frigo

import (
	"bytes"
	"context"
	"errors"
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

func TestDoctorRepairRejectsPointerAssociationWithConflictingAdminEvidence(t *testing.T) {
	for _, stale := range []bool{false, true} {
		name := "live"
		if stale {
			name = "stale"
		}
		t.Run(name, func(t *testing.T) {
			ws := newDoctorWorkspace(t, true)
			id, err := metadata.LoadPointer(ws.repo.WorktreeIDPath)
			if err != nil {
				t.Fatal(err)
			}
			siblingRoot := filepath.Join(filepath.Dir(ws.repo.Root), filepath.Base(ws.repo.Root)+"-conflict")
			testrepo.Run(t, ws.repo.Root, "worktree", "add", "-q", "-b", "doctor-conflict", siblingRoot)
			t.Cleanup(func() { _ = os.RemoveAll(siblingRoot) })
			siblingRepo, err := repository.Discover(context.Background(), gitpkg.Client{Path: "git"}, siblingRoot)
			if err != nil {
				t.Fatal(err)
			}
			if err := metadata.SavePointer(siblingRepo.WorktreeIDPath, id); err != nil {
				t.Fatal(err)
			}
			if stale {
				if err := os.RemoveAll(siblingRoot); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Remove(ws.repo.WorktreeIDPath); err != nil {
				t.Fatal(err)
			}

			result, err := ws.Doctor(context.Background(), DoctorOptions{Repair: true})
			if err != nil {
				t.Fatal(err)
			}
			assertDoctorIssue(t, result, "association-ambiguous", ws.repo.WorktreeIDPath, false)
			assertDoctorIssue(t, result, "orphan-store", filepath.Join(ws.repo.LinkedStoresDir, id), false)
			for _, actions := range [][]DoctorAction{result.Planned, result.Applied} {
				for _, action := range actions {
					if strings.HasPrefix(action.Code, "association-pointer-") {
						t.Fatalf("unsafe pointer repair outcome = %#v", action)
					}
				}
			}
			if got, err := metadata.LoadPointer(siblingRepo.WorktreeIDPath); err != nil || got != id {
				t.Fatalf("conflicting evidence = %q, %v; want retained %s", got, err, id)
			}
			if _, err := os.Lstat(ws.repo.WorktreeIDPath); !os.IsNotExist(err) {
				t.Fatalf("current pointer was repaired despite conflict: %v", err)
			}
		})
	}
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

func TestDoctorRepairRejectsSymlinkedExcludeParentWithoutMutatingTarget(t *testing.T) {
	ws := newDoctorWorkspace(t, false)
	externalInfo := filepath.Join(ws.repo.Root, "external-info")
	if err := os.Rename(filepath.Dir(ws.repo.ExcludePath), externalInfo); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalInfo, filepath.Dir(ws.repo.ExcludePath)); err != nil {
		t.Fatal(err)
	}
	externalExclude := filepath.Join(externalInfo, "exclude")
	foreign := []byte("foreign-before\n")
	if err := os.WriteFile(externalExclude, foreign, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := ws.Doctor(context.Background(), DoctorOptions{Repair: true})
	if err != nil {
		t.Fatal(err)
	}
	assertDoctorIssue(t, result, "exclusions-malformed", ws.repo.ExcludePath, false)
	if len(result.Planned) != 0 || len(result.Applied) != 0 {
		t.Fatalf("unsafe exclusion repair outcomes = %#v / %#v", result.Planned, result.Applied)
	}
	contents, err := os.ReadFile(externalExclude)
	if err != nil || !bytes.Equal(contents, foreign) {
		t.Fatalf("external exclude = %q, %v; want unchanged %q", contents, err, foreign)
	}
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

func TestDoctorDiagnosesReplacementCharacterInMalformedPointer(t *testing.T) {
	ws := newDoctorWorkspace(t, true)
	if err := os.WriteFile(ws.repo.WorktreeIDPath, []byte("\ufffd\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := diagnoseDoctor(t, ws)
	assertDoctorIssue(t, result, "metadata-replacement-character", ws.repo.WorktreeIDPath, false)
	assertDoctorIssue(t, result, "metadata-malformed", ws.repo.WorktreeIDPath, false)
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

func TestDoctorFromMainDiagnosesUnsupportedPreV02StateInActiveSiblingWithoutMutation(t *testing.T) {
	linked, mainRoot, _ := newLinkedWorkspace(t)
	legacy := filepath.Join(linked.repo.GitDir, "frigo")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(legacy, "marker")
	if err := os.WriteFile(marker, []byte("legacy\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	repo, err := repository.Discover(context.Background(), gitpkg.Client{Path: "git"}, mainRoot)
	if err != nil {
		t.Fatal(err)
	}
	ws := NewWorkspace(repo, gitpkg.Client{Path: "git"}, mainRoot)
	result, err := ws.Doctor(context.Background(), DoctorOptions{Repair: true})
	if err != nil {
		t.Fatal(err)
	}
	assertDoctorIssue(t, result, "unsupported-pre-v0.2", legacy, false)
	if len(result.Planned) != 0 || len(result.Applied) != 0 {
		t.Fatalf("doctor attempted to repair unsupported sibling state: %#v", result)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "legacy\n" {
		t.Fatalf("unsupported sibling state changed: %q, %v", got, err)
	}
}

func TestDoctorIgnoresUnsupportedStateInInactiveAdminDirectory(t *testing.T) {
	_, mainRoot, _ := newLinkedWorkspace(t)
	repo, err := repository.Discover(context.Background(), gitpkg.Client{Path: "git"}, mainRoot)
	if err != nil {
		t.Fatal(err)
	}
	staleAdmin := filepath.Join(repo.CommonDir, "worktrees", "stale")
	legacy := filepath.Join(staleAdmin, "frigo")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staleAdmin, "gitdir"), []byte(filepath.Join(t.TempDir(), ".git")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := diagnoseDoctor(t, NewWorkspace(repo, gitpkg.Client{Path: "git"}, mainRoot))
	for _, issue := range result.Issues {
		if issue.Code == "unsupported-pre-v0.2" && issue.Path == legacy {
			t.Fatalf("doctor reported inactive admin state: %#v", issue)
		}
	}
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

	for _, repair := range []bool{false, true} {
		result, err := ws.Doctor(context.Background(), DoctorOptions{Repair: repair})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Issues) != 1 {
			t.Fatalf("Doctor(repair=%t) issues = %#v, want only operation lock issue", repair, result.Issues)
		}
		issue := result.Issues[0]
		if issue.Code != "operation-lock-unavailable" || issue.Path != ws.repo.OperationLockPath {
			t.Fatalf("Doctor(repair=%t) issue = %#v, want operation lock metadata", repair, issue)
		}
		for _, want := range []string{
			"held-by-doctor-test",
			ws.repo.OperationLockPath,
			"verify that no Frigo process is still running for this repository",
			"manually delete " + ws.repo.OperationLockPath,
			"doctor will never remove this lock",
		} {
			if !strings.Contains(issue.Message, want) {
				t.Fatalf("Doctor(repair=%t) lock message = %q, want %q", repair, issue.Message, want)
			}
		}
		if _, err := os.Lstat(ws.repo.OperationLockPath); err != nil {
			t.Fatalf("Doctor(repair=%t) removed operation lock: %v", repair, err)
		}
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

func TestDoctorApplyFailureRetainsCompletedActionsAndFreshDiagnosis(t *testing.T) {
	ws := newDoctorWorkspace(t, false)
	if err := os.Remove(ws.repo.PrivateAttributesPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ws.repo.ExcludePath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ws.doctorActionHook = func(code string) error {
		if code == "exclusions-stale" {
			return errors.New("induced exclusion apply failure")
		}
		return nil
	}

	result, err := ws.Doctor(context.Background(), DoctorOptions{Repair: true})
	if err == nil || !strings.Contains(err.Error(), "apply doctor action exclusions-stale") {
		t.Fatalf("DoctorWithPlan() error = %v, want exclusion apply failure", err)
	}
	if got, want := result.Applied, []DoctorAction{{
		Code: "attributes-private", Path: ws.repo.PrivateAttributesPath, Description: "restore exact private attributes",
	}}; !slices.Equal(got, want) {
		t.Fatalf("applied = %#v, want %#v", got, want)
	}
	assertDoctorIssue(t, result, "exclusions-stale", ws.repo.ExcludePath, true)
	for _, issue := range result.Issues {
		if issue.Code == "attributes-private" {
			t.Fatalf("stale pre-apply diagnosis retained repaired issue: %#v", result.Issues)
		}
	}
	if got, err := os.ReadFile(ws.repo.PrivateAttributesPath); err != nil || string(got) != privateAttributes {
		t.Fatalf("completed attributes repair = %q, %v", got, err)
	}
}

func TestDoctorDoesNotReportCallbackSatisfiedActionAsApplied(t *testing.T) {
	ws := newDoctorWorkspace(t, false)
	if err := os.Remove(ws.repo.PrivateAttributesPath); err != nil {
		t.Fatal(err)
	}

	result, err := ws.DoctorWithPlan(context.Background(), DoctorOptions{Repair: true}, func([]DoctorAction) error {
		return os.WriteFile(ws.repo.PrivateAttributesPath, []byte(privateAttributes), 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Planned) != 1 || result.Planned[0].Code != "attributes-private" {
		t.Fatalf("planned = %#v, want attributes repair", result.Planned)
	}
	if len(result.Applied) != 0 {
		t.Fatalf("applied = %#v, want callback-satisfied no-op omitted", result.Applied)
	}
	if len(result.Issues) != 0 {
		t.Fatalf("post-repair issues = %#v, want none", result.Issues)
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
