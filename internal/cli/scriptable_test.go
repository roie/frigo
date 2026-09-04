package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/roie/frigo/internal/testrepo"
)

func TestMachineSpecialPathsAndLinkedWorktree(t *testing.T) {
	for _, linked := range []bool{false, true} {
		name := "main"
		if linked {
			name = "linked"
		}
		t.Run(name, func(t *testing.T) {
			mainRoot := testrepo.Init(t)
			testrepo.Write(t, mainRoot, "README.md", "main\n")
			testrepo.CommitAll(t, mainRoot, "initial", "README.md")
			root := mainRoot
			if linked {
				root = filepath.Join(t.TempDir(), "linked")
				testrepo.Run(t, mainRoot, "worktree", "add", "-q", "-b", "machine-test", root)
			}
			paths := []string{"space name.md", "-leading.md", "literal[1].md", "éclair.md", "日本語.md"}
			if runtime.GOOS != "windows" {
				paths = append(paths, "tab\tname.md", "quote\"name.md", "back\\slash.md")
			}
			slices.Sort(paths)
			content := []byte{0, 0xff, '\n', 1}
			for _, path := range paths {
				if err := os.WriteFile(filepath.Join(root, path), content, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := invoke(t, root, append([]string{"add", "--"}, paths...)...); got.code != 0 {
				t.Fatalf("add: %+v", got)
			}
			wantList := strings.Join(paths, "\x00") + "\x00"
			for _, command := range []string{"list", "ls"} {
				assertMachineBytes(t, root, []byte(wantList), command, "-z")
			}
			var status, changes bytes.Buffer
			for _, path := range paths {
				status.WriteString(" A " + path + "\x00")
				changes.WriteString("A\x00" + path + "\x00")
				assertMachineBytes(t, root, []byte(" A "+path+"\x00"), "status", "--porcelain=v1", "-z", "--", path)
			}
			assertMachineBytes(t, root, status.Bytes(), "status", "--porcelain=v1", "-z")
			if got := invoke(t, root, "commit", "-a", "-m", "special paths"); got.code != 0 {
				t.Fatalf("commit: %+v", got)
			}
			assertMachineBytes(t, root, nil, "status", "--porcelain=v1", "-z")
			assertMachineBytes(t, root, changes.Bytes(), "show", "--name-status", "-z", "HEAD")
			for _, path := range paths {
				assertMachineBytes(t, root, content, "show", "HEAD:"+path)
				assertMachineBytes(t, root, []byte("A\x00"+path+"\x00"), "show", "--name-status", "-z", "HEAD", "--", path)
			}
			if linked {
				// Initializing the linked store must not initialize or expose it
				// through the main worktree's independent Frigo commands.
				for _, args := range [][]string{{"list", "-z"}, {"log", "--porcelain=v1", "-z"}} {
					got := invoke(t, mainRoot, args...)
					if got.code != 1 || got.stdout != "" || !strings.Contains(got.stderr, "not initialized") {
						t.Fatalf("main worktree %q: %+v", args, got)
					}
				}
			}
		})
	}
}

func assertMachineBytes(t *testing.T, root string, want []byte, args ...string) {
	t.Helper()
	got := invoke(t, root, args...)
	if got.code != 0 || got.stderr != "" || !bytes.Equal([]byte(got.stdout), want) {
		t.Fatalf("%q: code=%d stderr=%q stdout=%q, want %q", args, got.code, got.stderr, got.stdout, want)
	}
}
