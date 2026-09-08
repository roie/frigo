package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roie/frigo/internal/metadata"
	"github.com/roie/frigo/internal/testrepo"
)

func TestMetadataDurabilityCLI(t *testing.T) {
	binary := buildCLI(t)
	for _, linked := range []bool{false, true} {
		kind := "main"
		if linked {
			kind = "linked"
		}
		for _, operation := range []string{"add", "release"} {
			for _, phase := range []string{"file-sync", "before-publish", "directory-sync", "cleanup"} {
				for _, file := range []string{"registry.json", "exclude", "manifest.json"} {
					if !linked && file == "manifest.json" {
						continue
					}
					t.Run(strings.Join([]string{kind, operation, phase, file}, "/"), func(t *testing.T) {
						root := durabilityRoot(t, linked)
						testrepo.Write(t, root, "private.local", "private\n")
						runCLI(t, binary, root, "add", "private.local")
						runCLI(t, binary, root, "commit", "-m", "private history", "private.local")
						if operation == "add" {
							runCLI(t, binary, root, "release", "--all", "--force")
						}
						repo := discoverRepository(t, root)
						regPath := repo.RegistryPath
						if linked {
							regPath = linkedRegistryPath(t, repo)
						}
						history := "--git-dir=" + filepath.Join(filepath.Dir(regPath), "history.git")
						head := testrepo.Output(t, root, history, "rev-parse", "HEAD")
						hook := "atomic-" + phase + "-" + file
						args := []string{"add", "private.local"}
						if operation == "release" {
							args = []string{"release", "--all", "--force"}
						}
						process := startCLI(t, binary, root, processHooks{fail: []string{hook}}, args...)
						assertProcessFailure(t, process, "induced test failure at "+hook)
						if got := testrepo.Output(t, root, history, "rev-parse", "HEAD"); got != head {
							t.Fatalf("HEAD changed: %s != %s", got, head)
						}
						if got := testrepo.Read(t, root, "private.local"); got != "private\n" {
							t.Fatalf("owned bytes changed: %q", got)
						}
						active := operation == "release"
						if phase == "directory-sync" || phase == "cleanup" {
							active = operation == "add"
						}
						if active {
							assertRegistryPaths(t, regPath, "private.local")
							assertExcludePatterns(t, repo.ExcludePath, "private.local")
						} else {
							assertRegistryPaths(t, regPath)
							assertExcludeOmits(t, repo.ExcludePath, "private.local")
						}
						if linked {
							manifest, err := metadata.Load(filepath.Join(filepath.Dir(regPath), "manifest.json"))
							if err != nil {
								t.Fatal(err)
							}
							_, lockErr := os.Stat(filepath.Join(repo.GitDir, "locked"))
							if manifest.LockOwned != active || (lockErr == nil) != active {
								t.Fatalf("active=%v manifest=%+v lock=%v", active, manifest, lockErr)
							}
						}
					})
				}
			}
		}
	}
}

func TestMetadataDurabilityLaterFailureStillRollsBack(t *testing.T) {
	binary := buildCLI(t)
	for _, operation := range []string{"add", "release"} {
		for _, later := range []string{"exclude-sync", "worktree-unlock-command"} {
			if operation == "add" && later == "worktree-unlock-command" {
				continue
			}
			t.Run(operation+"/"+later, func(t *testing.T) {
				root := durabilityRoot(t, true)
				testrepo.Write(t, root, "private.local", "private\n")
				runCLI(t, binary, root, "add", "private.local")
				if operation == "add" {
					runCLI(t, binary, root, "release", "--all", "--force")
				}
				repo := discoverRepository(t, root)
				args := []string{"add", "private.local"}
				if operation == "release" {
					args = []string{"release", "--all", "--force"}
				}
				process := startCLI(t, binary, root, processHooks{fail: []string{"atomic-directory-sync-registry.json", later}}, args...)
				assertProcessFailure(t, process, "induced test failure at "+later)
				if !strings.Contains(process.result.stderr, "atomic-directory-sync-registry.json") {
					t.Fatal("lost durability error", process.result.stderr)
				}
				regPath := linkedRegistryPath(t, repo)
				active := operation == "release"
				if active {
					assertRegistryPaths(t, regPath, "private.local")
					assertExcludePatterns(t, repo.ExcludePath, "private.local")
				} else {
					assertRegistryPaths(t, regPath)
					assertExcludeOmits(t, repo.ExcludePath, "private.local")
				}
				manifest, err := metadata.Load(filepath.Join(filepath.Dir(regPath), "manifest.json"))
				if err != nil {
					t.Fatal(err)
				}
				_, lockErr := os.Stat(filepath.Join(repo.GitDir, "locked"))
				if manifest.LockOwned != active || (lockErr == nil) != active {
					t.Fatalf("active=%v manifest=%+v lock=%v", active, manifest, lockErr)
				}
			})
		}
	}
}

