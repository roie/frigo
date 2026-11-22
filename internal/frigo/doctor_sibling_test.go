package frigo

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	gitpkg "github.com/roie/frigo/internal/git"
	"github.com/roie/frigo/internal/metadata"
	"github.com/roie/frigo/internal/repository"
	"github.com/roie/frigo/internal/testrepo"
)

func TestDoctorRepairsAssociatedSiblingStore(t *testing.T) {
	for _, invocation := range []string{"main", "uninitialized-main", "sibling"} {
		for _, damage := range []string{"attributes-public", "attributes-private", "lifecycle-lock-missing"} {
			t.Run(invocation+"/"+damage, func(t *testing.T) {
				target, mainRoot, linkedRoot := newLinkedWorkspace(t)
				testrepo.Write(t, linkedRoot, "private.txt", "private\n")
				if _, err := target.Add(t.Context(), []string{"private.txt"}); err != nil {
					t.Fatal(err)
				}
				invokingRoot := mainRoot
				if invocation == "sibling" {
					invokingRoot = filepath.Join(filepath.Dir(mainRoot), filepath.Base(mainRoot)+"-sibling")
					testrepo.Run(t, mainRoot, "worktree", "add", "-q", "-b", "doctor-sibling", invokingRoot)
					t.Cleanup(func() { _ = os.RemoveAll(invokingRoot) })
				}
				repo, err := repository.Discover(t.Context(), gitpkg.Client{Path: "git"}, invokingRoot)
				if err != nil {
					t.Fatal(err)
				}
				invoker := NewWorkspace(repo, gitpkg.Client{Path: "git"}, invokingRoot)
				if invocation != "uninitialized-main" {
					testrepo.Write(t, invokingRoot, "invoker.txt", "private invoker\n")
					if _, err := invoker.Add(t.Context(), []string{"invoker.txt"}); err != nil {
						t.Fatal(err)
					}
				}
				path := target.repo.AttributesPath
				switch damage {
				case "attributes-private":
					path = target.repo.PrivateAttributesPath
				case "lifecycle-lock-missing":
					path = filepath.Join(target.repo.GitDir, "locked")
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				assertDoctorIssue(t, diagnoseDoctor(t, invoker), damage, path, true)
				result, err := invoker.Doctor(t.Context(), DoctorOptions{Repair: true})
				if err != nil {
					t.Fatal(err)
				}
				if len(result.Issues) != 0 || len(result.Applied) != 1 || result.Applied[0].Code != damage || result.Applied[0].Path != path || !slices.Equal(result.Planned, result.Applied) {
					t.Fatalf("repair = %#v, want exactly the sibling repair", result)
				}
				if _, err := target.List(t.Context(), nil); err != nil {
					t.Fatalf("repaired sibling: %v", err)
				}
				if invocation == "uninitialized-main" {
					if _, err := os.Lstat(invoker.repo.RegistryPath); !os.IsNotExist(err) {
						t.Fatalf("repair initialized invoking store: %v", err)
					}
				}
			})
		}
	}
}

func TestDoctorSiblingRepairRetainsAssociationSafeguards(t *testing.T) {
	for _, damage := range []string{"foreign-lock", "ambiguous-pointer"} {
		t.Run(damage, func(t *testing.T) {
			target, mainRoot, linkedRoot := newLinkedWorkspace(t)
			testrepo.Write(t, linkedRoot, "private.txt", "private\n")
			if _, err := target.Add(t.Context(), []string{"private.txt"}); err != nil {
				t.Fatal(err)
			}
			lockPath := filepath.Join(target.repo.GitDir, "locked")
			if damage == "foreign-lock" {
				if err := os.WriteFile(lockPath, []byte("foreign owner\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				otherRoot := filepath.Join(filepath.Dir(mainRoot), filepath.Base(mainRoot)+"-ambiguous")
				testrepo.Run(t, mainRoot, "worktree", "add", "-q", "-b", "ambiguous", otherRoot)
				t.Cleanup(func() { _ = os.RemoveAll(otherRoot) })
				other, err := repository.Discover(t.Context(), gitpkg.Client{Path: "git"}, otherRoot)
				if err != nil {
					t.Fatal(err)
				}
				id, err := metadata.LoadPointer(target.repo.WorktreeIDPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := metadata.SavePointer(other.WorktreeIDPath, id); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target.repo.PrivateAttributesPath, []byte("foreign attributes\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			repo, err := repository.Discover(t.Context(), gitpkg.Client{Path: "git"}, mainRoot)
			if err != nil {
				t.Fatal(err)
			}
			before := snapshotManagedTree(t, target.repo.FrigoDir)
			beforeLock := snapshotManagedPath(t, lockPath)
			invoker := NewWorkspace(repo, gitpkg.Client{Path: "git"}, mainRoot)
			result, err := invoker.Doctor(t.Context(), DoctorOptions{Repair: true})
			if err != nil || len(result.Issues) == 0 || len(result.Applied) != 0 {
				t.Fatalf("unsafe repair = %#v, %v", result, err)
			}
			assertManagedTreeUnchanged(t, target.repo.FrigoDir, before)
			assertManagedPathUnchanged(t, lockPath, beforeLock)
		})
	}
}
