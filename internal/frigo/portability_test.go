package frigo

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitpkg "github.com/roie/frigo/internal/git"
	"github.com/roie/frigo/internal/repository"
	"github.com/roie/frigo/internal/testexec"
	"github.com/roie/frigo/internal/testrepo"
)

func TestHistoryPathConfigurationCompatibility(t *testing.T) {
	for _, linked := range []bool{false, true} {
		for _, legacy := range []bool{false, true} {
			name := map[bool]string{false: "main", true: "linked"}[linked] + "/" + map[bool]string{false: "missing", true: "stale"}[legacy]
			t.Run(name, func(t *testing.T) {
				var ws *Workspace
				var root string
				if linked {
					ws, _, root = newLinkedWorkspace(t)
				} else {
					ws, root = newBareWorkspace(t)
				}
				testrepo.Write(t, root, "notes.txt", "private\n")
				if _, err := ws.Add(t.Context(), []string{"notes.txt"}); err != nil {
					t.Fatal(err)
				}
				assertNoPersistedHistoryPaths(t, ws)
				if legacy {
					for _, key := range []string{"core.hooksPath", "core.attributesFile"} {
						testrepo.Run(t, root, "--git-dir="+ws.repo.HistoryDir, "config", key, filepath.Join(t.TempDir(), "missing"))
					}
				}
				before := snapshotManagedPath(t, filepath.Join(ws.repo.HistoryDir, "config"))
				if err := ws.validateInitializedHistory(t.Context()); err != nil {
					t.Fatal(err)
				}
				for _, repair := range []bool{false, true} {
					result, err := ws.Doctor(t.Context(), DoctorOptions{Repair: repair})
					if err != nil || len(result.Issues) != 0 || len(result.Applied) != 0 {
						t.Fatalf("Doctor(repair=%v) = %#v, %v", repair, result, err)
					}
				}
				if result, err := ws.Commit(t.Context(), CommitOptions{All: true, Message: "save"}); err != nil || !result.Committed {
					t.Fatalf("Commit() = %#v, %v", result, err)
				}
				assertManagedPathUnchanged(t, filepath.Join(ws.repo.HistoryDir, "config"), before)
			})
		}
	}
}

