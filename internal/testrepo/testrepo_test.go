package testrepo

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestInitWriteReadAndCommitAll(t *testing.T) {
	root := Init(t)
	Write(t, root, "PLAN.md", "draft\n")
	Write(t, root, "NOTES.md", "ignore me\n")
	if got := Read(t, root, "PLAN.md"); got != "draft\n" {
		t.Fatalf("Read() = %q, want %q", got, "draft\n")
	}

	CommitAll(t, root, "checkpoint", "PLAN.md")
	if got := Output(t, root, "log", "--oneline"); !strings.Contains(got, "checkpoint") {
		t.Fatalf("log = %q, want checkpoint commit", got)
	}
	if got := Output(t, root, "show", "--pretty=", "--name-only", "HEAD"); got != "PLAN.md" {
		t.Fatalf("HEAD contents = %q, want PLAN.md only", got)
	}
	if got := Output(t, root, "ls-files", "--others", "--exclude-standard"); got != "NOTES.md" {
		t.Fatalf("untracked files = %q, want NOTES.md", got)
	}
}

func TestCommitAllTreatsMagicLookingPathAsLiteral(t *testing.T) {
	root := Init(t)
	Write(t, root, ":(glob)*", "literal\n")
	Write(t, root, "notes.md", "keep me\n")

	CommitAll(t, root, "literal-pathspec", ":(glob)*")

	if got := Output(t, root, "show", "--pretty=", "--name-only", "HEAD"); got != ":(glob)*" {
		t.Fatalf("HEAD contents = %q, want only literal magic-looking filename", got)
	}
	if got := Output(t, root, "ls-files", "--others", "--exclude-standard"); got != "notes.md" {
		t.Fatalf("untracked files = %q, want notes.md", got)
	}
}

func TestResolvePathRejectsAbsoluteAndTraversal(t *testing.T) {
	root := t.TempDir()
	absolute := filepath.Join(t.TempDir(), "escape.md")
	for _, rel := range []string{absolute, "../escape.md", "nested/../escape.md"} {
		t.Run(rel, func(t *testing.T) {
			if _, err := resolveFixturePath(root, rel); err == nil {
				t.Fatalf("resolveFixturePath(%q) error = nil, want rejection", rel)
			}
		})
	}
}

func TestResolvePathStaysUnderRoot(t *testing.T) {
	root := t.TempDir()
	got, err := resolveFixturePath(root, "nested/PLAN.md")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, got)
	if err != nil {
		t.Fatal(err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("resolved path %q escaped root %q", got, root)
	}
}

func TestGitOutputIncludesStderrOnFailure(t *testing.T) {
	root := Init(t)
	output, err := gitOutput(root, "rev-parse", "--verify", "missing")
	if err == nil {
		t.Fatal("gitOutput() error = nil, want failure")
	}
	if !strings.Contains(output, "fatal:") {
		t.Fatalf("gitOutput() output = %q, want stderr preserved", output)
	}
}

func TestOutputTrimsTrailingNewline(t *testing.T) {
	root := Init(t)
	if got := Output(t, root, "rev-parse", "--show-toplevel"); got != root {
		t.Fatalf("Output() = %q, want %q", got, root)
	}
}
