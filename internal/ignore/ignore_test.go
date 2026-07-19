package ignore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roie/frigo/internal/git"
	"github.com/roie/frigo/internal/registry"
	"github.com/roie/frigo/internal/repository"
	"github.com/roie/frigo/internal/testrepo"
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
	root := testrepo.Init(t)
	repo := discoverRepository(t, root)
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

func TestSyncNonemptyThenEmptyRestoresExactOutsideBytes(t *testing.T) {
	for _, tt := range []struct {
		name         string
		original     string
		syncedPrefix string
	}{
		{name: "with-terminal-newline", original: "keep one\n\nkeep two\n", syncedPrefix: "keep one\n\nkeep two\n# >>> frigo >>>\n"},
		{name: "without-terminal-newline", original: "keep without terminal newline", syncedPrefix: "# >>> frigo >>>\n/PLAN.md\n# <<< frigo <<<\nkeep without terminal newline"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := testrepo.Init(t)
			repo := discoverRepository(t, root)
			if err := os.WriteFile(repo.ExcludePath, []byte(tt.original), 0o644); err != nil {
				t.Fatal(err)
			}
			owned := registry.Registry{Version: registry.CurrentVersion, Paths: []string{"PLAN.md"}}
			for i := 0; i < 2; i++ {
				if err := Sync(repo, owned); err != nil {
					t.Fatal(err)
				}
				contents, err := os.ReadFile(repo.ExcludePath)
				if err != nil {
					t.Fatal(err)
				}
				if got := string(contents); !strings.HasPrefix(got, tt.syncedPrefix) {
					t.Fatalf("synced exclude = %q, want prefix %q", got, tt.syncedPrefix)
				}
			}
			for i := 0; i < 2; i++ {
				if err := Sync(repo, registry.New()); err != nil {
					t.Fatal(err)
				}
			}
			contents, err := os.ReadFile(repo.ExcludePath)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(contents); got != tt.original {
				t.Fatalf("exclude after repeated Sync = %q, want exact original %q", got, tt.original)
			}
		})
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

func discoverRepository(t *testing.T, root string) repository.Repository {
	t.Helper()
	repo, err := repository.Discover(context.Background(), git.Client{Path: "git"}, root)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}
