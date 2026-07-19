package git

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/roie/frigo/internal/testrepo"
)

func TestParseVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  Version
	}{
		{name: "standard", input: "git version 2.47.3\n", want: Version{Major: 2, Minor: 47, Patch: 3}},
		{name: "apple", input: "git version 2.39.3 (Apple Git-146)\n", want: Version{Major: 2, Minor: 39, Patch: 3}},
		{name: "windows", input: "git version 2.46.0.windows.1\n", want: Version{Major: 2, Minor: 46, Patch: 0}},
		{name: "missing patch", input: "git version 2.23\n", want: Version{Major: 2, Minor: 23, Patch: 0}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseVersion(tt.input)
			if err != nil {
				t.Fatalf("ParseVersion() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseVersion() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseVersionRejectsUnknownOutput(t *testing.T) {
	t.Parallel()

	if _, err := ParseVersion("not git"); err == nil {
		t.Fatal("ParseVersion() error = nil, want error")
	}
}

func TestVersionAtLeast(t *testing.T) {
	t.Parallel()

	minimum := Version{Major: 2, Minor: 23, Patch: 0}
	if !minimum.AtLeast(minimum) {
		t.Fatal("version must satisfy itself")
	}
	if !(Version{Major: 2, Minor: 23, Patch: 1}).AtLeast(minimum) {
		t.Fatal("newer patch should satisfy minimum")
	}
	if (Version{Major: 2, Minor: 22, Patch: 9}).AtLeast(minimum) {
		t.Fatal("older minor should not satisfy minimum")
	}
}

func TestCheckMinimumRejectsOldGit(t *testing.T) {
	t.Parallel()

	path := writeExecutable(t, "git", "#!/bin/sh\necho 'git version 2.22.9'\n")
	client := Client{Path: path}
	err := CheckMinimum(context.Background(), client, Version{Major: 2, Minor: 23, Patch: 0})
	if err == nil {
		t.Fatal("CheckMinimum() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "Git 2.23.0 or newer is required") {
		t.Fatalf("CheckMinimum() error = %q", err)
	}
}

func TestClientReportsExitCodeAndStderr(t *testing.T) {
	t.Parallel()

	path := writeExecutable(t, "git", "#!/bin/sh\necho 'bad command' >&2\nexit 7\n")
	client := Client{Path: path}
	_, err := client.Output(context.Background(), "", "status")
	if err == nil {
		t.Fatal("Output() error = nil, want error")
	}
	commandErr, ok := err.(*CommandError)
	if !ok {
		t.Fatalf("Output() error type = %T, want *CommandError", err)
	}
	if commandErr.ExitCode != 7 {
		t.Fatalf("ExitCode = %d, want 7", commandErr.ExitCode)
	}
	if commandErr.Stderr != "bad command" {
		t.Fatalf("Stderr = %q, want %q", commandErr.Stderr, "bad command")
	}
}

func TestOutputRemovesExactlyOneTerminalLFOrCRLF(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		output string
		want   string
	}{
		{name: "no newline", output: "value with space ", want: "value with space "},
		{name: "one LF", output: "value with spaces  \n", want: "value with spaces  "},
		{name: "double LF", output: "value\n\n", want: "value\n"},
		{name: "one CRLF", output: "value with spaces  \r\n", want: "value with spaces  "},
		{name: "double CRLF", output: "value\r\n\r\n", want: "value\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeExecutable(t, "git", "#!/bin/sh\nprintf '%s' \"$FRIGO_OUTPUT\"\n")
			client := Client{Path: path}.WithEnv("FRIGO_OUTPUT=" + tt.output)
			got, err := client.Output(context.Background(), "")
			if err != nil {
				t.Fatalf("Output() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("Output() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunAgainstRealRepo(t *testing.T) {
	root := testrepo.Init(t)
	client := Client{Path: "git"}
	if got, err := client.Output(context.Background(), root, "rev-parse", "--show-toplevel"); err != nil {
		t.Fatalf("Output() error = %v", err)
	} else if got != root {
		t.Fatalf("Output() = %q, want %q", got, root)
	}
}

func writeExecutable(t *testing.T, name, content string) string {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is required for this test")
	}
	path := t.TempDir() + "/" + name
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	return path
}
