package testrepo

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func Init(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}

	root := tempDir(t)
	Run(t, root, "init", "--quiet")
	Run(t, root, "config", "user.name", "Frigo Test")
	Run(t, root, "config", "user.email", "frigo@example.invalid")
	return root
}

func tempDir(t *testing.T) string {
	t.Helper()
	if info, err := os.Stat("/dev/shm"); err == nil && info.IsDir() {
		root, err := os.MkdirTemp("/dev/shm", "frigo-test-*")
		if err == nil {
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			return root
		}
	}
	return t.TempDir()
}

func Run(t *testing.T, root string, args ...string) {
	t.Helper()
	output, err := gitOutput(root, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func Output(t *testing.T, root string, args ...string) string {
	t.Helper()
	output, err := gitOutput(root, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return trimGitOutput(output)
}

func Write(t *testing.T, root, rel, contents string) {
	t.Helper()
	filename, err := resolveFixturePath(root, rel)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func Read(t *testing.T, root, rel string) string {
	t.Helper()
	filename, err := resolveFixturePath(root, rel)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func CommitAll(t *testing.T, root, message string, paths ...string) {
	t.Helper()
	if len(paths) == 0 {
		t.Fatal("CommitAll requires at least one explicit path")
	}
	args := append([]string{"add", "--"}, paths...)
	output, err := gitOutputWithEnv(root, []string{"GIT_LITERAL_PATHSPECS=1"}, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	Run(t, root, "commit", "-q", "-m", message)
}

func gitOutput(root string, args ...string) (string, error) {
	return gitOutputWithEnv(root, nil, args...)
}

func gitOutputWithEnv(root string, extraEnv []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), append([]string{"GIT_CONFIG_NOSYSTEM=1"}, extraEnv...)...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func resolveFixturePath(root, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("invalid fixture path %q", rel)
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("fixture path must be relative: %q", rel)
	}
	cleanRel := filepath.Clean(rel)
	if cleanRel != rel || cleanRel == "." || cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("fixture path escapes root: %q", rel)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve fixture root: %w", err)
	}
	rootAbs = filepath.Clean(rootAbs)
	target := filepath.Clean(filepath.Join(rootAbs, rel))
	relative, err := filepath.Rel(rootAbs, target)
	if err != nil {
		return "", fmt.Errorf("resolve fixture path: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("fixture path escapes root: %q", rel)
	}
	return target, nil
}

func trimGitOutput(output string) string {
	if strings.HasSuffix(output, "\r\n") {
		return strings.TrimSuffix(output, "\r\n")
	}
	return strings.TrimSuffix(output, "\n")
}
