package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	gitpkg "github.com/roie/frigo/internal/git"
	"github.com/roie/frigo/internal/ignore"
	"github.com/roie/frigo/internal/lockfile"
	"github.com/roie/frigo/internal/registry"
	"github.com/roie/frigo/internal/repository"
	"github.com/roie/frigo/internal/testrepo"
)

type cliProcessResult struct {
	err    error
	stdout string
	stderr string
}

type cliProcess struct {
	cmd    *exec.Cmd
	done   chan struct{}
	result cliProcessResult
}

func TestConcurrentCLIRepositoryOperations(t *testing.T) {
	binary := buildCLI(t)

	t.Run("distinct adds", func(t *testing.T) {
		root := testrepo.Init(t)
		testrepo.Write(t, root, "alpha.local", "alpha\n")
		testrepo.Write(t, root, "beta.local", "beta\n")
		repo := discoverRepository(t, root)
		guard, released := holdOperationLock(t, repo.OperationLockPath)

		alpha := startCLI(t, binary, root, "add", "alpha.local")
		beta := startCLI(t, binary, root, "add", "beta.local")
		assertProcessesBlocked(t, alpha, beta)
		releaseOperationLock(t, guard, released)
		assertProcessSuccess(t, alpha)
		assertProcessSuccess(t, beta)

		assertRegistryPaths(t, repo.RegistryPath, "alpha.local", "beta.local")
		assertExcludePatterns(t, repo.ExcludePath, "alpha.local", "beta.local")
	})

	t.Run("add and release preserve both updates", func(t *testing.T) {
		root := testrepo.Init(t)
		for _, path := range []string{"keep.local", "old.local", "new.local"} {
			testrepo.Write(t, root, path, path+"\n")
		}
		runCLI(t, binary, root, "add", "keep.local", "old.local")
		repo := discoverRepository(t, root)
		guard, released := holdOperationLock(t, repo.OperationLockPath)

		add := startCLI(t, binary, root, "add", "new.local")
		release := startCLI(t, binary, root, "release", "--force", "old.local")
		assertProcessesBlocked(t, add, release)
		releaseOperationLock(t, guard, released)
		assertProcessSuccess(t, add)
		assertProcessSuccess(t, release)

		assertRegistryPaths(t, repo.RegistryPath, "keep.local", "new.local")
		assertExcludePatterns(t, repo.ExcludePath, "keep.local", "new.local")
		assertExcludeOmits(t, repo.ExcludePath, "old.local")
	})

	t.Run("main and linked worktrees share contention", func(t *testing.T) {
		root := testrepo.Init(t)
		testrepo.Write(t, root, "README.md", "test\n")
		testrepo.CommitAll(t, root, "initial", "README.md")
		linked := filepath.Join(t.TempDir(), "linked")
		testrepo.Run(t, root, "worktree", "add", "-q", "-b", "linked-concurrency", linked)
		testrepo.Write(t, root, "main.local", "main\n")
		testrepo.Write(t, linked, "linked.local", "linked\n")

		mainRepo := discoverRepository(t, root)
		linkedRepo := discoverRepository(t, linked)
		if mainRepo.OperationLockPath != linkedRepo.OperationLockPath {
			t.Fatalf("operation lock paths differ: %q != %q", mainRepo.OperationLockPath, linkedRepo.OperationLockPath)
		}
		guard, released := holdOperationLock(t, mainRepo.OperationLockPath)

		mainAdd := startCLI(t, binary, root, "add", "main.local")
		linkedAdd := startCLI(t, binary, linked, "add", "linked.local")
		assertProcessesBlocked(t, mainAdd, linkedAdd)
		releaseOperationLock(t, guard, released)
		assertProcessSuccess(t, mainAdd)
		assertProcessSuccess(t, linkedAdd)

		assertRegistryPaths(t, mainRepo.RegistryPath, "main.local")
		assertRegistryPaths(t, linkedRepo.RegistryPath, "linked.local")
		assertExcludePatterns(t, mainRepo.ExcludePath, "main.local", "linked.local")
	})
}

