package registry

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLoadRejectsTrailingJSON(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "registry.json")
	err := os.WriteFile(filename, []byte(`{"version":1,"paths":[]} garbage`), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	_, err = Load(filename)
	if err == nil || !strings.Contains(err.Error(), "trailing data") {
		t.Fatalf("Load() error = %v, want trailing data error", err)
	}
}

func TestAddParentReplacesOwnedChildren(t *testing.T) {
	owned := Registry{Version: CurrentVersion, Paths: []string{"docs/local/a", "docs/local/b"}}
	result, err := owned.Add("docs/local")
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	if !slices.Equal(owned.Paths, []string{"docs/local"}) {
		t.Fatalf("paths = %v", owned.Paths)
	}
	if !slices.Equal(result.ReleasedCovered, []string{"docs/local/a", "docs/local/b"}) {
		t.Fatalf("released covered = %v", result.ReleasedCovered)
	}
}

func TestReleaseRemovesExactPathsAndReportsMissing(t *testing.T) {
	owned := Registry{Version: CurrentVersion, Paths: []string{"docs/local", "PLAN.md"}}
	result, err := owned.Release("PLAN.md", "missing.md")
	if err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	if !slices.Equal(owned.Paths, []string{"docs/local"}) {
		t.Fatalf("paths = %v", owned.Paths)
	}
	if !slices.Equal(result.Released, []string{"PLAN.md"}) {
		t.Fatalf("released = %v", result.Released)
	}
	if !slices.Equal(result.Missing, []string{"missing.md"}) {
		t.Fatalf("missing = %v", result.Missing)
	}
}

func TestOwnsAndOwnsExact(t *testing.T) {
	t.Parallel()

	owned := Registry{Version: CurrentVersion, Paths: []string{"docs", "PLAN.md"}}
	if !owned.Owns("docs/local/a.md") {
		t.Fatal("Owns() = false, want true")
	}
	if owned.OwnsExact("docs/local/a.md") {
		t.Fatal("OwnsExact() = true, want false")
	}
	if !owned.OwnsExact("PLAN.md") {
		t.Fatal("OwnsExact() = false, want true")
	}
}

func TestAddRejectsInvalidMutationInputsAndLeavesStateUnchanged(t *testing.T) {
	base := Registry{Version: CurrentVersion, Paths: []string{"docs", "PLAN.md"}}
	for _, candidate := range []string{"/abs/path", "../escape.md", "nested/../escape.md"} {
		t.Run(candidate, func(t *testing.T) {
			owned := base
			result, err := owned.Add(candidate)
			if err == nil {
				t.Fatalf("Add(%q) error = nil, want rejection", candidate)
			}
			if !slices.Equal(owned.Paths, base.Paths) || owned.Version != base.Version {
				t.Fatalf("registry mutated on error: %#v", owned)
			}
			if len(result.Added) != 0 || len(result.ReleasedCovered) != 0 || len(result.AlreadyOwned) != 0 {
				t.Fatalf("Add(%q) result = %#v, want zero value", candidate, result)
			}
		})
	}
}

func TestReleaseRejectsInvalidMutationInputsAndLeavesStateUnchanged(t *testing.T) {
	base := Registry{Version: CurrentVersion, Paths: []string{"docs", "PLAN.md"}}
	for _, candidate := range []string{"/abs/path", "../escape.md", "nested/../escape.md"} {
		t.Run(candidate, func(t *testing.T) {
			owned := base
			result, err := owned.Release(candidate)
			if err == nil {
				t.Fatalf("Release(%q) error = nil, want rejection", candidate)
			}
			if !slices.Equal(owned.Paths, base.Paths) || owned.Version != base.Version {
				t.Fatalf("registry mutated on error: %#v", owned)
			}
			if len(result.Released) != 0 || len(result.Missing) != 0 {
				t.Fatalf("Release(%q) result = %#v, want zero value", candidate, result)
			}
		})
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	t.Parallel()

	filename := filepath.Join(t.TempDir(), "state", "registry.json")
	reg := Registry{Version: CurrentVersion, Paths: []string{"z.md", "a.md", "a.md"}}
	if err := Save(filename, reg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := Load(filename)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !slices.Equal(loaded.Paths, []string{"a.md", "z.md"}) {
		t.Fatalf("loaded Paths = %#v", loaded.Paths)
	}
}
