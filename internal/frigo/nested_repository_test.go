package frigo

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/roie/frigo/internal/testrepo"
)

func TestCommitRejectsNestedRepositoryInOwnedDirectory(t *testing.T) {
	ctx := t.Context()
	ws, root := newWorkspace(t)
	testrepo.Write(t, root, "notes/plan.md", "before\n")
	if _, err := ws.Add(ctx, []string{"notes"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Commit(ctx, CommitOptions{All: true, Message: "initial snapshot"}); err != nil {
		t.Fatal(err)
	}
	base, err := ws.resolveHistoryBase(ctx)
	if err != nil {
		t.Fatal(err)
	}

	testrepo.Write(t, root, "notes/imported/details.md", "nested notes\n")
	nested := filepath.Join(root, "notes", "imported")
	testrepo.Run(t, nested, "init", "-q")
	testrepo.Run(t, nested, "config", "user.name", "Frigo Test")
	testrepo.Run(t, nested, "config", "user.email", "frigo@example.invalid")
	testrepo.CommitAll(t, nested, "nested snapshot", "details.md")
	testrepo.Write(t, root, "notes/plan.md", "after\n")

	result, err := ws.Commit(ctx, CommitOptions{All: true, Message: "must not snapshot"})
	if err == nil || !strings.Contains(err.Error(), "nested Git repository") || !strings.Contains(err.Error(), "notes/imported") || result.Committed {
		t.Fatalf("Commit() = %#v, %v; want nested repository rejection", result, err)
	}
	assertHistoryHead(t, ws, base.OID)
}
