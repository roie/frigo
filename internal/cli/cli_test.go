package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	gitpkg "github.com/roie/frigo/internal/git"
	"github.com/roie/frigo/internal/testrepo"
)

const wantUsage = "Usage: frigo <command> [options]\nCommands: add, release, status, list, diff, commit, log, restore, doctor, help\nRun 'frigo help' for detailed help.\n"

const wantHelp = `frigo keeps local project files without adding them to your main Git history.

Usage:
  frigo add [--] <path>...
  frigo release [--all] [--force] [--] <path>...
  frigo status
  frigo list | frigo ls
  frigo diff [--] [<path>...]
  frigo commit -m <message> [--] <path>...
  frigo commit -a -m <message>
  frigo commit -am <message>
  frigo log
  frigo restore [--] <path>...
  frigo doctor [--repair]

Commands:
  add      Assign existing untracked paths to frigo.
  release  Release exact ownership in the current worktree without deleting files or history; other Git ignore rules may still hide it, or every owned root with --all.
  status   Show main-repository and frigo working-tree status.
  list     List exact ownership roots; ls is an alias.
  diff     Show owned changes against frigo HEAD.
  commit   Commit selected paths, or every owned change with -a.
  log      Show frigo commit history.
  restore  Restore saved owned paths from frigo HEAD.
  doctor   Diagnose metadata, or apply bounded repairs with --repair.

Notes:
  doctor --repair prints a complete repair plan before mutation.
  release --all applies only to the current worktree.

Use -- before paths beginning with '-'. frigo has no persistent staging area.
`

var cwdMu sync.Mutex

