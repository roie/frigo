package testexec_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/roie/frigo/internal/testexec"
)

func TestBuildIsReusable(t *testing.T) {
	t.Parallel()

	got1 := testexec.Build(t)
	got2 := testexec.Build(t)
	if got1 != got2 {
		t.Fatalf("Build() = %q then %q, want one cached helper per process", got1, got2)
	}
}

func TestBuildPassesStdinAndConfiguredStreams(t *testing.T) {
	t.Parallel()

	path := testexec.Build(t)
	cmd := exec.Command(path)
	cmd.Env = append(os.Environ(),
		"FRIGO_OUTPUT=stdout line\n",
		"FRIGO_STDERR=stderr line\n",
		"FRIGO_EXIT_CODE=7",
	)
	cmd.Stdin = bytes.NewBufferString("input line\n")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Run() error = %v, want exit error", err)
	}
	if code := exitErr.ExitCode(); code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}
	if got := stdout.String(); got != "stdout line\n" {
		t.Fatalf("stdout = %q, want %q", got, "stdout line\n")
	}
	if got := stderr.String(); got != "stderr line\n" {
		t.Fatalf("stderr = %q, want %q", got, "stderr line\n")
	}
}

func TestBuildFailsOnMatchingCommandAndArg(t *testing.T) {
	t.Parallel()

	path := testexec.Build(t)
	cmd := exec.Command(path, "status", "--porcelain")
	cmd.Env = append(os.Environ(),
		"FRIGO_FAIL_COMMAND=status",
		"FRIGO_FAIL_ARG=--porcelain",
		"FRIGO_FAIL_STDERR=forced git failure",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Run() error = %v, want exit error", err)
	}
	if code := exitErr.ExitCode(); code != 42 {
		t.Fatalf("exit code = %d, want 42", code)
	}
	if got := stderr.String(); got != "forced git failure\n" {
		t.Fatalf("stderr = %q, want %q", got, "forced git failure\n")
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("stdout = %q, want empty", got)
	}
}

func TestBuildDefaultsToCopyingStdout(t *testing.T) {
	t.Parallel()

	path := testexec.Build(t)
	cmd := exec.Command(path)
	cmd.Stdin = bytes.NewBufferString("hello\nworld")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := stdout.String(); got != "hello\nworld" {
		t.Fatalf("stdout = %q, want %q", got, "hello\nworld")
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
}
