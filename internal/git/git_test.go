package git

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roie/frigo/internal/testexec"
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

	client := stubClient(t, "FRIGO_OUTPUT=git version 2.22.9\n")
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

	client := stubClient(t, "FRIGO_STDERR=bad command\n", "FRIGO_EXIT_CODE=7")
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

func TestOutputBytesPreservesExactBytes(t *testing.T) {
	root := testrepo.Init(t)
	client := Client{Path: "git"}
	tests := []struct {
		name    string
		payload []byte
	}{
		{name: "empty"},
		{name: "terminal LF", payload: []byte("value\n")},
		{name: "terminal CRLF", payload: []byte("value\r\n")},
		{name: "multiple terminal newlines", payload: []byte("value\n\n")},
		{name: "NUL bytes", payload: []byte{'a', 0, 'b', 0}},
		{name: "non UTF-8 bytes", payload: []byte{0xff, 0xfe, '\n'}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oidOutput, err := client.OutputBytesWithInput(context.Background(), root, tt.payload, "hash-object", "-w", "--stdin")
			if err != nil {
				t.Fatalf("hash blob: %v", err)
			}
			oid := strings.TrimSpace(string(oidOutput))
			got, err := client.OutputBytes(context.Background(), root, "cat-file", "blob", oid)
			if err != nil {
				t.Fatalf("OutputBytes() error = %v", err)
			}
			if !bytes.Equal(got, tt.payload) {
				t.Fatalf("OutputBytes() = %v, want %v", got, tt.payload)
			}
		})
	}
}

func TestOutputBytesWithInputPreservesBatchResponse(t *testing.T) {
	root := testrepo.Init(t)
	client := Client{Path: "git"}
	payload := []byte{'a', 0, 0xff, '\n'}
	oidOutput, err := client.OutputBytesWithInput(context.Background(), root, payload, "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatalf("hash blob: %v", err)
	}
	oid := strings.TrimSpace(string(oidOutput))

	got, err := client.OutputBytesWithInput(context.Background(), root, []byte(oid+"\n"), "cat-file", "--batch")
	if err != nil {
		t.Fatalf("OutputBytesWithInput() error = %v", err)
	}
	want := append([]byte(fmt.Sprintf("%s blob %d\n", oid, len(payload))), payload...)
	want = append(want, '\n')
	if !bytes.Equal(got, want) {
		t.Fatalf("OutputBytesWithInput() = %v, want %v", got, want)
	}
}

func TestOutputBytesDiscardsStdoutOnFailure(t *testing.T) {
	client := stubClient(t, "FRIGO_OUTPUT=partial", "FRIGO_STDERR=bad command\n", "FRIGO_EXIT_CODE=7")
	got, err := client.OutputBytes(context.Background(), "", "status")
	if err == nil {
		t.Fatal("OutputBytes() error = nil, want error")
	}
	if got != nil {
		t.Fatalf("OutputBytes() = %v, want nil", got)
	}
	commandErr, ok := err.(*CommandError)
	if !ok {
		t.Fatalf("OutputBytes() error type = %T, want *CommandError", err)
	}
	if commandErr.ExitCode != 7 || commandErr.Stderr != "bad command" {
		t.Fatalf("OutputBytes() error = %#v", commandErr)
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
			client := stubClient(t, "FRIGO_OUTPUT="+tt.output)
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
	} else if normalized := filepath.Clean(filepath.FromSlash(got)); normalized != root {
		t.Fatalf("Output() = %q (normalized %q), want %q", got, normalized, root)
	}
}

func stubClient(t *testing.T, env ...string) Client {
	t.Helper()
	return Client{Path: testexec.Build(t)}.WithEnv(env...)
}