func buildCLI(t *testing.T) string {
	t.Helper()
	name := "frigo"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", binary, "github.com/roie/frigo/cmd/frigo")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build frigo: %v\n%s", err, output)
	}
	return binary
}

func discoverRepository(t *testing.T, root string) repository.Repository {
	t.Helper()
	repo, err := repository.Discover(context.Background(), gitpkg.Client{Path: "git"}, root)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	return repo
}

func holdOperationLock(t *testing.T, filename string) (*lockfile.Lock, *bool) {
	t.Helper()
	guard, err := lockfile.Acquire(context.Background(), filename, "concurrency test", 0)
	if err != nil {
		t.Fatalf("hold operation lock: %v", err)
	}
	released := new(bool)
	t.Cleanup(func() {
		if !*released {
			_ = guard.Release()
		}
	})
	return guard, released
}

func releaseOperationLock(t *testing.T, guard *lockfile.Lock, released *bool) {
	t.Helper()
	if err := guard.Release(); err != nil {
		t.Fatalf("release operation lock: %v", err)
	}
	*released = true
}

func startCLI(t *testing.T, binary, root string, args ...string) *cliProcess {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(binary, args...)
	cmd.Dir = root
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	process := &cliProcess{cmd: cmd, done: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start frigo %v: %v", args, err)
	}
	go func() {
		err := cmd.Wait()
		process.result = cliProcessResult{err: err, stdout: stdout.String(), stderr: stderr.String()}
		close(process.done)
	}()
	t.Cleanup(func() {
		select {
		case <-process.done:
			return
		default:
			_ = process.cmd.Process.Kill()
			<-process.done
		}
	})
	return process
}

func assertProcessesBlocked(t *testing.T, processes ...*cliProcess) {
	t.Helper()
	timer := time.NewTimer(750 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	for _, process := range processes {
		select {
		case <-process.done:
			t.Fatalf("frigo exited while operation lock was held: err=%v stdout=%q stderr=%q", process.result.err, process.result.stdout, process.result.stderr)
		default:
		}
	}
}

func assertProcessSuccess(t *testing.T, process *cliProcess) {
	t.Helper()
	select {
	case <-process.done:
		if process.result.err != nil || process.result.stderr != "" {
			t.Fatalf("frigo process: err=%v stdout=%q stderr=%q", process.result.err, process.result.stdout, process.result.stderr)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("frigo process did not finish")
	}
}

func runCLI(t *testing.T, binary, root string, args ...string) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("frigo %v: %v\n%s", args, err, output)
	}
}

func assertRegistryPaths(t *testing.T, filename string, want ...string) {
	t.Helper()
	got, err := registry.Load(filename)
	if err != nil {
		t.Fatalf("load registry %s: %v", filename, err)
	}
	gotPaths := append([]string(nil), got.Paths...)
	slices.Sort(gotPaths)
	slices.Sort(want)
	if !slices.Equal(gotPaths, want) {
		t.Fatalf("registry paths = %v, want %v", gotPaths, want)
	}
}

func assertExcludePatterns(t *testing.T, filename string, paths ...string) {
	t.Helper()
	contents, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("read exclude %s: %v", filename, err)
	}
	for _, path := range paths {
		pattern, err := ignore.LiteralPattern(path)
		if err != nil {
			t.Fatalf("LiteralPattern(%q): %v", path, err)
		}
		if !strings.Contains(string(contents), pattern) {
			t.Fatalf("exclude = %q, want pattern %q", contents, pattern)
		}
	}
}

func assertExcludeOmits(t *testing.T, filename, path string) {
	t.Helper()
	contents, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("read exclude %s: %v", filename, err)
	}
	pattern, err := ignore.LiteralPattern(path)
	if err != nil {
		t.Fatalf("LiteralPattern(%q): %v", path, err)
	}
	if strings.Contains(string(contents), pattern) {
		t.Fatalf("exclude = %q, unwanted pattern %q", contents, pattern)
	}
}