type result struct {
	stdout string
	stderr string
	code   int
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("injected output failure")
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

func TestVersionDoesNotRequireRepository(t *testing.T) {
	got := invoke(t, t.TempDir(), "--version")
	if got.code != 0 || got.stderr != "" || got.stdout != "frigo dev\n" {
		t.Fatalf("result=%+v, want stdout %q", got, "frigo dev\n")
	}
}

func TestSelectVersion(t *testing.T) {
	tests := []struct {
		name          string
		linkerVersion string
		moduleVersion string
		want          string
	}{
		{name: "linker release wins", linkerVersion: "0.3.0", moduleVersion: "v0.2.0", want: "0.3.0"},
		{name: "module release", linkerVersion: "dev", moduleVersion: "v0.2.0", want: "0.2.0"},
		{name: "module prerelease", linkerVersion: "dev", moduleVersion: "v0.3.0-rc.1", want: "0.3.0-rc.1"},
		{name: "development build", linkerVersion: "dev", moduleVersion: "(devel)", want: "dev"},
		{name: "missing build info", linkerVersion: "dev", moduleVersion: "", want: "dev"},
		{name: "non-module string", linkerVersion: "dev", moduleVersion: "workspace", want: "dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selectVersion(tt.linkerVersion, tt.moduleVersion); got != tt.want {
				t.Fatalf("selectVersion(%q, %q) = %q, want %q", tt.linkerVersion, tt.moduleVersion, got, tt.want)
			}
		})
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

func TestDoctorCLIUsesDeterministicExitCodesAndPrintsPlanBeforeApply(t *testing.T) {
	root := testrepo.Init(t)
	testrepo.Write(t, root, "README.md", "main\n")
	testrepo.CommitAll(t, root, "initial", "README.md")
	testrepo.Write(t, root, "private.txt", "private\n")
	if added := invoke(t, root, "add", "private.txt"); added.code != 0 {
		t.Fatalf("add result = %+v", added)
	}

	healthy := invoke(t, root, "doctor")
	if healthy.code != 0 || healthy.stderr != "" || healthy.stdout != "ok\n" {
		t.Fatalf("healthy doctor result = %+v", healthy)
	}

	attributes := filepath.Join(root, ".git", "frigo", "history.git", "info", "attributes")
	if err := os.Remove(attributes); err != nil {
		t.Fatal(err)
	}
	unhealthy := invoke(t, root, "doctor")
	if unhealthy.code != 1 || unhealthy.stderr != "" || !strings.Contains(unhealthy.stdout, "issue attributes-private ") {
		t.Fatalf("unhealthy doctor result = %+v", unhealthy)
	}

	repaired := invoke(t, root, "doctor", "--repair")
	if repaired.code != 0 || repaired.stderr != "" {
		t.Fatalf("repair doctor result = %+v", repaired)
	}
	planAt := strings.Index(repaired.stdout, "plan attributes-private ")
	appliedAt := strings.Index(repaired.stdout, "applied attributes-private ")
	if planAt < 0 || appliedAt < 0 || planAt >= appliedAt || !strings.HasSuffix(repaired.stdout, "ok\n") {
		t.Fatalf("repair output does not print plan before apply:\n%s", repaired.stdout)
	}

	usage := invoke(t, root, "doctor", "unexpected")
	if usage.code != 2 || !strings.Contains(usage.stderr, "Usage: frigo doctor [--repair]") {
		t.Fatalf("doctor usage result = %+v", usage)
	}
}

func TestDoctorRepairDoesNotMutateWhenCompletePlanCannotBePrinted(t *testing.T) {
	root := testrepo.Init(t)
	testrepo.Write(t, root, "README.md", "main\n")
	testrepo.CommitAll(t, root, "initial", "README.md")
	testrepo.Write(t, root, "private.txt", "private\n")
	if added := invoke(t, root, "add", "private.txt"); added.code != 0 {
		t.Fatalf("add result = %+v", added)
	}
	attributes := filepath.Join(root, ".git", "frigo", "history.git", "info", "attributes")
	if err := os.Remove(attributes); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	code := runAt(context.Background(), []string{"doctor", "--repair"}, bytes.NewReader(nil), failingWriter{}, &stderr, root, gitpkg.Client{Path: "git"})
	if code != 1 || !strings.Contains(stderr.String(), "injected output failure") {
		t.Fatalf("doctor result code=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Lstat(attributes); !os.IsNotExist(err) {
		t.Fatalf("attributes mutated after plan output failure, err=%v", err)
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

func TestReleaseAllParser(t *testing.T) {
	t.Run("all", func(t *testing.T) {
		got, usageErr := parseArgs([]string{"release", "--all"})
		if usageErr != nil {
			t.Fatalf("parseArgs() usage error = %v", usageErr)
		}
		if got.name != "release" || got.message != "" || !got.all || got.force || len(got.paths) != 0 {
			t.Fatalf("parseArgs() = %+v, want release --all", got)
		}
	})

	t.Run("all with force", func(t *testing.T) {
		got, usageErr := parseArgs([]string{"release", "--all", "--force"})
		if usageErr != nil {
			t.Fatalf("parseArgs() usage error = %v", usageErr)
		}
		if got.name != "release" || got.message != "" || !got.all || !got.force || len(got.paths) != 0 {
			t.Fatalf("parseArgs() = %+v, want release --all --force", got)
		}
	})

	t.Run("single-dash all and force aliases", func(t *testing.T) {
		got, usageErr := parseArgs([]string{"release", "-all", "-force"})
		if usageErr != nil {
			t.Fatalf("parseArgs() usage error = %v", usageErr)
		}
		if got.name != "release" || !got.all || !got.force || len(got.paths) != 0 {
			t.Fatalf("parseArgs() = %+v, want release -all -force", got)
		}
	})

	for _, forceFlag := range []string{"-force", "--force"} {
		t.Run("path with "+forceFlag, func(t *testing.T) {
			got, usageErr := parseArgs([]string{"release", forceFlag, "PLAN.md"})
			if usageErr != nil {
				t.Fatalf("parseArgs() usage error = %v", usageErr)
			}
			if got.name != "release" || got.all || !got.force || !slices.Equal(got.paths, []string{"PLAN.md"}) {
				t.Fatalf("parseArgs() = %+v, want forced path release", got)
			}
		})
	}

	t.Run("all rejects paths", func(t *testing.T) {
		_, usageErr := parseArgs([]string{"release", "--all", "PLAN.md"})
		if usageErr == nil || !strings.Contains(usageErr.message, "release --all does not accept paths") {
			t.Fatalf("parseArgs() usage error = %v", usageErr)
		}
	})
}

func TestReleaseAllCommandReleasesEveryOwnedRoot(t *testing.T) {
	root := testrepo.Init(t)
	testrepo.Write(t, root, "README.md", "main\n")
	testrepo.CommitAll(t, root, "initial", "README.md")
	for _, path := range []string{"PLAN.md", "NOTES.md"} {
		testrepo.Write(t, root, path, path+"\n")
	}
	if got := invoke(t, root, "add", "PLAN.md", "NOTES.md"); got.code != 0 {
		t.Fatalf("add: %+v", got)
	}
	if got := invoke(t, root, "commit", "-a", "-m", "checkpoint"); got.code != 0 {
		t.Fatalf("commit: %+v", got)
	}

	got := invoke(t, root, "release", "--all")
	if got.code != 0 || got.stderr != "" {
		t.Fatalf("release --all: %+v", got)
	}
	if !strings.Contains(got.stdout, "released NOTES.md") || !strings.Contains(got.stdout, "released PLAN.md") {
		t.Fatalf("release --all stdout:\n%s", got.stdout)
	}

	got = invoke(t, root, "list")
	if got.code != 0 || got.stderr != "" || got.stdout != "" {
		t.Fatalf("list after release --all: %+v", got)
	}
	contents := testrepo.Read(t, root, ".git/info/exclude")
	if strings.Contains(contents, "/PLAN.md") || strings.Contains(contents, "/NOTES.md") {
		t.Fatalf("exclude file still contains released paths: %q", contents)
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
