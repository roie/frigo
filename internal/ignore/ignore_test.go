package ignore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frigo/internal/git"
	"frigo/internal/registry"
	"frigo/internal/repository"
	"frigo/internal/testrepo"
)

func TestLiteralPatternEscapesGitMetacharacters(t *testing.T) {
	got, err := LiteralPattern(`docs/My [local]* notes`)
	if err != nil {
		t.Fatal(err)
	}
	if want := `/docs/My\ \[local\]\*\ notes`; got != want {
		t.Fatalf("LiteralPattern() = %q, want %q", got, want)
	}
}

func TestSyncPreservesContentOutsideManagedSection(t *testing.T) {
	repo := testRepository(t)
	original := "keep one\n# >>> frigo >>>\n/old\n# <<< frigo <<<\nkeep two\n"
	if err := os.WriteFile(repo.ExcludePath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Sync(repo, registry.Registry{Version: registry.CurrentVersion, Paths: []string{"PLAN.md"}}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(repo.ExcludePath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(contents), "keep one\n# >>> frigo >>>\n/PLAN.md\n# <<< frigo <<<\nkeep two\n"; got != want {
		t.Fatalf("exclude = %q, want %q", got, want)
	}
}

func TestSyncUnionsMainAndLinkedWorktreeRegistries(t *testing.T) {
	root := testrepo.Init(t)
	mainRepo := discoverRepository(t, root)
	linkedRoot := filepath.Join(root, "linked")
	otherRoot := filepath.Join(root, "other")
	testrepo.Run(t, root, "worktree", "add", "-q", "-b", "linked-branch", linkedRoot)
	testrepo.Run(t, root, "worktree", "add", "-q", "-b", "other-branch", otherRoot)
	linkedRepo := discoverRepository(t, linkedRoot)
	otherRepo := discoverRepository(t, otherRoot)

	if err := registry.Save(mainRepo.RegistryPath, registry.Registry{Version: registry.CurrentVersion, Paths: []string{"main.txt"}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(linkedRepo.RegistryPath, registry.Registry{Version: registry.CurrentVersion, Paths: []string{"stale.txt"}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(otherRepo.RegistryPath, registry.Registry{Version: registry.CurrentVersion, Paths: []string{"other.txt"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(linkedRepo.ExcludePath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	owned := registry.Registry{Version: registry.CurrentVersion, Paths: []string{"current.txt"}}
	if err := Sync(linkedRepo, owned); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(linkedRepo.ExcludePath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(contents), "# >>> frigo >>>\n/current.txt\n/main.txt\n/other.txt\n# <<< frigo <<<\n"; got != want {
		t.Fatalf("exclude = %q, want %q", got, want)
	}
	if strings.Contains(string(contents), "/stale.txt") {
		t.Fatalf("exclude resurrected stale current-worktree registry path: %q", contents)
	}
}

func testRepository(t *testing.T) repository.Repository {
	t.Helper()
	root := testrepo.Init(t)
	return discoverRepository(t, root)
}

func discoverRepository(t *testing.T, root string) repository.Repository {
	t.Helper()
	repo, err := repository.Discover(context.Background(), git.Client{Path: "git"}, root)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}
