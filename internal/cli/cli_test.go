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

const wantUsage = "Usage: frigo <command> [options]\nCommands: add, release, status, list, diff, commit, log, show, restore, doctor, help\nRun 'frigo help' for detailed help.\n"

const wantHelp = `frigo keeps local project files without adding them to your main Git history.

Usage:
  frigo add [--] <path>...
  frigo release [--all] [--force] [--] <path>...
  frigo status
  frigo status --porcelain=v1 -z [--] [<path>...]
  frigo list | frigo ls
  frigo list -z | frigo ls -z
  frigo diff [--] [<path>...]
  frigo diff --patch [--] [<path>...]
  frigo commit -m <message> [--] <path>...
  frigo commit -a -m <message>
  frigo commit -am <message>
  frigo log
  frigo log --porcelain=v1 -z [--max-count=<n>] [--skip=<n>] [<revision>]
  frigo show [<revision>] [-- <path>...]
  frigo show --name-status -z <revision> [-- <path>...]
  frigo show --patch <revision> [-- <path>...]
  frigo show <revision>:<path>
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
  show     Show one frigo commit and its patch.
  restore  Restore saved owned paths from frigo HEAD.
  doctor   Diagnose metadata, or apply bounded repairs with --repair.

Notes:
  doctor --repair prints a complete repair plan before mutation.
  release --all applies only to the current worktree.
  Porcelain and -z forms write machine-readable bytes without headings.

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

func TestMachineCommandParsers(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want parsedCommand
	}{
		{
			name: "status porcelain",
			args: []string{"status", "--porcelain=v1", "-z"},
			want: parsedCommand{name: "status", output: outputPorcelainV1},
		},
		{
			name: "status porcelain paths",
			args: []string{"status", "--porcelain=v1", "-z", "--", "PLAN.md", "-draft.md"},
			want: parsedCommand{name: "status", output: outputPorcelainV1, paths: []string{"PLAN.md", "-draft.md"}},
		},
		{
			name: "list NUL",
			args: []string{"list", "-z"},
			want: parsedCommand{name: "list", output: outputNUL},
		},
		{
			name: "ls NUL",
			args: []string{"ls", "-z"},
			want: parsedCommand{name: "ls", output: outputNUL},
		},
		{
			name: "diff patch",
			args: []string{"diff", "--patch", "--", "PLAN.md"},
			want: parsedCommand{name: "diff", output: outputPatch, paths: []string{"PLAN.md"}},
		},
		{
			name: "log porcelain selection",
			args: []string{"log", "--porcelain=v1", "-z", "--max-count=0", "--skip=2", "HEAD~1"},
			want: parsedCommand{name: "log", output: outputPorcelainV1, revision: "HEAD~1", maxCount: new(0), skip: new(2)},
		},
		{
			name: "show name status",
			args: []string{"show", "--name-status", "-z", "HEAD", "--", "PLAN.md"},
			want: parsedCommand{name: "show", output: outputNameStatus, revision: "HEAD", paths: []string{"PLAN.md"}},
		},
		{
			name: "show patch",
			args: []string{"show", "--patch", "HEAD~1", "--", "PLAN.md"},
			want: parsedCommand{name: "show", output: outputPatch, revision: "HEAD~1", paths: []string{"PLAN.md"}},
		},
		{
			name: "show blob",
			args: []string{"show", "HEAD:PLAN.md"},
			want: parsedCommand{name: "show", output: outputBlob, revision: "HEAD", blobPath: "PLAN.md"},
		},
		{
			name: "show blob path colon",
			args: []string{"show", "HEAD:docs/a:b.md"},
			want: parsedCommand{name: "show", output: outputBlob, revision: "HEAD", blobPath: "docs/a:b.md"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, usageErr := parseArgs(tt.args)
			if usageErr != nil {
				t.Fatalf("parseArgs() usage error = %v", usageErr)
			}
			if !parsedCommandsEqual(got, tt.want) {
				t.Fatalf("parseArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestMachineCommandParserErrors(t *testing.T) {
	tests := [][]string{
		{"status", "--porcelain=v2", "-z"},
		{"status", "--porcelain=v1"},
		{"status", "-z"},
		{"status", "PLAN.md"},
		{"list", "PLAN.md"},
		{"list", "--unknown"},
		{"diff", "--unknown"},
		{"log", "--porcelain=v1"},
		{"log", "-z"},
		{"log", "--max-count=1"},
		{"log", "--porcelain=v1", "-z", "--max-count=-1"},
		{"log", "--porcelain=v1", "-z", "--skip=-1"},
		{"log", "--porcelain=v1", "-z", "--max-count=999999999999999999999999"},
		{"show", "--name-status", "HEAD"},
		{"show", "--name-status", "-z"},
		{"show", "--patch"},
		{"show", "--patch", "-z", "HEAD"},
		{"show", "--name-status", "--patch", "-z", "HEAD"},
		{"show", "HEAD:", "--", "PLAN.md"},
		{"show", "--patch", "HEAD:PLAN.md"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, usageErr := parseArgs(args)
			if usageErr == nil || usageErr.command != args[0] {
				t.Fatalf("parseArgs(%q) usage error = %v, want %s usage error", args, usageErr, args[0])
			}
		})
	}
}

func parsedCommandsEqual(got, want parsedCommand) bool {
	return got.name == want.name &&
		slices.Equal(got.paths, want.paths) &&
		got.revision == want.revision &&
		got.blobPath == want.blobPath &&
		got.message == want.message &&
		optionalIntsEqual(got.maxCount, want.maxCount) &&
		optionalIntsEqual(got.skip, want.skip) &&
		got.output == want.output &&
		got.all == want.all &&
		got.force == want.force &&
		got.repair == want.repair
}

func optionalIntsEqual(got, want *int) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return *got == *want
}

func TestShowParser(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want parsedCommand
	}{
		{name: "latest", args: []string{"show"}, want: parsedCommand{name: "show"}},
		{name: "revision", args: []string{"show", "HEAD~1"}, want: parsedCommand{name: "show", revision: "HEAD~1"}},
		{name: "latest path", args: []string{"show", "--", "PLAN.md"}, want: parsedCommand{name: "show", paths: []string{"PLAN.md"}}},
		{name: "revision paths", args: []string{"show", "abc1234", "--", "PLAN.md", "-draft.md"}, want: parsedCommand{name: "show", revision: "abc1234", paths: []string{"PLAN.md", "-draft.md"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, usageErr := parseArgs(tt.args)
			if usageErr != nil {
				t.Fatalf("parseArgs() usage error = %v", usageErr)
			}
			if got.name != tt.want.name || got.revision != tt.want.revision || !slices.Equal(got.paths, tt.want.paths) {
				t.Fatalf("parseArgs() = %+v, want %+v", got, tt.want)
			}
		})
	}

	for _, args := range [][]string{{"show", "HEAD", "PLAN.md"}, {"show", "-n1"}, {"show", ""}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, usageErr := parseArgs(args)
			if usageErr == nil || usageErr.command != "show" {
				t.Fatalf("parseArgs(%q) usage error = %v, want show usage error", args, usageErr)
			}
		})
	}
}

func TestShowUsageErrors(t *testing.T) {
	got := invoke(t, t.TempDir(), "show", "HEAD", "PLAN.md")
	if got.code != 2 || !strings.Contains(got.stderr, "  frigo show [<revision>] [-- <path>...]") {
		t.Fatalf("show usage result = %+v", got)
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

func TestCLIShowDisplaysLatestCommitAndPathFilter(t *testing.T) {
	root := testrepo.Init(t)
	testrepo.Write(t, root, "README.md", "main\n")
	testrepo.CommitAll(t, root, "initial", "README.md")
	testrepo.Write(t, root, "NOTES.md", "notes body\n")
	testrepo.Write(t, root, "PLAN.md", "plan body\n")
	if got := invoke(t, root, "add", "NOTES.md", "PLAN.md"); got.code != 0 {
		t.Fatalf("add: %+v", got)
	}
	if got := invoke(t, root, "commit", "-am", "show checkpoint"); got.code != 0 {
		t.Fatalf("commit: %+v", got)
	}

	latest := invoke(t, root, "show")
	if latest.code != 0 || latest.stderr != "" {
		t.Fatalf("show latest: %+v", latest)
	}
	for _, want := range []string{"show checkpoint", "NOTES.md", "PLAN.md"} {
		if !strings.Contains(latest.stdout, want) {
			t.Fatalf("show latest missing %q:\n%s", want, latest.stdout)
		}
	}

	filtered := invoke(t, root, "show", "HEAD", "--", "PLAN.md")
	if filtered.code != 0 || filtered.stderr != "" || !strings.Contains(filtered.stdout, "PLAN.md") {
		t.Fatalf("show filtered: %+v", filtered)
	}
	if strings.Contains(filtered.stdout, "NOTES.md") {
		t.Fatalf("show filtered unexpectedly contains NOTES.md:\n%s", filtered.stdout)
	}
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

func TestCLIMachineCurrentState(t *testing.T) {
	root := testrepo.Init(t)
	testrepo.Write(t, root, "README.md", "main\n")
	testrepo.CommitAll(t, root, "initial", "README.md")
	testrepo.Write(t, root, "z.md", "z\n")
	testrepo.Write(t, root, "a.md", "a\n")

	if got := invoke(t, root, "add", "z.md", "a.md"); got.code != 0 {
		t.Fatalf("add: %+v", got)
	}
	if got := invoke(t, root, "list", "-z"); got.code != 0 || got.stderr != "" || got.stdout != "a.md\x00z.md\x00" {
		t.Fatalf("list -z: %+v", got)
	}
	if got := invoke(t, root, "ls", "-z"); got.code != 0 || got.stderr != "" || got.stdout != "a.md\x00z.md\x00" {
		t.Fatalf("ls -z: %+v", got)
	}
	if got := invoke(t, root, "status", "--porcelain=v1", "-z"); got.code != 0 || got.stderr != "" || got.stdout != " A a.md\x00 A z.md\x00" {
		t.Fatalf("status porcelain: %+v", got)
	}
	if got := invoke(t, root, "status", "--porcelain=v1", "-z", "--", "z.md"); got.code != 0 || got.stderr != "" || got.stdout != " A z.md\x00" {
		t.Fatalf("filtered status porcelain: %+v", got)
	}
	if got := invoke(t, root, "status", "--porcelain=v1", "-z", "--", "README.md"); got.code != 1 || got.stdout != "" {
		t.Fatalf("failed status porcelain: %+v", got)
	}

	if got := invoke(t, root, "commit", "-a", "-m", "save files"); got.code != 0 {
		t.Fatalf("commit: %+v", got)
	}
	if got := invoke(t, root, "diff", "--patch"); got.code != 0 || got.stderr != "" || got.stdout != "" {
		t.Fatalf("clean diff --patch: %+v", got)
	}
	if got := invoke(t, root, "diff"); got.code != 0 || got.stdout != "no changes\n" {
		t.Fatalf("clean human diff: %+v", got)
	}

	testrepo.Write(t, root, "a.md", "changed\n")
	got := invoke(t, root, "diff", "--patch", "--", "a.md")
	if got.code != 0 || got.stderr != "" || !strings.Contains(got.stdout, "+changed") || !strings.HasSuffix(got.stdout, "\n") {
		t.Fatalf("changed diff --patch: %+v", got)
	}
}

func TestCLILogPorcelain(t *testing.T) {
	root := testrepo.Init(t)
	testrepo.Write(t, root, "README.md", "main\n")
	testrepo.CommitAll(t, root, "initial", "README.md")
	testrepo.Write(t, root, "PLAN.md", "first\n")
	if got := invoke(t, root, "add", "PLAN.md"); got.code != 0 {
		t.Fatalf("add: %+v", got)
	}
	if got := invoke(t, root, "commit", "-a", "-m", "first subject"); got.code != 0 {
		t.Fatalf("first commit: %+v", got)
	}
	testrepo.Write(t, root, "PLAN.md", "second\n")
	if got := invoke(t, root, "commit", "-a", "-m", "second subject"); got.code != 0 {
		t.Fatalf("second commit: %+v", got)
	}

	got := invoke(t, root, "log", "--porcelain=v1", "-z")
	if got.code != 0 || got.stderr != "" {
		t.Fatalf("porcelain log: %+v", got)
	}
	fields := bytes.Split([]byte(got.stdout), []byte{0})
	if len(fields) != 21 || string(fields[2]) != "second subject" || string(fields[12]) != "first subject" || len(fields[20]) != 0 {
		t.Fatalf("porcelain log fields = %#v", fields)
	}

	got = invoke(t, root, "log", "--porcelain=v1", "-z", "--max-count=1", "--skip=1")
	fields = bytes.Split([]byte(got.stdout), []byte{0})
	if got.code != 0 || got.stderr != "" || len(fields) != 11 || string(fields[2]) != "first subject" || len(fields[10]) != 0 {
		t.Fatalf("selected porcelain log: %+v fields=%#v", got, fields)
	}

	if got := invoke(t, root, "log", "--porcelain=v1", "-z", "HEAD..HEAD"); got.code != 1 || got.stdout != "" {
		t.Fatalf("range porcelain log: %+v", got)
	}
	if got := invoke(t, root, "log"); got.code != 0 || !strings.Contains(got.stdout, "second subject") {
		t.Fatalf("human log: %+v", got)
	}

	emptyRoot := testrepo.Init(t)
	testrepo.Write(t, emptyRoot, "README.md", "main\n")
	testrepo.CommitAll(t, emptyRoot, "initial", "README.md")
	testrepo.Write(t, emptyRoot, "PLAN.md", "draft\n")
	if got := invoke(t, emptyRoot, "add", "PLAN.md"); got.code != 0 {
		t.Fatalf("empty add: %+v", got)
	}
	if got := invoke(t, emptyRoot, "log", "--porcelain=v1", "-z"); got.code != 0 || got.stderr != "" || got.stdout != "" {
		t.Fatalf("empty porcelain log: %+v", got)
	}
}

func TestCLIShowMachineChanges(t *testing.T) {
	root := testrepo.Init(t)
	testrepo.Write(t, root, "README.md", "main\n")
	testrepo.CommitAll(t, root, "initial", "README.md")
	testrepo.Write(t, root, "docs/a.md", "first\n")
	testrepo.Write(t, root, "docs/z.md", "rename\n")
	if got := invoke(t, root, "add", "docs"); got.code != 0 {
		t.Fatalf("add: %+v", got)
	}
	if got := invoke(t, root, "commit", "-a", "-m", "root snapshot"); got.code != 0 {
		t.Fatalf("root commit: %+v", got)
	}

	rootLog := invoke(t, root, "log", "--porcelain=v1", "-z", "--max-count=1")
	rootFields := bytes.Split([]byte(rootLog.stdout), []byte{0})
	if rootLog.code != 0 || len(rootFields) != 11 {
		t.Fatalf("root log: %+v fields=%#v", rootLog, rootFields)
	}
	rootOID := string(rootFields[0])
	if got := invoke(t, root, "show", "--name-status", "-z", rootOID); got.code != 0 || got.stderr != "" || got.stdout != "A\x00docs/a.md\x00A\x00docs/z.md\x00" {
		t.Fatalf("root name-status: %+v", got)
	}

	testrepo.Write(t, root, "docs/a.md", "second\n")
	if err := os.Rename(filepath.Join(root, "docs/z.md"), filepath.Join(root, "docs/m.md")); err != nil {
		t.Fatal(err)
	}
	if got := invoke(t, root, "commit", "-a", "-m", "child snapshot"); got.code != 0 {
		t.Fatalf("child commit: %+v", got)
	}
	childLog := invoke(t, root, "log", "--porcelain=v1", "-z", "--max-count=1")
	childFields := bytes.Split([]byte(childLog.stdout), []byte{0})
	if childLog.code != 0 || len(childFields) != 11 {
		t.Fatalf("child log: %+v fields=%#v", childLog, childFields)
	}
	childOID := string(childFields[0])

	got := invoke(t, root, "show", "--name-status", "-z", childOID)
	if got.code != 0 || got.stderr != "" || got.stdout != "M\x00docs/a.md\x00A\x00docs/m.md\x00D\x00docs/z.md\x00" {
		t.Fatalf("child name-status: %+v", got)
	}
	got = invoke(t, root, "show", "--patch", childOID, "--", "docs/a.md")
	if got.code != 0 || got.stderr != "" || strings.Contains(got.stdout, "child snapshot") ||
		!strings.Contains(got.stdout, "-first\n+second\n") || !strings.HasSuffix(got.stdout, "\n") {
		t.Fatalf("child patch: %+v", got)
	}
	if got := invoke(t, root, "show", "--patch", childOID, "--", "docs/missing.md"); got.code != 0 || got.stderr != "" || got.stdout != "" {
		t.Fatalf("unmatched patch: %+v", got)
	}
	if got := invoke(t, root, "show", "--name-status", "-z", "HEAD..HEAD"); got.code != 1 || got.stdout != "" {
		t.Fatalf("range name-status: %+v", got)
	}
	if got := invoke(t, root, "show", childOID, "--", "docs/a.md"); got.code != 0 || !strings.Contains(got.stdout, "child snapshot") {
		t.Fatalf("human show: %+v", got)
	}
}

func TestCLIShowBlobPreservesExactBytes(t *testing.T) {
	root := testrepo.Init(t)
	testrepo.Write(t, root, "README.md", "main\n")
	testrepo.CommitAll(t, root, "initial", "README.md")
	specialPath := "files/name:part.txt"
	if filepath.Separator == '\\' {
		specialPath = "files/name part.txt"
	}
	contents := map[string][]byte{
		"files/text.txt":   []byte("without newline"),
		"files/empty.txt":  {},
		"files/binary.bin": {0, 0xff, 1, 0},
		specialPath:        []byte("colon\n"),
	}
	for name, content := range contents {
		filename := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := invoke(t, root, "add", "files"); got.code != 0 {
		t.Fatalf("add: %+v", got)
	}
	if got := invoke(t, root, "commit", "-a", "-m", "blob snapshot"); got.code != 0 {
		t.Fatalf("commit: %+v", got)
	}
	log := invoke(t, root, "log", "--porcelain=v1", "-z", "--max-count=1")
	fields := bytes.Split([]byte(log.stdout), []byte{0})
	if log.code != 0 || len(fields) != 11 {
		t.Fatalf("log: %+v fields=%#v", log, fields)
	}
	oid := string(fields[0])

	for path, want := range contents {
		got := invoke(t, root, "show", oid+":"+path)
		if got.code != 0 || got.stderr != "" || got.stdout != string(want) {
			t.Fatalf("show blob %q: %+v, want %v", path, got, want)
		}
	}
	if got := invoke(t, root, "release", "files"); got.code != 0 {
		t.Fatalf("release: %+v", got)
	}
	if got := invoke(t, root, "show", oid+":"+specialPath); got.code != 0 || got.stderr != "" || got.stdout != "colon\n" {
		t.Fatalf("released colon blob: %+v", got)
	}
	if got := invoke(t, root, "show", oid+":files/missing.txt"); got.code != 1 || got.stdout != "" {
		t.Fatalf("missing blob: %+v", got)
	}
	if got := invoke(t, root, "show", "HEAD:../outside.txt"); got.code != 1 || got.stdout != "" {
		t.Fatalf("unsafe blob: %+v", got)
	}
	if got := invoke(t, root, "show", oid, "--", "files/text.txt"); got.code != 0 || !strings.Contains(got.stdout, "blob snapshot") {
		t.Fatalf("human show: %+v", got)
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
