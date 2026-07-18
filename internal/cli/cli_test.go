package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"frigo/internal/testrepo"
)

const wantUsage = "Usage:\n  frigo\n  frigo add [--] <path>...\n  frigo release [--force] [--] <path>...\n  frigo status\n  frigo list\n  frigo ls\n  frigo diff [--] [<path>...]\n  frigo commit -m <message> [--] <path>...\n  frigo commit -a -m <message>\n  frigo commit -am <message>\n  frigo log\n  frigo restore [--] <path>...\n  frigo help\n"

var cwdMu sync.Mutex

type result struct {
	stdout string
	stderr string
	code   int
}

func TestBareUsageAndHelpDoNotRequireRepository(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"help"},
		{"--help"},
	} {
		got := invoke(t, t.TempDir(), args...)
		if got.code != 0 || got.stderr != "" {
			t.Fatalf("args=%v result=%+v", args, got)
		}
		if got.stdout != wantUsage {
			t.Fatalf("args=%v stdout:\n%q\nwant:\n%q", args, got.stdout, wantUsage)
		}
	}
}

func TestHelpWithExtraArgsReturnsUsageError(t *testing.T) {
	for _, args := range [][]string{{"help", "extra"}, {"--help", "extra"}} {
		got := invoke(t, t.TempDir(), args...)
		if got.code != 2 {
			t.Fatalf("args=%v result=%+v", args, got)
		}
		if !strings.Contains(got.stderr, "does not accept arguments") {
			t.Fatalf("args=%v stderr=%q", args, got.stderr)
		}
		if !strings.HasPrefix(got.stderr, "frigo:") {
			t.Fatalf("args=%v stderr=%q", args, got.stderr)
		}
	}
}

func TestDashHIsUnsupported(t *testing.T) {
	got := invoke(t, t.TempDir(), "-h")
	if got.code != 2 {
		t.Fatalf("result=%+v", got)
	}
	if !strings.Contains(got.stderr, `frigo: unknown command "-h"`) {
		t.Fatalf("stderr=%q", got.stderr)
	}
}

func TestUnknownCommandReturnsUsageError(t *testing.T) {
	got := invoke(t, t.TempDir(), "wat")
	if got.code != 2 {
		t.Fatalf("result=%+v", got)
	}
	if !strings.Contains(got.stderr, `frigo: unknown command "wat"`) {
		t.Fatalf("stderr=%q", got.stderr)
	}
	if strings.Count(got.stderr, "frigo:") != 1 {
		t.Fatalf("stderr has repeated prefix: %q", got.stderr)
	}
}

func TestRepositoryCommandOutsideGitFailsClearly(t *testing.T) {
	got := invoke(t, t.TempDir(), "status")
	if got.code != 1 {
		t.Fatalf("result=%+v", got)
	}
	if !strings.Contains(got.stderr, "frigo: not inside a Git worktree") {
		t.Fatalf("stderr=%q", got.stderr)
	}
}

func TestReservedCommandNameCanBeOwned(t *testing.T) {
	root := initRepo(t)
	testrepo.Write(t, root, "log", "local log\n")

	got := invoke(t, root, "add", "log")
	if got.code != 0 {
		t.Fatalf("add: %+v", got)
	}

	got = invoke(t, root, "list")
	if got.stdout != "log\n" {
		t.Fatalf("list=%q", got.stdout)
	}
}

func TestPathTakingCommandsAcceptDoubleDash(t *testing.T) {
	root := initRepo(t)
	testrepo.Write(t, root, "-draft.md", "draft\n")

	got := invoke(t, root, "add", "--", "-draft.md")
	if got.code != 0 {
		t.Fatalf("add: %+v", got)
	}
	got = invoke(t, root, "list")
	if got.stdout != "-draft.md\n" {
		t.Fatalf("list=%q", got.stdout)
	}
}

func TestPathlessCommitSuggestsAll(t *testing.T) {
	got := invoke(t, initRepo(t), "commit", "-m", "checkpoint")
	if got.code != 2 || !strings.Contains(got.stderr, "use -a") {
		t.Fatalf("result=%+v", got)
	}
	if strings.Count(got.stderr, "frigo:") != 1 {
		t.Fatalf("stderr has repeated prefix: %q", got.stderr)
	}
}

