package frigo

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roie/frigo/internal/git"
	"github.com/roie/frigo/internal/testrepo"
)

func TestEstablishedHistoryRejectsUnexpectedPublicAttributes(t *testing.T) {
	for _, linked := range []bool{false, true} {
		kind := "main"
		if linked {
			kind = "linked"
		}
		t.Run(kind, func(t *testing.T) {
			for _, damage := range []string{"missing", "nonempty", "symlink", "directory"} {
				t.Run(damage, func(t *testing.T) {
					var ws *Workspace
					var root string
					if linked {
						ws, _, root = newLinkedWorkspace(t)
					} else {
						ws, root = newBareWorkspace(t)
					}
					testrepo.Write(t, root, "notes/a", "saved a\n")
					testrepo.Write(t, root, "notes/z", "saved z\n")
					ctx := context.Background()
					if _, err := ws.Add(ctx, []string{"notes"}); err != nil {
						t.Fatal(err)
					}
					if _, err := ws.Commit(ctx, CommitOptions{All: true, Message: "save"}); err != nil {
						t.Fatal(err)
					}
					testrepo.Write(t, root, "notes/a", "changed a\n")
					testrepo.Write(t, root, "notes/z", "changed z\n")
					if err := os.Remove(ws.repo.AttributesPath); err != nil {
						t.Fatal(err)
					}
					switch damage {
					case "nonempty":
						if err := os.WriteFile(ws.repo.AttributesPath, []byte("notes/z\n"), 0o600); err != nil {
							t.Fatal(err)
						}
					case "symlink":
						target := filepath.Join(t.TempDir(), "empty")
						if err := os.WriteFile(target, nil, 0o600); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(target, ws.repo.AttributesPath); err != nil {
							t.Skipf("symlink unavailable: %v", err)
						}
					case "directory":
						if err := os.Mkdir(ws.repo.AttributesPath, 0o700); err != nil {
							t.Fatal(err)
						}
					}
					beforeStore := snapshotManagedTree(t, ws.repo.FrigoDir)
					beforeExclude := snapshotManagedPath(t, ws.repo.ExcludePath)
					operations := []struct {
						name string
						run  func() error
					}{
						{"status", func() error { _, err := ws.Status(ctx, nil); return err }},
						{"status-porcelain", func() error { _, err := ws.StatusPorcelain(ctx, nil); return err }},
						{"diff", func() error { _, err := ws.Diff(ctx, nil); return err }},
						{"diff-patch", func() error {
							got, err := ws.DiffPatch(ctx, nil)
							if len(got) != 0 {
								t.Errorf("failed diff returned bytes: %q", got)
							}
							return err
						}},
						{"log", func() error { _, err := ws.Log(ctx); return err }},
						{"log-porcelain", func() error { _, err := ws.LogPorcelain(ctx, LogOptions{}); return err }},
						{"show", func() error { _, err := ws.Show(ctx, "HEAD", nil); return err }},
						{"show-patch", func() error { _, err := ws.ShowPatch(ctx, "HEAD", nil); return err }},
						{"show-name-status", func() error { _, err := ws.ShowNameStatus(ctx, "HEAD", nil); return err }},
						{"show-blob", func() error { _, err := ws.ShowBlob(ctx, "HEAD", "notes/a"); return err }},
						{"commit", func() error { _, err := ws.Commit(ctx, CommitOptions{All: true, Message: "reject"}); return err }},
						{"restore", func() error { _, err := ws.Restore(ctx, []string{"notes"}); return err }},
						{"release", func() error { _, err := ws.Release(ctx, []string{"notes"}, true); return err }},
						{"add", func() error { _, err := ws.Add(ctx, []string{"notes"}); return err }},
					}
					for _, operation := range operations {
						t.Run(operation.name, func(t *testing.T) {
							if err := operation.run(); err == nil || !strings.Contains(err.Error(), "attributes") {
								t.Fatalf("error = %v, want public attributes rejection", err)
							}
							assertManagedTreeUnchanged(t, ws.repo.FrigoDir, beforeStore)
							assertManagedPathUnchanged(t, ws.repo.ExcludePath, beforeExclude)
							if got := testrepo.Read(t, root, "notes/a"); got != "changed a\n" {
								t.Fatalf("worktree changed: %q", got)
							}
						})
					}
				})
			}
		})
	}
}

