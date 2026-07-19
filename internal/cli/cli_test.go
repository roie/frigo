package cli

import (
	"bytes"
	"context"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"frigo/internal/testrepo"
)

const wantUsage = "Usage: frigo <command> [options]\nCommands: add, release, status, list, diff, commit, log, restore, help\nRun 'frigo help' for detailed help.\n"

const wantHelp = `frigo keeps selected paths in a separate local Git history.

Usage:
  frigo add [--] <path>...
  frigo release [--force] [--] <path>...
  frigo status
  frigo list | frigo ls
  frigo diff [--] [<path>...]
  frigo commit -m <message> [--] <path>...
  frigo commit -a -m <message>
  frigo commit -am <message>
  frigo log
  frigo restore [--] <path>...

Commands:
  add      Assign existing untracked paths to frigo.
  release  Release exact ownership without deleting files or history.
  status   Show main-repository and frigo working-tree status.
  list     List exact ownership roots; ls is an alias.
  diff     Show owned changes against frigo HEAD.
  commit   Commit selected paths, or every owned change with -a.
  log      Show frigo commit history.
  restore  Restore saved owned paths from frigo HEAD.

Use -- before paths beginning with '-'. frigo has no persistent staging area.
`

var cwdMu sync.Mutex

type result struct {
	stdout string
	stderr string
	code   int
}

func TestBareUsageAndDetailedHelpDoNotRequireRepository(t *testing.T) {
	bare := invoke(t, t.TempDir())
	if bare.code != 0 || bare.stderr != "" || bare.stdout != wantUsage {
		t.Fatalf("bare result=%+v, want stdout %q", bare, wantUsage)
	}
	for _, args := range [][]string{{"help"}, {"--help"}} {
		got := invoke(t, t.TempDir(), args...)
		if got.code != 0 || got.stderr != "" {
			t.Fatalf("args=%v result=%+v", args, got)
		}
		if got.stdout != wantHelp {
			t.Fatalf("args=%v stdout:\n%q\nwant:\n%q", args, got.stdout, wantHelp)
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
	root := testrepo.Init(t)
	testrepo.Write(t, root, "README.md", "main\n")
	testrepo.CommitAll(t, root, "initial", "README.md")
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
	root := testrepo.Init(t)
	testrepo.Write(t, root, "README.md", "main\n")
	testrepo.CommitAll(t, root, "initial", "README.md")
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
	root := testrepo.Init(t)
	testrepo.Write(t, root, "README.md", "main\n")
	testrepo.CommitAll(t, root, "initial", "README.md")
	got := invoke(t, root, "commit", "-m", "checkpoint")
	if got.code != 2 || !strings.Contains(got.stderr, "use -a") {
		t.Fatalf("result=%+v", got)
	}
	if strings.Count(got.stderr, "frigo:") != 1 {
		t.Fatalf("stderr has repeated prefix: %q", got.stderr)
	}
}

func TestCommitCombinedFlagExpansionRespectsMessageAndPathValues(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want parsedCommand
	}{
		{
			name: "message equal to combined flag",
			args: []string{"commit", "-m", "-am", "PLAN.md"},
			want: parsedCommand{name: "commit", message: "-am", paths: []string{"PLAN.md"}},
		},
		{
			name: "path equal to combined flag after separator",
			args: []string{"commit", "-m", "checkpoint", "--", "-am"},
			want: parsedCommand{name: "commit", message: "checkpoint", paths: []string{"-am"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, usageErr := parseArgs(tt.args)
			if usageErr != nil {
				t.Fatalf("parseArgs() usage error = %v", usageErr)
			}
			if got.name != tt.want.name || got.message != tt.want.message || got.all != tt.want.all || !slices.Equal(got.paths, tt.want.paths) {
				t.Fatalf("parseArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCLICommitAcceptsCombinedFlagAsMessageAndPathValue(t *testing.T) {
	t.Run("message", func(t *testing.T) {
		root := testrepo.Init(t)
		testrepo.Write(t, root, "README.md", "main\n")
		testrepo.CommitAll(t, root, "initial", "README.md")
		testrepo.Write(t, root, "PLAN.md", "plan\n")
		if got := invoke(t, root, "add", "PLAN.md"); got.code != 0 {
			t.Fatal(got.stderr)
		}
		if got := invoke(t, root, "commit", "-m", "-am", "PLAN.md"); got.code != 0 {
			t.Fatalf("commit: %+v", got)
		}
	})

	t.Run("path after separator", func(t *testing.T) {
		root := testrepo.Init(t)
		testrepo.Write(t, root, "README.md", "main\n")
		testrepo.CommitAll(t, root, "initial", "README.md")
		testrepo.Write(t, root, "-am", "plan\n")
		if got := invoke(t, root, "add", "--", "-am"); got.code != 0 {
			t.Fatal(got.stderr)
		}
		if got := invoke(t, root, "commit", "-m", "checkpoint", "--", "-am"); got.code != 0 {
			t.Fatalf("commit: %+v", got)
		}
		if got := privateTree(t, root); got == "" {
			t.Fatal("private tree is empty")
		}
	})
}

func TestAddPrintsNormalizedAlreadyOwnedPath(t *testing.T) {
	root := testrepo.Init(t)
	testrepo.Write(t, root, "README.md", "main\n")
	testrepo.CommitAll(t, root, "initial", "README.md")
	testrepo.Write(t, root, "PLAN.md", "plan\n")
	if got := invoke(t, root, "add", "PLAN.md"); got.code != 0 {
		t.Fatal(got.stderr)
	}

	got := invoke(t, root, "add", "./PLAN.md")
	if got.code != 0 || got.stderr != "" || got.stdout != "already owned PLAN.md\n" {
		t.Fatalf("second add: %+v", got)
	}
}

func TestCombinedAndSeparateAllFlags(t *testing.T) {
	var trees []string
	for _, args := range [][]string{
		{"commit", "-am", "checkpoint"},
		{"commit", "-a", "-m", "checkpoint"},
	} {
		root := testrepo.Init(t)
		testrepo.Write(t, root, "README.md", "main\n")
		testrepo.CommitAll(t, root, "initial", "README.md")
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
	root := testrepo.Init(t)
	testrepo.Write(t, root, "README.md", "main\n")
	testrepo.CommitAll(t, root, "initial", "README.md")
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
			t.Fatalf("restore working directory: %v", err)
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

func privateTree(t *testing.T, root string) string {
	t.Helper()
	return testrepo.Output(t, root, "--git-dir=.git/frigo/history.git", "--work-tree=.", "rev-parse", "HEAD^{tree}")
}