func TestCombinedAndSeparateAllFlags(t *testing.T) {
	var trees []string
	for _, args := range [][]string{
		{"commit", "-am", "checkpoint"},
		{"commit", "-a", "-m", "checkpoint"},
	} {
		root := initRepo(t)
		testrepo.Write(t, root, "PLAN.md", "plan\n")
		if got := invoke(t, root, "add", "PLAN.md"); got.code != 0 {
			t.Fatal(got.stderr)
		}
		if got := invoke(t, root, args...); got.code != 0 {
			t.Fatal(got.stderr)
		}
		trees = append(trees, privateTree(t, root))
	}
	if trees[0] != trees[1] {
		t.Fatalf("trees differ: %q != %q", trees[0], trees[1])
	}
}

func TestStatusDiffCommitLogRestoreReleaseAndLs(t *testing.T) {
	root := initRepo(t)
	testrepo.Write(t, root, "PLAN.md", "plan v1\n")
	testrepo.Write(t, root, "NOTES.md", "notes v1\n")
	if got := invoke(t, root, "add", "PLAN.md", "NOTES.md"); got.code != 0 {
		t.Fatalf("add: %+v", got)
	}

	got := invoke(t, root, "status")
	if got.code != 0 || got.stderr != "" {
		t.Fatalf("status: %+v", got)
	}
	if !strings.Contains(got.stdout, "main\n  clean\nfrigo\n") ||
		!strings.Contains(got.stdout, "PLAN.md") ||
		!strings.Contains(got.stdout, "NOTES.md") {
		t.Fatalf("status stdout:\n%s", got.stdout)
	}

	got = invoke(t, root, "diff", "PLAN.md")
	if got.code != 0 || got.stderr != "" {
		t.Fatalf("diff: %+v", got)
	}
	if !strings.Contains(got.stdout, "+plan v1") || strings.Contains(got.stdout, "notes v1") {
		t.Fatalf("diff stdout:\n%s", got.stdout)
	}

	got = invoke(t, root, "commit", "-m", "checkpoint", "PLAN.md")
	if got.code != 0 || got.stderr != "" || !strings.Contains(got.stdout, "committed ") {
		t.Fatalf("commit: %+v", got)
	}

	got = invoke(t, root, "log")
	if got.code != 0 || got.stderr != "" || !strings.Contains(got.stdout, "checkpoint") {
		t.Fatalf("log: %+v", got)
	}

	testrepo.Write(t, root, "PLAN.md", "plan v2\n")
	got = invoke(t, root, "restore", "PLAN.md")
	if got.code != 0 || got.stderr != "" || !strings.Contains(got.stdout, "restored PLAN.md") {
		t.Fatalf("restore: %+v", got)
	}
	if got := testrepo.Read(t, root, "PLAN.md"); got != "plan v1\n" {
		t.Fatalf("PLAN.md=%q", got)
	}

	got = invoke(t, root, "release", "--force", "NOTES.md")
	if got.code != 0 || got.stderr != "" || !strings.Contains(got.stdout, "released NOTES.md") {
		t.Fatalf("release: %+v", got)
	}

	got = invoke(t, root, "ls")
	if got.code != 0 || got.stderr != "" || got.stdout != "PLAN.md\n" {
		t.Fatalf("ls: %+v", got)
	}

	got = invoke(t, root, "commit", "-a", "-m", "noop")
	if got.code != 0 || got.stderr != "" || got.stdout != "nothing to commit\n" {
		t.Fatalf("noop commit: %+v", got)
	}
}

func invoke(t *testing.T, root string, args ...string) result {
	t.Helper()
	cwdMu.Lock()
	defer cwdMu.Unlock()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(cwd); err != nil {
			panic(err)
		}
	}()

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
	return result{
		stdout: stdout.String(),
		stderr: stderr.String(),
		code:   code,
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	root := testrepo.Init(t)
	testrepo.Write(t, root, "README.md", "main\n")
	testrepo.CommitAll(t, root, "initial", "README.md")
	return root
}

func privateTree(t *testing.T, root string) string {
	t.Helper()
	return testrepo.Output(t, root, "--git-dir=.git/frigo/history.git", "--work-tree=.", "rev-parse", "HEAD^{tree}")
}
