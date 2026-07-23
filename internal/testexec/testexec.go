package testexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

var (
	buildOnce sync.Once
	buildPath string
	buildErr  error
)

// Build compiles and caches a tiny helper executable for the current test process.
func Build(t testing.TB) string {
	t.Helper()

	buildOnce.Do(func() {
		buildPath, buildErr = build(t)
	})
	if buildErr != nil {
		t.Fatalf("build test helper: %v", buildErr)
	}
	return buildPath
}

func build(t testing.TB) (string, error) {
	srcDir := filepath.Join(t.TempDir(), "testexec")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte(helperSource), 0o644); err != nil {
		return "", err
	}

	outDir, err := os.MkdirTemp("", "frigo-testexec-bin-")
	if err != nil {
		return "", err
	}
	name := "git-stub"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	outPath := filepath.Join(outDir, name)

	cmd := exec.Command("go", "build", "-o", outPath, "main.go")
	cmd.Dir = srcDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return outPath, nil
}

const helperSource = `package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
)

func main() {
	if matchFailure() {
		fail("FRIGO_FAIL_STDERR", 42)
	}

	if shouldUpdateHistory() {
		if err := updateHistory(); err != nil {
			exitWithError(err)
		}
	}

	if proxy := os.Getenv("FRIGO_REAL_GIT"); proxy != "" {
		if err := runCommand(proxy, os.Args[1:]...); err != nil {
			exitWithError(err)
		}
		return
	}

	handled := false
	if stdout, ok := os.LookupEnv("FRIGO_OUTPUT"); ok {
		handled = true
		if stdout != "" {
			_, _ = os.Stdout.WriteString(stdout)
		}
	}
	if stderr, ok := os.LookupEnv("FRIGO_STDERR"); ok {
		handled = true
		if stderr != "" {
			_, _ = os.Stderr.WriteString(stderr)
		}
	}
	if exitCode, ok := os.LookupEnv("FRIGO_EXIT_CODE"); ok {
		handled = true
		code, err := strconv.Atoi(exitCode)
		if err != nil {
			_, _ = os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(1)
		}
		os.Exit(code)
	}
	if handled {
		return
	}

	if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}

func shouldUpdateHistory() bool {
	return os.Getenv("FRIGO_REAL_GIT") != "" && os.Getenv("FRIGO_HISTORY_DIR") != "" && commandMatchesUpdateRef() && (os.Getenv("FRIGO_WINNER") != "" || os.Getenv("FRIGO_EXPECTED") != "")
}

func commandMatchesUpdateRef() bool {
	seenUpdateRef := false
	seenHead := false
	for _, arg := range os.Args[1:] {
		if arg == "update-ref" {
			seenUpdateRef = true
		}
		if arg == "HEAD" {
			seenHead = true
		}
	}
	return seenUpdateRef && seenHead
}

func updateHistory() error {
	realGit := os.Getenv("FRIGO_REAL_GIT")
	historyDir := os.Getenv("FRIGO_HISTORY_DIR")
	expected := os.Getenv("FRIGO_EXPECTED")
	winner := os.Getenv("FRIGO_WINNER")

	args := []string{"--git-dir=" + historyDir, "update-ref", "-d", "HEAD"}
	if winner != "" {
		args = []string{"--git-dir=" + historyDir, "update-ref", "HEAD", winner}
	}
	if expected != "" {
		args = append(args, expected)
	}
	return runCommand(realGit, args...)
}

func runCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	return cmd.Run()
}

func fail(env string, code int) {
	if stderr := os.Getenv(env); stderr != "" {
		_, _ = os.Stderr.WriteString(stderr + "\n")
	}
	os.Exit(code)
}

func exitWithError(err error) {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		os.Exit(exitErr.ExitCode())
	}
	_, _ = os.Stderr.WriteString(err.Error() + "\n")
	os.Exit(1)
}

func matchFailure() bool {
	failDir, dirSet := os.LookupEnv("FRIGO_FAIL_GIT_DIR")
	failCommand, commandSet := os.LookupEnv("FRIGO_FAIL_COMMAND")
	failArg, argSet := os.LookupEnv("FRIGO_FAIL_ARG")

	if !dirSet && !commandSet && !argSet {
		return false
	}

	matchDir := failDir == ""
	if !matchDir && len(os.Args) > 1 && os.Args[1] == "--git-dir="+failDir {
		matchDir = true
	}
	if !matchDir {
		return false
	}

	seenCommand := false
	seenArg := false
	for _, arg := range os.Args[1:] {
		if arg == failCommand {
			seenCommand = true
		}
		if failArg == "" || arg == failArg {
			seenArg = true
		}
	}
	return seenCommand && seenArg
}
`