func TestMetadataDurabilityInitialPublication(t *testing.T) {
	binary := buildCLI(t)
	for _, file := range []string{"manifest.json", "frigo-id", "attributes", "registry.json", "exclude"} {
		t.Run(file, func(t *testing.T) {
			root := durabilityRoot(t, true)
			testrepo.Write(t, root, "private.local", "private\n")
			hook := "atomic-directory-sync-" + file
			process := startCLI(t, binary, root, processHooks{fail: []string{hook}}, "add", "private.local")
			assertProcessFailure(t, process, "induced test failure at "+hook)
			// Initialization retains its published association evidence and can be resumed.
			repo := discoverRepository(t, root)
			stores, err := os.ReadDir(repo.LinkedStoresDir)
			if err != nil || len(stores) != 1 {
				t.Fatalf("stores=%v err=%v", stores, err)
			}
			runCLI(t, binary, root, "add", "private.local")
			after, err := os.ReadDir(repo.LinkedStoresDir)
			if err != nil || len(after) != 1 || after[0].Name() != stores[0].Name() {
				t.Fatalf("lost published store: %v %v", after, err)
			}
			assertRegistryPaths(t, linkedRegistryPath(t, repo), "private.local")
			assertExcludePatterns(t, repo.ExcludePath, "private.local")
		})
	}
}

func TestMetadataDurabilityDoctorReportsApplied(t *testing.T) {
	binary := buildCLI(t)
	for _, repair := range []string{"attributes-public", "attributes-private", "exclusions-stale", "association-pointer-missing", "association-pointer-mismatch", "lifecycle-lock-unowned"} {
		t.Run(repair, func(t *testing.T) {
			root := durabilityRoot(t, true)
			testrepo.Write(t, root, "private.local", "private\n")
			runCLI(t, binary, root, "add", "private.local")
			repo := discoverRepository(t, root)
			store := filepath.Dir(linkedRegistryPath(t, repo))
			filename := filepath.Join(store, "attributes")
			switch repair {
			case "attributes-private":
				filename = filepath.Join(store, "history.git", "info", "attributes")
			case "exclusions-stale":
				filename = repo.ExcludePath
			case "association-pointer-missing", "association-pointer-mismatch":
				filename = repo.WorktreeIDPath
			case "lifecycle-lock-unowned":
				manifestPath := filepath.Join(store, "manifest.json")
				manifest, err := metadata.Load(manifestPath)
				if err != nil {
					t.Fatal(err)
				}
				manifest.LockOwned = false
				if err := metadata.Save(manifestPath, manifest); err != nil {
					t.Fatal(err)
				}
				filename = manifestPath
			}
			if repair != "lifecycle-lock-unowned" {
				if err := os.Remove(filename); err != nil {
					t.Fatal(err)
				}
			}
			if repair == "exclusions-stale" {
				if err := os.WriteFile(filename, nil, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if repair == "association-pointer-mismatch" {
				if err := metadata.SavePointer(filename, strings.Repeat("f", 32)); err != nil {
					t.Fatal(err)
				}
			}
			if repair == "attributes-private" {
				if err := os.WriteFile(filename, []byte("unexpected\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			hook := "atomic-directory-sync-" + filepath.Base(filename)
			process := startCLI(t, binary, root, processHooks{fail: []string{hook}}, "doctor", "--repair")
			assertProcessFailure(t, process, "induced test failure at "+hook)
			if !strings.Contains(process.result.stdout, "applied "+repair+" ") {
				t.Fatalf("published repair not reported: %s", process.result.stdout)
			}
			runCLI(t, binary, root, "doctor")
		})
	}
}

func TestMetadataDurabilityInitialMainPublication(t *testing.T) {
	binary := buildCLI(t)
	for _, file := range []string{"attributes", "registry.json", "exclude"} {
		t.Run(file, func(t *testing.T) {
			root := durabilityRoot(t, false)
			testrepo.Write(t, root, "private.local", "private\n")
			hook := "atomic-directory-sync-" + file
			process := startCLI(t, binary, root, processHooks{fail: []string{hook}}, "add", "private.local")
			assertProcessFailure(t, process, "induced test failure at "+hook)
			repo := discoverRepository(t, root)
			assertRegistryPaths(t, repo.RegistryPath, "private.local")
			assertExcludePatterns(t, repo.ExcludePath, "private.local")
			runCLI(t, binary, root, "add", "private.local")
		})
	}
}

func TestMetadataDurabilityAddRollbackPublication(t *testing.T) {
	binary := buildCLI(t)
	root := durabilityRoot(t, true)
	testrepo.Write(t, root, "private.local", "private\n")
	runCLI(t, binary, root, "add", "private.local")
	runCLI(t, binary, root, "release", "--all", "--force")
	process := startCLI(t, binary, root, processHooks{fail: []string{"atomic-before-publish-registry.json", "atomic-directory-sync-registry.json"}}, "add", "private.local")
	assertProcessFailure(t, process, "atomic-before-publish-registry.json")
	if !strings.Contains(process.result.stderr, "atomic-directory-sync-registry.json") {
		t.Fatalf("lost rollback error: %s", process.result.stderr)
	}
	repo := discoverRepository(t, root)
	regPath := linkedRegistryPath(t, repo)
	assertRegistryPaths(t, regPath)
	assertExcludeOmits(t, repo.ExcludePath, "private.local")
	manifest, err := metadata.Load(filepath.Join(filepath.Dir(regPath), "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, lockErr := os.Stat(filepath.Join(repo.GitDir, "locked"))
	if manifest.LockOwned || !os.IsNotExist(lockErr) {
		t.Fatalf("rollback skipped protection cleanup: %+v %v", manifest, lockErr)
	}
}

func durabilityRoot(t *testing.T, linked bool) string {
	t.Helper()
	root := testrepo.Init(t)
	if !linked {
		return root
	}
	testrepo.Write(t, root, "README.md", "test\n")
	testrepo.CommitAll(t, root, "initial", "README.md")
	checkout := filepath.Join(t.TempDir(), "linked")
	testrepo.Run(t, root, "worktree", "add", "-q", "-b", "durability", checkout)
	return checkout
}
