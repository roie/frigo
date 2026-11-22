package frigo

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gitpkg "github.com/roie/frigo/internal/git"
	"github.com/roie/frigo/internal/repository"
	"github.com/roie/frigo/internal/testrepo"
)

func TestSiblingAddPreservesExclusionsWithWrongValidPointer(t *testing.T) {
	ctx := t.Context()
	linked, mainRoot, linkedRoot := newLinkedWorkspace(t)
	testrepo.Write(t, linkedRoot, "first-notes.md", "private\n")
	if _, err := linked.Add(ctx, []string{"first-notes.md"}); err != nil {
		t.Fatal(err)
	}

	otherRoot := filepath.Join(t.TempDir(), "other")
	testrepo.Run(t, mainRoot, "worktree", "add", "-q", "-b", "other-branch", otherRoot)
	client := gitpkg.Client{Path: "git"}
	otherRepo, err := repository.Discover(ctx, client, otherRoot)
	if err != nil {
		t.Fatal(err)
	}
	testrepo.Write(t, otherRoot, "other-notes.md", "private\n")
	if _, err := NewWorkspace(otherRepo, client, otherRoot).Add(ctx, []string{"other-notes.md"}); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(linked.repo.ExcludePath)
	if err != nil {
		t.Fatal(err)
	}
	pointer, err := os.ReadFile(otherRepo.WorktreeIDPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(linked.repo.WorktreeIDPath, pointer, 0o600); err != nil {
		t.Fatal(err)
	}

	mainRepo, err := repository.Discover(ctx, client, mainRoot)
	if err != nil {
		t.Fatal(err)
	}
	testrepo.Write(t, mainRoot, "main-notes.md", "private\n")
	_, addErr := NewWorkspace(mainRepo, client, mainRoot).Add(ctx, []string{"main-notes.md"})
	if got := testrepo.Output(t, linkedRoot, "status", "--porcelain", "--untracked-files=all"); got != "" {
		t.Fatalf("managed file became visible to Git after sibling add: %q (add error: %v)", got, addErr)
	}
	after, err := os.ReadFile(mainRepo.ExcludePath)
	if err != nil {
		t.Fatal(err)
	}
	if addErr == nil || !bytes.Equal(before, after) {
		t.Fatalf("sibling add error = %v; exclusions before = %q, after = %q", addErr, before, after)
	}
}

func TestSiblingOperationsPreserveExclusionsWithDamagedPointer(t *testing.T) {
	for _, damage := range []string{"missing", "malformed"} {
		t.Run(damage, func(t *testing.T) {
			ctx := t.Context()
			linked, mainRoot, linkedRoot := newLinkedWorkspace(t)
			testrepo.Write(t, linkedRoot, "secret.txt", "private\n")
			if _, err := linked.Add(ctx, []string{"secret.txt"}); err != nil {
				t.Fatal(err)
			}
			mainRepo, err := repository.Discover(ctx, gitpkg.Client{Path: "git"}, mainRoot)
			if err != nil {
				t.Fatal(err)
			}
			main := NewWorkspace(mainRepo, gitpkg.Client{Path: "git"}, mainRoot)
			testrepo.Write(t, mainRoot, "existing.txt", "private\n")
			if _, err := main.Add(ctx, []string{"existing.txt"}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(mainRepo.ExcludePath)
			if err != nil {
				t.Fatal(err)
			}
			if damage == "missing" {
				err = os.Remove(linked.repo.WorktreeIDPath)
			} else {
				err = os.WriteFile(linked.repo.WorktreeIDPath, []byte("invalid\n"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			testrepo.Write(t, mainRoot, "other.txt", "private\n")
			if _, err := main.Add(ctx, []string{"other.txt"}); err == nil {
				t.Fatal("add accepted damaged sibling pointer")
			}
			if _, err := main.Release(ctx, []string{"existing.txt"}, true); err == nil {
				t.Fatal("release accepted damaged sibling pointer")
			}
			if _, err := main.Doctor(ctx, DoctorOptions{Repair: true}); err != nil {
				t.Fatalf("doctor: %v", err)
			}
			after, err := os.ReadFile(mainRepo.ExcludePath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatalf("exclude changed: %q -> %q", before, after)
			}
			for _, args := range [][]string{{"status", "--porcelain", "--untracked-files=all"}, {"add", "-n", "-A"}} {
				out, err := exec.Command("git", append([]string{"-C", linkedRoot}, args...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
				if strings.Contains(string(out), "secret.txt") {
					t.Fatalf("git %v exposed secret: %s", args, out)
				}
			}
			if damage == "missing" {
				if _, err := linked.Doctor(ctx, DoctorOptions{Repair: true}); err != nil {
					t.Fatalf("repair pointer: %v", err)
				}
				if _, err := os.Stat(linked.repo.WorktreeIDPath); err != nil {
					t.Fatalf("pointer not repaired: %v", err)
				}
			}
		})
	}
}
