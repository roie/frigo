package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/roie/frigo/internal/registry"
	"github.com/roie/frigo/internal/repository"
	"github.com/roie/frigo/internal/testrepo"
)

const (
	addLoaded               = "add-loaded"
	releaseLoaded           = "release-loaded"
	releaseRollbackComplete = "release-rollback-complete"
	lockContended           = "lock-contended"
	excludeSync             = "exclude-sync"
)

type cliProcessResult struct {
	err    error
	stdout string
	stderr string
}

type cliProcess struct {
	cmd     *exec.Cmd
	done    chan struct{}
	result  cliProcessResult
	syncDir string
}

type processHooks struct {
	syncDir string
	pause   []string
	fail    []string
}

type contender struct {
	process    *cliProcess
	checkpoint string
}

func TestConcurrentCLIRepositoryOperations(t *testing.T) {
	binary := buildCLI(t)

	t.Run("distinct adds", func(t *testing.T) {
		root := testrepo.Init(t)
		testrepo.Write(t, root, "alpha.local", "alpha\n")
		testrepo.Write(t, root, "beta.local", "beta\n")
		repo := discoverRepository(t, root)
		syncDir := t.TempDir()

		alpha := startCLI(t, binary, root, processHooks{syncDir: syncDir, pause: []string{addLoaded}}, "add", "alpha.local")
		beta := startCLI(t, binary, root, processHooks{syncDir: syncDir, pause: []string{addLoaded}}, "add", "beta.local")
		assertSerializedContenders(t, repo.OperationLockPath,
			contender{process: alpha, checkpoint: addLoaded},
			contender{process: beta, checkpoint: addLoaded},
		)

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
		syncDir := t.TempDir()

		add := startCLI(t, binary, root, processHooks{syncDir: syncDir, pause: []string{addLoaded}}, "add", "new.local")
		release := startCLI(t, binary, root, processHooks{syncDir: syncDir, pause: []string{releaseLoaded}}, "release", "--force", "old.local")
		assertSerializedContenders(t, repo.OperationLockPath,
			contender{process: add, checkpoint: addLoaded},
			contender{process: release, checkpoint: releaseLoaded},
		)

		assertRegistryPaths(t, repo.RegistryPath, "keep.local", "new.local")
		assertExcludePatterns(t, repo.ExcludePath, "keep.local", "new.local")
		assertExcludeOmits(t, repo.ExcludePath, "old.local")
	})

	t.Run("release rollback completes before add proceeds", func(t *testing.T) {
		root := testrepo.Init(t)
		for _, path := range []string{"keep.local", "old.local", "new.local"} {
			testrepo.Write(t, root, path, path+"\n")
		}
		runCLI(t, binary, root, "add", "keep.local", "old.local")
		repo := discoverRepository(t, root)
		syncDir := t.TempDir()

		failedRelease := startCLI(t, binary, root, processHooks{
			syncDir: syncDir,
			pause:   []string{releaseRollbackComplete},
			fail:    []string{excludeSync},
		}, "release", "--force", "old.local")
		waitForProcessEvent(t, failedRelease, releaseRollbackComplete)
		assertLockOwner(t, repo.OperationLockPath, failedRelease)
		assertProcessRunning(t, failedRelease, "release rollback checkpoint")
		assertRegistryPaths(t, repo.RegistryPath, "keep.local", "old.local")
		assertExcludePatterns(t, repo.ExcludePath, "keep.local", "old.local")

		add := startCLI(t, binary, root, processHooks{syncDir: syncDir, pause: []string{addLoaded}}, "add", "new.local")
		waitForProcessEvent(t, add, lockContended)
		assertLockOwner(t, repo.OperationLockPath, failedRelease)
		continueProcess(t, failedRelease, releaseRollbackComplete)
		assertProcessFailure(t, failedRelease, "induced test failure at exclude-sync")

		waitForProcessEvent(t, add, addLoaded)
		assertLockOwner(t, repo.OperationLockPath, add)
		assertRegistryPaths(t, repo.RegistryPath, "keep.local", "old.local")
		continueProcess(t, add, addLoaded)
		assertProcessSuccess(t, add)

		assertRegistryPaths(t, repo.RegistryPath, "keep.local", "new.local", "old.local")
		assertExcludePatterns(t, repo.ExcludePath, "keep.local", "new.local", "old.local")
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
		syncDir := t.TempDir()

		mainAdd := startCLI(t, binary, root, processHooks{syncDir: syncDir, pause: []string{addLoaded}}, "add", "main.local")
		linkedAdd := startCLI(t, binary, linked, processHooks{syncDir: syncDir, pause: []string{addLoaded}}, "add", "linked.local")
		assertSerializedContenders(t, mainRepo.OperationLockPath,
			contender{process: mainAdd, checkpoint: addLoaded},
			contender{process: linkedAdd, checkpoint: addLoaded},
		)

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
	cmd := exec.Command("go", "build", "-tags=frigo_test", "-o", binary, "github.com/roie/frigo/cmd/frigo")
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

func startCLI(t *testing.T, binary, root string, hooks processHooks, args ...string) *cliProcess {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(binary, args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"FRIGO_TEST_SYNC_DIR="+hooks.syncDir,
		"FRIGO_TEST_PAUSE="+strings.Join(hooks.pause, ","),
		"FRIGO_TEST_FAIL="+strings.Join(hooks.fail, ","),
	)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	process := &cliProcess{cmd: cmd, done: make(chan struct{}), syncDir: hooks.syncDir}
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

func assertSerializedContenders(t *testing.T, lockPath string, contenders ...contender) {
	t.Helper()
	if len(contenders) != 2 {
		t.Fatalf("contender count = %d, want 2", len(contenders))
	}

	states := waitForContentionStates(t, contenders)
	loaded := make([]contender, 0, len(contenders))
	for _, candidate := range contenders {
		if states[candidate.process] == candidate.checkpoint {
			loaded = append(loaded, candidate)
		}
	}
	if len(loaded) != 1 {
		t.Errorf("mutual exclusion violated: %d contenders entered vulnerable windows before either could finish", len(loaded))
		for _, candidate := range loaded {
			continueProcess(t, candidate.process, candidate.checkpoint)
		}
		for _, candidate := range contenders {
			assertProcessSuccess(t, candidate.process)
		}
		return
	}

	first := loaded[0]
	assertLockOwner(t, lockPath, first.process)
	continueProcess(t, first.process, first.checkpoint)
	assertProcessSuccess(t, first.process)

	var second contender
	for _, candidate := range contenders {
		if candidate.process != first.process {
			second = candidate
			break
		}
	}
	waitForProcessEvent(t, second.process, second.checkpoint)
	assertLockOwner(t, lockPath, second.process)
	continueProcess(t, second.process, second.checkpoint)
	assertProcessSuccess(t, second.process)
}

func waitForContentionStates(t *testing.T, contenders []contender) map[*cliProcess]string {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	states := make(map[*cliProcess]string, len(contenders))

	for len(states) < len(contenders) {
		for _, candidate := range contenders {
			if eventExists(t, candidate.process, candidate.checkpoint) {
				states[candidate.process] = candidate.checkpoint
				continue
			}
			if _, found := states[candidate.process]; !found && eventExists(t, candidate.process, lockContended) {
				states[candidate.process] = lockContended
			}
			select {
			case <-candidate.process.done:
				if _, found := states[candidate.process]; !found {
					t.Fatalf("frigo exited before synchronization event: err=%v stdout=%q stderr=%q", candidate.process.result.err, candidate.process.result.stdout, candidate.process.result.stderr)
				}
			default:
			}
		}
		if len(states) == len(contenders) {
			return states
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for contender synchronization states: %v", states)
		case <-ticker.C:
		}
	}
	return states
}

func waitForProcessEvent(t *testing.T, process *cliProcess, name string) {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if eventExists(t, process, name) {
			return
		}
		select {
		case <-process.done:
			t.Fatalf("frigo exited before event %q: err=%v stdout=%q stderr=%q", name, process.result.err, process.result.stdout, process.result.stderr)
		case <-deadline.C:
			t.Fatalf("timed out waiting for process %d event %q", process.cmd.Process.Pid, name)
		case <-ticker.C:
		}
	}
}

func eventExists(t *testing.T, process *cliProcess, name string) bool {
	t.Helper()
	_, err := os.Stat(syncPath(process, "event", name))
	if err == nil {
		return true
	}
	if !os.IsNotExist(err) {
		t.Fatalf("stat process event %q: %v", name, err)
	}
	return false
}

func continueProcess(t *testing.T, process *cliProcess, name string) {
	t.Helper()
	if err := os.WriteFile(syncPath(process, "continue", name), nil, 0o600); err != nil {
		t.Fatalf("continue process %d at %q: %v", process.cmd.Process.Pid, name, err)
	}
}

func syncPath(process *cliProcess, kind, name string) string {
	return filepath.Join(process.syncDir, fmt.Sprintf("%s.%d.%s", kind, process.cmd.Process.Pid, name))
}

func assertLockOwner(t *testing.T, filename string, process *cliProcess) {
	t.Helper()
	contents, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("read operation lock: %v", err)
	}
	var owner struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(contents, &owner); err != nil {
		t.Fatalf("decode operation lock: %v", err)
	}
	if owner.PID != process.cmd.Process.Pid {
		t.Fatalf("operation lock owner pid = %d, want contender pid %d", owner.PID, process.cmd.Process.Pid)
	}
}

func assertProcessRunning(t *testing.T, process *cliProcess, checkpoint string) {
	t.Helper()
	select {
	case <-process.done:
		t.Fatalf("frigo exited while paused at %s: err=%v stdout=%q stderr=%q", checkpoint, process.result.err, process.result.stdout, process.result.stderr)
	default:
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

func assertProcessFailure(t *testing.T, process *cliProcess, want string) {
	t.Helper()
	select {
	case <-process.done:
		if process.result.err == nil {
			t.Fatalf("frigo process succeeded, want failure containing %q: stdout=%q stderr=%q", want, process.result.stdout, process.result.stderr)
		}
		if !strings.Contains(process.result.stderr, want) {
			t.Fatalf("frigo failure stderr = %q, want %q", process.result.stderr, want)
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