func TestRepositoryRelocationPreservesPrivateIsolation(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "legacy"}[legacy], func(t *testing.T) {
			root := t.TempDir()
			testrepo.Run(t, root, "init", "--quiet")
			testrepo.Run(t, root, "config", "user.name", "Frigo Test")
			testrepo.Run(t, root, "config", "user.email", "frigo@example.invalid")
			testrepo.Write(t, root, "README.md", "main\n")
			testrepo.CommitAll(t, root, "initial", "README.md")
			repo, err := repository.Discover(t.Context(), gitpkg.Client{Path: "git"}, root)
			if err != nil {
				t.Fatal(err)
			}
			ws := NewWorkspace(repo, gitpkg.Client{Path: "git"}, root)
			testrepo.Write(t, root, "notes.txt", "saved\r\n$Id$\n")
			if _, err := ws.Add(t.Context(), []string{"notes.txt"}); err != nil {
				t.Fatal(err)
			}
			assertNoPersistedHistoryPaths(t, ws)
			if _, err := ws.Commit(t.Context(), CommitOptions{All: true, Message: "before move"}); err != nil {
				t.Fatal(err)
			}
			oldHooks, oldAttributes := ws.repo.HooksDir, ws.repo.AttributesPath
			if legacy {
				testrepo.Run(t, root, "--git-dir="+ws.repo.HistoryDir, "config", "core.hooksPath", oldHooks)
				testrepo.Run(t, root, "--git-dir="+ws.repo.HistoryDir, "config", "core.attributesFile", oldAttributes)
			}
			beforeConfig := snapshotManagedPath(t, filepath.Join(ws.repo.HistoryDir, "config"))
			destination, err := os.MkdirTemp(filepath.Dir(root), "frigo-relocated-*")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(destination) })
			moved := filepath.Join(destination, "renamed")
			if err := os.Rename(root, moved); err != nil {
				t.Fatal(err)
			}
			repo, err = repository.Discover(t.Context(), ws.git, moved)
			if err != nil {
				t.Fatal(err)
			}
			ws = NewWorkspace(repo, ws.git, moved)
			root = moved

			if err := os.MkdirAll(oldHooks, 0o700); err != nil {
				t.Fatal(err)
			}
			executable, err := os.ReadFile(testexec.Build(t))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(oldHooks, "post-index-change"), executable, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(oldAttributes, []byte("* text eol=lf ident filter=frigo-portability\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			global := filepath.Join(t.TempDir(), ".gitconfig")
			for key, value := range map[string]string{
				"core.hooksPath": oldHooks, "core.attributesFile": oldAttributes,
				"filter.frigo-portability.clean":    filepath.ToSlash(testexec.Build(t)),
				"filter.frigo-portability.required": "true",
			} {
				testrepo.Run(t, root, "config", "--file", global, key, value)
			}
			marker := filepath.Join(t.TempDir(), "foreign-code-ran")
			ws.git = ws.git.WithEnv("GIT_CONFIG_GLOBAL="+global, "HOME="+filepath.Dir(global), "FRIGO_MARKER_FILE="+marker)
			control := ws.git.WithEnv("GIT_INDEX_FILE=" + filepath.Join(t.TempDir(), "control-index"))
			if _, err := control.Output(t.Context(), root, "--git-dir="+ws.repo.HistoryDir, "--work-tree="+root, "read-tree", "HEAD"); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(marker); err != nil {
				t.Fatalf("control did not run the foreign hook: %v", err)
			}
			filtered, err := ws.git.OutputWithInput(t.Context(), root, "saved\r\n$Id$\n", "hash-object", "--stdin", "--path=notes.txt")
			if err != nil {
				t.Fatal(err)
			}
			raw, err := ws.git.OutputWithInput(t.Context(), root, "saved\r\n$Id$\n", "hash-object", "--stdin")
			if err != nil || raw == filtered {
				t.Fatalf("attribute control did not change file handling: raw=%q, filtered=%q, err=%v", raw, filtered, err)
			}
			if err := os.Remove(marker); err != nil {
				t.Fatalf("control did not run the foreign filter: %v", err)
			}
			assertIsolated := func() {
				t.Helper()
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("private operation invoked a foreign hook or filter: %v", err)
				}
			}
			if err := ws.validateInitializedHistory(t.Context()); err != nil {
				t.Fatal(err)
			}
			for _, repair := range []bool{false, true} {
				result, err := ws.Doctor(t.Context(), DoctorOptions{Repair: repair})
				if err != nil || len(result.Issues) != 0 || len(result.Applied) != 0 {
					t.Fatalf("relocated Doctor(repair=%v) = %#v, %v", repair, result, err)
				}
			}
			if status, err := ws.Status(t.Context(), nil); err != nil || status != "" {
				t.Fatalf("relocated Status() = %q, %v", status, err)
			}
			assertIsolated()
			changed := "changed\r\n$Id$\n"
			testrepo.Write(t, root, "notes.txt", changed)
			if status, err := ws.StatusPorcelain(t.Context(), nil); err != nil || string(status) != " M notes.txt\x00" {
				t.Fatalf("relocated StatusPorcelain() = %q, %v", status, err)
			}
			assertIsolated()
			if diff, err := ws.DiffPatch(t.Context(), nil); err != nil || !bytes.Contains(diff, []byte("+changed\r\n")) {
				t.Fatalf("relocated DiffPatch() = %q, %v", diff, err)
			}
			if result, err := ws.Commit(t.Context(), CommitOptions{All: true, Message: "after move"}); err != nil || !result.Committed {
				t.Fatalf("relocated Commit() = %#v, %v", result, err)
			}
			if blob, err := ws.ShowBlob(t.Context(), "HEAD", "notes.txt"); err != nil || string(blob) != changed {
				t.Fatalf("relocated ShowBlob() = %q, %v", blob, err)
			}
			if log, err := ws.Log(t.Context()); err != nil || !strings.Contains(log, "after move") {
				t.Fatalf("relocated Log() = %q, %v", log, err)
			}
			testrepo.Write(t, root, "notes.txt", "dirty\n")
			if _, err := ws.Restore(t.Context(), []string{"notes.txt"}); err != nil {
				t.Fatal(err)
			}
			if got := testrepo.Read(t, root, "notes.txt"); got != changed {
				t.Fatalf("relocated Restore() wrote %q, want %q", got, changed)
			}
			assertIsolated()
			assertManagedPathUnchanged(t, filepath.Join(ws.repo.HistoryDir, "config"), beforeConfig)
			assertNoPersistentIndex(t, ws)
			assertNoTemporaryIndexes(t, ws)
		})
	}
}

func assertNoPersistedHistoryPaths(t *testing.T, ws *Workspace) {
	t.Helper()
	for _, key := range []string{"core.hooksPath", "core.attributesFile"} {
		value, err := ws.git.Output(t.Context(), "", "--git-dir="+ws.repo.HistoryDir, "config", "--local", "--get", key)
		if code, ok := gitpkg.ExitCode(err); !ok || code != 1 {
			t.Fatalf("persisted %s = %q, %v; want absent", key, value, err)
		}
	}
}
