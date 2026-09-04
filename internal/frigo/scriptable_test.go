package frigo

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/roie/frigo/internal/testrepo"
)

func TestMachinePatchesIgnoreDiffConfiguration(t *testing.T) {
	ws, root := workspaceWithOwnership(t, "docs")
	ctx := context.Background()
	for _, name := range []string{"docs/a.md", "docs/z.md"} {
		testrepo.Write(t, root, name, "before\n\ncontext\n")
	}
	saveForTest(t, ws, "first")
	for _, name := range []string{"docs/a.md", "docs/z.md"} {
		testrepo.Write(t, root, name, "after\n\ncontext\n")
	}
	saveForTest(t, ws, "second")
	t.Chdir(filepath.Join(root, "docs"))
	changes, err := ws.ShowNameStatus(ctx, "HEAD", nil)
	if err != nil {
		t.Fatal(err)
	}
	historical, err := ws.ShowPatch(ctx, "HEAD", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"docs/a.md", "docs/z.md"} {
		testrepo.Write(t, root, name, "third\n\ncontext\n")
	}
	current, err := ws.DiffPatch(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	order := filepath.Join(t.TempDir(), "order")
	if err := os.WriteFile(order, []byte("docs/z.md\ndocs/a.md\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"diff.orderFile":           order,
		"diff.relative":            "true",
		"diff.suppressBlankEmpty":  "true",
		"diff.compactionHeuristic": "true",
		"diff.noprefix":            "true",
		"diff.mnemonicPrefix":      "true",
		"diff.context":             "0",
		"diff.interHunkContext":    "20",
		"diff.algorithm":           "histogram",
		"diff.indentHeuristic":     "true",
		"core.abbrev":              "12",
		"diff.submodule":           "log",
	} {
		t.Run(key, func(t *testing.T) {
			if _, err := ws.privateOutput(ctx, ws.git, "config", key, value); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := ws.privateOutput(ctx, ws.git, "config", "--unset", key); err != nil {
					t.Error(err)
				}
			}()
			got, err := ws.DiffPatch(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, current) {
				t.Errorf("current patch changed with %s\ngot: %q\nwant: %q", key, got, current)
			}
			got, err = ws.ShowPatch(ctx, "HEAD", nil)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, historical) {
				t.Errorf("historical patch changed with %s\ngot: %q\nwant: %q", key, got, historical)
			}
			got, err = ws.ShowNameStatus(ctx, "HEAD", nil)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, changes) {
				t.Errorf("changed paths changed with %s\ngot: %q\nwant: %q", key, got, changes)
			}
		})
	}
}