func TestPublicAttributesOrderFileCannotChangeMachinePatch(t *testing.T) {
	ws, root := workspaceWithOwnership(t, "notes")
	testrepo.Write(t, root, "notes/a", "a\n")
	testrepo.Write(t, root, "notes/z", "z\n")
	saveForTest(t, ws, "save")
	testrepo.Write(t, root, "notes/a", "new a\n")
	testrepo.Write(t, root, "notes/z", "new z\n")
	ctx := context.Background()
	before, err := ws.DiffPatch(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ws.repo.AttributesPath, []byte("notes/z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Prove the same bytes reorder Git's patch when used as its order file.
	var reordered []byte
	err = ws.withTemporaryIndexAt(ctx, historyBase{}, nil, func(client git.Client) error {
		var err error
		args := append(ws.machinePatchArgs(), "HEAD", "--", "notes")
		reordered, err = ws.privateOutputBytes(ctx, client, args...)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(before, reordered) || bytes.Index(reordered, []byte("diff --git a/notes/z")) >= bytes.Index(reordered, []byte("diff --git a/notes/a")) {
		t.Fatalf("order-file reproduction failed: %q", reordered)
	}
	got, err := ws.DiffPatch(ctx, nil)
	if err == nil || !strings.Contains(err.Error(), "attributes") || len(got) != 0 {
		t.Fatalf("DiffPatch() = %q, %v; want fail closed", got, err)
	}
	got, err = ws.ShowPatch(ctx, "HEAD", nil)
	if err == nil || !strings.Contains(err.Error(), "attributes") || len(got) != 0 {
		t.Fatalf("ShowPatch() = %q, %v; want fail closed", got, err)
	}
}

func TestPublicAttributesDoctorRepairBoundaries(t *testing.T) {
	for _, damage := range []string{"missing", "nonempty", "symlink"} {
		t.Run(damage, func(t *testing.T) {
			ws := newDoctorWorkspace(t, false)
			ctx := context.Background()
			if _, err := ws.Commit(ctx, CommitOptions{All: true, Message: "save"}); err != nil {
				t.Fatal(err)
			}
			base, err := ws.resolveHistoryBase(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(ws.repo.AttributesPath); err != nil {
				t.Fatal(err)
			}
			switch damage {
			case "nonempty":
				if err := os.WriteFile(ws.repo.AttributesPath, []byte("foreign\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(t.TempDir(), "empty")
				if err := os.WriteFile(target, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, ws.repo.AttributesPath); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}
			before := snapshotManagedTree(t, ws.repo.FrigoDir)
			diagnosed := diagnoseDoctor(t, ws)
			assertDoctorIssue(t, diagnosed, "attributes-public", ws.repo.AttributesPath, damage != "symlink")
			assertManagedTreeUnchanged(t, ws.repo.FrigoDir, before)
			repaired, err := ws.Doctor(ctx, DoctorOptions{Repair: true})
			if err != nil {
				t.Fatal(err)
			}
			if damage == "symlink" {
				assertDoctorIssue(t, repaired, "attributes-public", ws.repo.AttributesPath, false)
				assertManagedTreeUnchanged(t, ws.repo.FrigoDir, before)
			} else {
				if len(repaired.Issues) != 0 || len(repaired.Applied) != 1 || repaired.Applied[0].Code != "attributes-public" {
					t.Fatalf("repair = %#v", repaired)
				}
				if err := requireManagedFileContents(ws.repo.AttributesPath, nil); err != nil {
					t.Fatal(err)
				}
				if _, err := ws.ShowPatch(ctx, "HEAD", nil); err != nil {
					t.Fatalf("repaired history: %v", err)
				}
			}
			assertHistoryHead(t, ws, base.OID)
		})
	}
}

func TestCommitAbortsWhenStagedPathInspectionFails(t *testing.T) {
	ws, root := committedWorkspace(t, "notes", "old\n")
	testrepo.Write(t, root, "notes", "new\n")
	base, err := ws.resolveHistoryBase(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	failing := NewWorkspace(ws.repo, failingGitClient(t, ws.repo.HistoryDir, "ls-files", "--cached"), root)
	result, err := failing.Commit(context.Background(), CommitOptions{All: true, Message: "reject"})
	if err == nil || !strings.Contains(err.Error(), "inspect staged frigo paths") || result.Committed {
		t.Fatalf("Commit() = %#v, %v", result, err)
	}
	assertHistoryHead(t, ws, base.OID)
	assertNoTemporaryIndexes(t, ws)
	assertNoPersistentIndex(t, ws)
}
