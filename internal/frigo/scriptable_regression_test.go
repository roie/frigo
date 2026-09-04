package frigo

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/roie/frigo/internal/testexec"
	"github.com/roie/frigo/internal/testrepo"
)

func TestDiffPatchIncludesAdditionsAndDirectoryDeletions(t *testing.T) {
	for _, saved := range []bool{false, true} {
		t.Run(fmt.Sprintf("saved=%v", saved), func(t *testing.T) {
			ws, root := workspaceWithOwnership(t, "docs")
			if saved {
				testrepo.Write(t, root, "docs/deleted", "old\n")
				testrepo.Write(t, root, "docs/sibling", "keep\n")
				saveForTest(t, ws, "save directory")
				if err := os.Remove(filepath.Join(root, "docs/deleted")); err != nil {
					t.Fatal(err)
				}
			}
			testrepo.Write(t, root, "docs/empty", "")
			testrepo.Write(t, root, "docs/new", "new\n")
			got, err := ws.DiffPatch(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			want := "diff --git a/docs/empty b/docs/empty\nnew file mode 100644\nindex 0000000000000000000000000000000000000000..e69de29bb2d1d6434b8b29ae775ad8c2e48c5391\n" +
				"diff --git a/docs/new b/docs/new\nnew file mode 100644\nindex 0000000000000000000000000000000000000000..3e757656cf36eca53338e520d134963a44f793f8\n--- /dev/null\n+++ b/docs/new\n@@ -0,0 +1 @@\n+new\n"
			if saved {
				want = "diff --git a/docs/deleted b/docs/deleted\ndeleted file mode 100644\nindex 3367afdbbf91e638efe983616377c60477cc6612..0000000000000000000000000000000000000000\n--- a/docs/deleted\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n" + want
			}
			if string(got) != want {
				t.Fatalf("DiffPatch() = %q, want %q", got, want)
			}
			assertNoTemporaryIndexes(t, ws)
		})
	}
}

func TestShowNameStatusIgnoresMissingOrderFile(t *testing.T) {
	ws, root := committedWorkspace(t, "PLAN.md", "saved\n")
	config := filepath.Join(t.TempDir(), ".gitconfig")
	testrepo.Run(t, root, "config", "--file", config, "diff.orderFile", filepath.Join(t.TempDir(), "missing"))
	ws.git = ws.git.WithEnv("GIT_CONFIG_GLOBAL="+config, "HOME="+filepath.Dir(config))
	got, err := ws.ShowNameStatus(context.Background(), "HEAD", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "A\x00PLAN.md\x00" {
		t.Fatalf("ShowNameStatus() = %q", got)
	}
}

func TestMachineHistoryIgnoresOIDNamedFile(t *testing.T) {
	ws, root := committedWorkspace(t, "PLAN.md", "saved\n")
	t.Chdir(root)
	ctx := context.Background()
	base, err := ws.resolveHistoryBase(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reads := map[string]func() ([]byte, error){
		"log":         func() ([]byte, error) { return ws.LogPorcelain(ctx, LogOptions{}) },
		"name-status": func() ([]byte, error) { return ws.ShowNameStatus(ctx, "HEAD", nil) },
		"patch":       func() ([]byte, error) { return ws.ShowPatch(ctx, "HEAD", nil) },
		"blob":        func() ([]byte, error) { return ws.ShowBlob(ctx, "HEAD", "PLAN.md") },
	}
	expected := map[string][]byte{}
	for name, read := range reads {
		got, err := read()
		if err != nil {
			t.Fatal(err)
		}
		expected[name] = got
	}
	testrepo.Write(t, root, base.OID, "a file, not a revision\n")
	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			got, err := read()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, expected[name]) {
				t.Fatalf("got %q, want %q", got, expected[name])
			}
		})
	}
}

func TestStatusPorcelainAndDiffPatchDisableFSMonitor(t *testing.T) {
	ws, root := committedWorkspace(t, "PLAN.md", "saved\n")
	ctx := context.Background()
	marker := filepath.Join(t.TempDir(), "fsmonitor-ran")
	hook := filepath.ToSlash(testexec.Build(t))
	config := filepath.Join(t.TempDir(), ".gitconfig")
	testrepo.Run(t, root, "config", "--file", config, "core.fsmonitor", hook)
	// Git 2.23 predates GIT_CONFIG_GLOBAL, so also supply a private HOME.
	client := ws.git.WithEnv("GIT_CONFIG_GLOBAL="+config, "HOME="+filepath.Dir(config), "FRIGO_MARKER_FILE="+marker)
	// First prove this executable really is invoked as a Git fsmonitor hook.
	control := client.WithEnv("GIT_INDEX_FILE=" + filepath.Join(t.TempDir(), "control-index"))
	args := []string{"--git-dir=" + ws.repo.HistoryDir, "--work-tree=" + root}
	if _, err := control.Output(ctx, root, append(args, "read-tree", "HEAD")...); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Output(ctx, root, append(args, "status", "--short")...); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("control did not invoke fsmonitor: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	ws.git = client
	testrepo.Write(t, root, "PLAN.md", "changed\n")
	got, err := ws.StatusPorcelain(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != " M PLAN.md\x00" {
		t.Fatalf("status = %q", got)
	}
	if _, err := ws.DiffPatch(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("private machine read invoked fsmonitor: %v", err)
	}
}

func TestDiffPatchUsesCapturedBaseAfterExternalRefUpdate(t *testing.T) {
	ws, root := committedWorkspace(t, "PLAN.md", "base\n")
	base, winner := createWinnerAndRestoreBase(t, ws, root, "PLAN.md", "winner\n")
	want, err := ws.DiffPatch(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 {
		t.Fatal("expected a change against the captured base")
	}
	changing := NewWorkspace(ws.repo, externalHeadChangeGitClient(t, ws.repo.HistoryDir, winner, base, "diff"), root)
	got, err := changing.DiffPatch(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("DiffPatch() = %q, want captured-base patch %q", got, want)
	}
	assertHistoryHead(t, ws, winner)
	assertNoTemporaryIndexes(t, changing)
}
