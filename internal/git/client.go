package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Client invokes the user's Git executable directly, without a shell.
type Client struct {
	Path     string
	ExtraEnv []string
}

func (c Client) executable() string {
	if c.Path == "" {
		return "git"
	}
	return c.Path
}

func (c Client) command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	return c.commandWithLiteralPathspecs(ctx, dir, true, args...)
}

func (c Client) commandWithLiteralPathspecs(ctx context.Context, dir string, literal bool, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.executable(), args...)
	if dir != "" {
		cmd.Dir = dir
	}
	literalValue := "GIT_LITERAL_PATHSPECS=0"
	if literal {
		literalValue = "GIT_LITERAL_PATHSPECS=1"
	}
	cmd.Env = mergeEnvironment(os.Environ(), append(append([]string(nil), c.ExtraEnv...),
		literalValue,
		"LC_ALL=C",
	)...)
	return cmd
}

// WithEnv returns a copy of Client with additional environment assignments.
func (c Client) WithEnv(assignments ...string) Client {
	copyClient := c
	copyClient.ExtraEnv = append(append([]string(nil), c.ExtraEnv...), assignments...)
	return copyClient
}

func mergeEnvironment(base []string, assignments ...string) []string {
	result := append([]string(nil), base...)
	positions := make(map[string]int, len(result))
	for i, entry := range result {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			key = entry
		}
		positions[key] = i
	}
	for _, assignment := range assignments {
		key, _, found := strings.Cut(assignment, "=")
		if !found {
			key = assignment
		}
		if position, ok := positions[key]; ok {
			result[position] = assignment
			continue
		}
		positions[key] = len(result)
		result = append(result, assignment)
	}
	return result
}

// Output runs Git and returns stdout with only one terminal newline removed.
func (c Client) Output(ctx context.Context, dir string, args ...string) (string, error) {
	return c.OutputWithInput(ctx, dir, "", args...)
}

// OutputWithInput runs Git with stdin and returns stdout with only one terminal newline removed.
func (c Client) OutputWithInput(ctx context.Context, dir, input string, args ...string) (string, error) {
	return c.outputWithInput(ctx, dir, input, true, args...)
}

// OutputWithInputNoLiteralPathspecs runs Git with stdin and literal pathspec mode disabled.
func (c Client) OutputWithInputNoLiteralPathspecs(ctx context.Context, dir, input string, args ...string) (string, error) {
	return c.outputWithInput(ctx, dir, input, false, args...)
}

func (c Client) outputWithInput(ctx context.Context, dir, input string, literalPathspecs bool, args ...string) (string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := c.commandWithLiteralPathspecs(ctx, dir, literalPathspecs, args...)
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", newCommandError(args, stderr.String(), err)
	}
	return trimTerminalNewline(stdout.String()), nil
}

// Run runs Git with caller-provided streams.
func (c Client) Run(ctx context.Context, dir string, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	cmd := c.command(ctx, dir, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return newCommandError(args, "", err)
	}
	return nil
}

// CommandError describes a Git process that could not be started or exited unsuccessfully.
type CommandError struct {
	Args     []string
	ExitCode int
	Stderr   string
	Err      error
}

func (e *CommandError) Error() string {
	if e.Stderr != "" {
		return e.Stderr
	}
	if e.ExitCode >= 0 {
		return fmt.Sprintf("git %s exited with status %d", strings.Join(e.Args, " "), e.ExitCode)
	}
	return fmt.Sprintf("run git %s: %v", strings.Join(e.Args, " "), e.Err)
}

func (e *CommandError) Unwrap() error { return e.Err }

func newCommandError(args []string, stderr string, err error) *CommandError {
	exitCode := -1
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode = exitErr.ExitCode()
	}
	return &CommandError{
		Args:     append([]string(nil), args...),
		ExitCode: exitCode,
		Stderr:   strings.TrimSpace(stderr),
		Err:      err,
	}
}

// ExitCode extracts an exit status from an error returned by Client.
func ExitCode(err error) (int, bool) {
	var commandErr *CommandError
	if !errors.As(err, &commandErr) || commandErr.ExitCode < 0 {
		return 0, false
	}
	return commandErr.ExitCode, true
}

func trimTerminalNewline(output string) string {
	if trimmed, ok := strings.CutSuffix(output, "\r\n"); ok {
		return trimmed
	}
	if trimmed, ok := strings.CutSuffix(output, "\n"); ok {
		return trimmed
	}
	return output
}
