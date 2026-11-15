package frigo

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	gitpkg "github.com/roie/frigo/internal/git"
	"github.com/roie/frigo/internal/repository"
	"github.com/roie/frigo/internal/testrepo"
)

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
